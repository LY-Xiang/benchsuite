package report

import (
	"fmt"

	"github.com/LY-Xiang/benchsuite/src/bench"
)

// Rendering a timing row.

// BenchRow renders one row: the problem's own words followed by the measurement.
//
// The limit is rendered here rather than stored pre-formatted, so a caller passing
// the registry's raw value still gets the `TL ` prefix and an empty one still
// renders nothing.
func BenchRow(row bench.Row) string {
	wallUs := int(row.Reading.Wall.Microseconds())
	floorUs := int(row.Reading.Floor.Microseconds())
	work := fmt.Sprintf("work=%dus", int(row.Reading.Work.Microseconds()))
	// The floor form is decided from the durations, not from the rendered
	// microseconds: Reading.Work is already floored at zero, so it cannot tell a
	// genuine zero from a solution that came in under the empty program.
	if row.Reading.Wall <= row.Reading.Floor {
		work = fmt.Sprintf("work=<floor (%d us)>", floorUs)
	}
	// The prefix belongs here, not in the row: a caller passing the registry's raw
	// value and a caller passing a pre-formatted one would otherwise both look
	// correct and one would print "TL TL 3".
	suffix := ""
	if label := TimeLimit(row.Limit); label != "" {
		suffix = " " + label
	}
	// cpu= is appended last and is the one addition to the row's contract: it
	// separates real work from time spent waiting to be scheduled, which wall time
	// alone cannot.
	return fmt.Sprintf("%s %s input=%dB wall=%dus %s %s%s cpu=%dus",
		row.Problem, row.Shape.String(), row.InputBytes, wallUs, work, peak(row), suffix,
		int(row.Reading.CPU.Microseconds()))
}

// peak renders the memory column. The floor is subtracted because a C++ binary's
// launch cost is tens of MiB of runtime and loader pages that have nothing to do
// with the solution's own data structures; what a solver cares about is the delta.
// Both halves stay visible so the subtraction can be checked.
//
// An unmeasured run prints `mem=n/a` rather than 0B: zero would read as "used no
// memory", which is a stronger and false claim.
func peak(row bench.Row) string {
	if !row.Reading.PeakKnown {
		return "mem=n/a"
	}
	delta := max(row.Reading.Peak-row.Reading.PeakFloor, 0)
	return fmt.Sprintf("mem=%s (peak=%s floor=%s)",
		bytes(delta), bytes(row.Reading.Peak), bytes(row.Reading.PeakFloor))
}

// bytes renders a byte count at a readable magnitude, keeping one decimal so two
// nearby rows stay distinguishable.
func bytes(n int64) string {
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

// TimeLimit renders the display-only time limit as a label, or the empty string
// when the registry declared none.
//
// Both the `--list` column and a `--bench` row call this, so the prefix is spelled
// once. A row that carried a pre-formatted label and a renderer that added the
// prefix would print "TL TL 3", and the two commands would disagree about the same
// problem.
func TimeLimit(limit string) string {
	if limit == "" {
		return ""
	}
	return "TL " + limit
}
