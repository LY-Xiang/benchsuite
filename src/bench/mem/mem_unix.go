//go:build linux || darwin

package mem

// Peak resident set and CPU time on Unix, both from the wait4 that reaped the
// child.
//
// This is the good case: wait4 fills a rusage for that one child, and its Maxrss
// is the kernel's high-water mark while its Utime and Stime are the exact CPU it
// consumed. No polling, no handle, no frequency to guess -- which is why Unix needs
// no sampler and this file reads the process state instead of the handle.

import (
	"os"
	"syscall"
	"time"

	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
)

// processState is the accounting the wait produced. It is carried on the handle
// exec hands over, because the handle is the only thing an observer sees.
func peakOf(proc execpkg.Process) (int64, bool) {
	state := stateOf(proc)
	if state == nil {
		return 0, false
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return 0, false
	}
	// Maxrss is kilobytes on Linux and bytes on Darwin. The two are different
	// units with the same field name, so the conversion is per platform rather
	// than a shared constant -- getting it wrong under-reports by 1024x.
	return int64(usage.Maxrss) * maxrssScale, true
}

func cpuOf(proc execpkg.Process) (time.Duration, bool) {
	state := stateOf(proc)
	if state == nil {
		return 0, false
	}
	usage, ok := state.SysUsage().(*syscall.Rusage)
	if !ok || usage == nil {
		return 0, false
	}
	return timeval(usage.Utime) + timeval(usage.Stime), true
}

func timeval(tv syscall.Timeval) time.Duration {
	return time.Duration(tv.Sec)*time.Second + time.Duration(tv.Usec)*time.Microsecond
}

func stateOf(proc execpkg.Process) *os.ProcessState {
	state, _ := proc.Sampler.(*os.ProcessState)
	return state
}
