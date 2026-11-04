package bench

import (
	"fmt"
	"strings"
	"time"

	"github.com/LY-Xiang/benchsuite/src/bench/clock"
	"github.com/LY-Xiang/benchsuite/src/bench/mem"
	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
)

// Measuring: the campaign loop, and what a refused row says.

// trials and runsPerTrial shape one campaign. The best of several averages is taken
// rather than one average, so a single scheduling hiccup is outvoted instead of
// being reported as the program's cost.
const (
	trials       = 3
	runsPerTrial = 10
)

// Measure times argv against the empty-program floor on the same input.
//
// The two campaigns are measured on the same machine in the same call, and each
// number in the Reading comes from a trial of the program it describes. A failure
// in either refuses the row: a row whose floor is missing would report work it
// cannot support, and the error says which program failed and how.
//
// observers are the facts this package asks for, in the order it unpacks them.
// Each is a separate observer so one that cannot answer -- an unsupported platform,
// a failed syscall -- leaves its own column absent without touching the others.
//
// timeout bounds each execution, so a `--timeout` can tighten a run that would
// otherwise sit at the default for every trial. Zero keeps exec's default.
func Measure(argv []string, input []byte, floor string, timeout time.Duration,
	observe ...execpkg.Observer,
) (Reading, error) {
	floorCampaign, detail, ok := campaign([]string{floor}, input, "launch floor", timeout, observe)
	if !ok {
		return Reading{}, fmt.Errorf("launch floor could not be timed: %s", detail)
	}
	solution, detail, ok := campaign(argv, input, "solution", timeout, observe)
	if !ok {
		return Reading{}, fmt.Errorf("solution could not be timed: %s", detail)
	}
	return solution.Against(floorCampaign), nil
}

// campaign is the best of trials averages of runsPerTrial executions, and whether
// every run finished.
//
// A child killed at the deadline has the kill's latency as its wall time and a zero
// exit code, so TimedOut is checked too. Any contaminated run refuses the whole
// campaign, even in a trial that was not the best: a row built from a campaign that
// partly failed would hide the failure behind a plausible number.
//
// label names the program being timed and is used only to phrase the refusal, so a
// caller can tell the launch floor apart from the solution. The detail string says
// which run failed and how; it is empty when ok is true.
func campaign(argv []string, input []byte, label string, timeout time.Duration,
	observe []execpkg.Observer,
) (Campaign, string, bool) {
	var (
		trialList []clock.Trial
		peak      int64
		peakKnown bool
	)
	for trial := 1; trial <= trials; trial++ {
		samples := make([]clock.Sample, 0, runsPerTrial)
		for run := 1; run <= runsPerTrial; run++ {
			started := time.Now()
			observed := execpkg.RunProbed(argv, input, timeout, observe...)
			elapsed := time.Since(started)
			result := observed.RunResult
			sample := clock.Sample{Wall: elapsed}

			if result.TimedOut {
				return Campaign{}, fmt.Sprintf("%s timed out (trial %d, run %d)", label, trial, run), false
			}
			if result.Code != 0 {
				detail := fmt.Sprintf("%s exited with code %d (trial %d, run %d)",
					label, result.Code, trial, run)
				if stderr := strings.TrimSpace(strings.ToValidUTF8(string(result.Stderr), "\uFFFD")); stderr != "" {
					detail += ": " + firstLine(stderr)
				}
				return Campaign{}, detail, false
			}
			// Each observation is unpacked at its own index, so an observer that
			// could not answer leaves only its own column absent.
			for i, reading := range observed.Readings {
				if reading == nil {
					continue
				}
				switch value := reading.(type) {
				case mem.Peak:
					// The largest sample across the campaign.
					peakKnown = true
					peak = max(peak, int64(value))
				case mem.CPU:
					sample.CPUKnown = true
					sample.CPU = time.Duration(value)
				default:
					panic(fmt.Sprintf("bench: unknown reading %d has type %T", i, reading))
				}
			}
			samples = append(samples, sample)
		}
		averaged, ok := clock.Average(samples)
		if !ok {
			return Campaign{}, label + " produced no usable trial", false
		}
		trialList = append(trialList, averaged)
	}
	best, ok := clock.Best(trialList)
	if !ok {
		return Campaign{}, label + " produced no usable trial", false
	}
	return Campaign{
		Trials:    trialList,
		Best:      trialList[best],
		Peak:      peak,
		PeakKnown: peakKnown,
	}, "", true
}

// firstLine returns s up to its first newline, so a multi-line diagnostic stays
// readable inside the one line a refusal is printed on.
func firstLine(s string) string {
	if before, _, found := strings.Cut(s, "\n"); found {
		return before
	}
	return s
}
