//go:build linux

package peakmem

// Peak resident set on Linux, read from /proc/<pid>/status VmHWM.
//
// VmHWM is the kernel's own high-water mark, so a sample taken at any moment
// reports the peak up to that moment, not the current size. That makes polling
// correct rather than racy: the last sample a process answers before it exits is
// its peak, and a spike between two samples is still counted because the kernel
// recorded it.
//
// The one shortfall is the final sliver between the last sample and exit, which
// is why the interval is short relative to a case rather than microseconds. The
// number is therefore a lower bound on the true peak that is exact for any run
// lasting longer than a few sample intervals -- which every measured case is,
// since a case that finishes faster than that reports `work=<floor>` anyway.
//
// Polling is not an option taken for convenience: the alternative readings are
// worse. getrusage(RUSAGE_CHILDREN) reports the maximum over *all* reaped
// children, so it cannot be differenced to one process, and wait4 cannot supply
// per-child rusage without displacing the wait exec.Cmd owns.

import (
	"os"
	"strconv"
	"strings"
	"sync"
	"time"
)

// pollInterval bounds the error at the tail: the true peak is at most
// one interval of growth past the last sample.
const pollInterval = 2 * time.Millisecond

// VmHWM is reported in kB regardless of the architecture's page size.
const bytesPerKB = 1024

type linux struct {
	path string
	stop chan struct{}
	done chan struct{}

	mu       sync.Mutex
	peak     int64
	observed bool
}

func Begin(pid int) Handle {
	h := &linux{
		path: "/proc/" + strconv.Itoa(pid) + "/status",
		stop: make(chan struct{}),
		done: make(chan struct{}),
	}
	go h.poll()
	return h
}

// poll samples until the process is gone or release closes stop. A closed stop is
// checked before each read, so release is not delayed by a sample in flight.
func (h *linux) poll() {
	defer close(h.done)
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		if value, ok := peakOf(h.path); ok {
			h.mu.Lock()
			// VmHWM only grows, so a smaller sample is a torn read and is ignored.
			if value > h.peak {
				h.peak = value
			}
			h.observed = true
			h.mu.Unlock()
		} else {
			// The process is gone; nothing further can be learned.
			return
		}
		select {
		case <-h.stop:
			return
		case <-ticker.C:
		}
	}
}

func (h *linux) Read() (int64, bool) {
	// One last sample, since the child's true peak may have landed after the
	// final tick.
	if value, ok := peakOf(h.path); ok {
		h.mu.Lock()
		if value > h.peak {
			h.peak = value
		}
		h.observed = true
		h.mu.Unlock()
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if !h.observed {
		return 0, false
	}
	return h.peak, true
}

func (h *linux) Release() {
	select {
	case <-h.stop:
	default:
		close(h.stop)
	}
	<-h.done
}

// peakOf reads one VmHWM value. A missing file, a missing key or an unparsable
// number is a failed sample, never a zero measurement.
func peakOf(path string) (int64, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	for line := range strings.SplitSeq(string(data), "\n") {
		rest, found := strings.CutPrefix(line, "VmHWM:")
		if !found {
			continue
		}
		fields := strings.Fields(rest)
		if len(fields) == 0 {
			return 0, false
		}
		kb, err := strconv.ParseInt(fields[0], 10, 64)
		if err != nil || kb < 0 {
			return 0, false
		}
		return kb * bytesPerKB, true
	}
	return 0, false
}
