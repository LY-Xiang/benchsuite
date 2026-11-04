package main

import (
	"fmt"
	"os"
	"strings"
	"time"

	execpkg "lyxiang/benchsuite/src/exec"
	"lyxiang/benchsuite/src/peakmem"
	"lyxiang/benchsuite/src/registry"
	"lyxiang/benchsuite/src/text"
)

// nopSource is the empty program whose launch and dynamic-link cost is the floor a
// timing row subtracts. Kept as source, since the harness writes it beside the
// solution it times.
const nopSource = "int main() { return 0; }\n"

// A BenchRow is one timing measurement. WallUs is the solution's own time;
// WorkUs is what is left after the empty-program launch cost is taken out.
type BenchRow struct {
	Problem    string
	Shape      string
	InputBytes int
	WallUs     int
	WorkUs     int
	FloorUs    int
	TL         string
	// PeakBytes is the solution's peak resident set, or peakmem.Unknown when the
	// platform cannot report one. FloorPeakBytes is the same for the empty program,
	// so the row can say how much memory the solution needed beyond a bare launch.
	PeakBytes      int64
	FloorPeakBytes int64
}

// FormatBenchRow renders one timing row. When the wall time is at or under the
// empty-program floor, the work is unresolvable, so the row says
// `work=<floor (n us)>` rather than claiming a number it cannot support.
func FormatBenchRow(row BenchRow) string {
	work := fmt.Sprintf("work=%dus", row.WorkUs)
	if row.WallUs <= row.FloorUs {
		work = fmt.Sprintf("work=<floor (%d us)>", row.FloorUs)
	}
	suffix := ""
	if row.TL != "" {
		suffix = " " + row.TL
	}
	return fmt.Sprintf("%s %s input=%dB wall=%dus %s %s%s",
		row.Problem, row.Shape, row.InputBytes, row.WallUs, work, formatPeak(row), suffix)
}

// formatPeak renders the memory column. The floor is subtracted because a C++
// binary's launch cost is tens of MiB of runtime and loader pages that have
// nothing to do with the solution's own data structures; what a solver cares about
// is the delta. Both halves stay visible so the subtraction can be checked.
//
// An unmeasured run prints `mem=n/a` rather than 0B: zero would read as "used no
// memory", which is a stronger and false claim.
func formatPeak(row BenchRow) string {
	if row.PeakBytes == peakmem.Unknown {
		return "mem=n/a"
	}
	delta := max(row.PeakBytes-row.FloorPeakBytes, 0)
	return fmt.Sprintf("mem=%s (peak=%s floor=%s)",
		formatBytes(delta), formatBytes(row.PeakBytes), formatBytes(row.FloorPeakBytes))
}

// formatBytes renders a byte count at a readable magnitude, keeping one decimal
// so two nearby rows stay distinguishable.
func formatBytes(n int64) string {
	const unit = 1024
	switch {
	case n < unit:
		return fmt.Sprintf("%dB", n)
	case n < unit*unit:
		return fmt.Sprintf("%.1fKiB", float64(n)/unit)
	case n < unit*unit*unit:
		return fmt.Sprintf("%.1fMiB", float64(n)/(unit*unit))
	default:
		return fmt.Sprintf("%.1fGiB", float64(n)/(unit*unit*unit))
	}
}

// RunBench times one problem's solution against the empty-program launch floor,
// always on the problem's largest input (MaxGen), since a timing row is about the
// advertised worst case. The returned BenchRow is usable only when err is nil;
// otherwise err says which step refused, because a bare "could not be measured"
// leaves an author with no idea whether to fix the source, the registry or the
// machine.
//
// timeout bounds each timed execution, so `--timeout` can tighten a run that would
// otherwise sit at the default for its whole trial. Zero keeps the default.
func (e *Env) RunBench(p registry.Problem, timeout time.Duration) (BenchRow, error) {
	work, err := os.MkdirTemp("", "bench-time-"+p.ID+"-*")
	if err != nil {
		return BenchRow{}, fmt.Errorf("cannot create a work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// The same preparation the gate runner does: a case that would fail a gate is
	// not one whose timing means anything.
	solution, failed := e.buildSolution(work, p)
	if failed != nil {
		return BenchRow{}, fmt.Errorf("solution did not build: %s", text.Text(failed.Detail))
	}
	input, _, _, failed := generateInput(e, p, work, p.MaxGen, 1)
	if failed != nil {
		return BenchRow{}, fmt.Errorf("generator produced no usable input: %s", text.Text(failed.Detail))
	}

	// The launch floor is the same empty program for every problem, so it is
	// compiled once per process and reused; only the timing is per problem. Its
	// build cost used to be paid again for each row of a sweep.
	nop, err := e.launchFloor()
	if err != nil {
		return BenchRow{}, err
	}

	// Timed in the same loop as the solution, so both see the same machine. The
	// memory reading rides along with the timing runs rather than costing its own
	// launches, and it is taken on the fastest trial so it belongs to the same
	// execution the wall time came from.
	floor, detail, ok := e.measureRuns([]string{nop}, input, "launch floor", timeout)
	if !ok {
		return BenchRow{}, fmt.Errorf("launch floor could not be timed: %s", detail)
	}
	measured, detail, ok := e.measureRuns([]string{solution}, input, "solution", timeout)
	if !ok {
		return BenchRow{}, fmt.Errorf("solution could not be timed: %s", detail)
	}
	wallUs, floorUs := measured.wallUs, floor.wallUs
	// A hiccup on the floor alone can put the solution "faster" than an empty
	// program; that is noise, so work floors at zero and the row renders the
	// floor form.
	workUs := max(wallUs-floorUs, 0)
	return BenchRow{
		Problem: p.ID, Shape: p.Shape, InputBytes: len(input),
		WallUs: wallUs, WorkUs: workUs, FloorUs: floorUs,
		TL:        timeLimitLabel(p),
		PeakBytes: measured.peakBytes, FloorPeakBytes: floor.peakBytes,
	}, nil
}

// runMeasurement is one program's fastest trial, with the peak memory observed in
// that same trial. Pairing them keeps the two numbers describing one execution.
type runMeasurement struct {
	wallUs    int
	peakBytes int64
}

// measureRuns is the best of benchTrials averages of benchRuns executions, and
// whether every run finished. The best average, not the mean, keeps one scheduling
// hiccup from being reported as the solution's cost.
//
// Memory comes from the run that set the best trial rather than from a separate
// batch: a second batch would time different executions, and the point of the row
// is that both numbers describe the same thing.
//
// A child killed at the deadline has the kill's latency as its wall time and a zero
// exit code, so TimedOut is checked too. Any contaminated run refuses the whole
// row, even in a trial that was not the best.
//
// label names the program being timed and is used only to phrase the refusal, so a
// caller can tell the launch floor apart from the solution. The detail string says
// which run failed and how; it is empty when ok is true.
//
// timeout bounds each execution; zero keeps the exec package's default. A run that
// hits it refuses the row at once rather than repeating it for every trial.
func (e *Env) measureRuns(argv []string, input []byte, label string, timeout time.Duration) (runMeasurement, string, bool) {
	best := runMeasurement{wallUs: -1, peakBytes: peakmem.Unknown}
	for trial := 1; trial <= benchTrials; trial++ {
		started := time.Now()
		peak := peakmem.Unknown
		for run := 1; run <= benchRuns; run++ {
			result := execpkg.RunMeasured(argv, input, timeout)
			if result.TimedOut {
				return runMeasurement{}, fmt.Sprintf("%s timed out (trial %d, run %d)", label, trial, run), false
			}
			if result.Code != 0 {
				detail := fmt.Sprintf("%s exited with code %d (trial %d, run %d)",
					label, result.Code, trial, run)
				if stderr := strings.TrimSpace(lossyString(result.Stderr)); stderr != "" {
					detail += ": " + firstLine(stderr)
				}
				return runMeasurement{}, detail, false
			}
			// The largest sample in the trial: a run whose measurement failed reports
			// unknown, and unknown must not be mistaken for small.
			if result.PeakBytes > peak {
				peak = result.PeakBytes
			}
		}
		elapsed := int(time.Since(started).Microseconds() / int64(benchRuns))
		if best.wallUs < 0 || elapsed < best.wallUs {
			best = runMeasurement{wallUs: elapsed, peakBytes: peak}
		}
	}
	if best.wallUs < 0 {
		return runMeasurement{}, label + " produced no usable trial", false
	}
	return best, "", true
}

// firstLine returns s up to its first newline, so a multi-line diagnostic stays
// readable inside the one line a refusal is printed on.
func firstLine(s string) string {
	if before, _, found := strings.Cut(s, "\n"); found {
		return before
	}
	return s
}
