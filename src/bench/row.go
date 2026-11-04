// Package bench times a solution against the empty-program launch floor.
//
// A timing row is two numbers about one machine at one moment: what the solution
// cost, and what a bare process launch costs on the same input. The second is
// subtracted, because a C++ binary pays tens of milliseconds of loader and runtime
// start-up that have nothing to do with the problem. Both halves stay in the row so
// the subtraction can be checked.
//
// This package knows nothing about problems, gates or output formats: it is handed
// an argv, an input and a floor binary, and it returns a Reading. Where that row is
// printed, and what a problem id looks like, is somebody else's business.
package bench

import (
	"time"

	"github.com/LY-Xiang/benchsuite/src/bench/clock"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// A Row is one timing measurement prepared for reporting. Its fields are what a
// reader sees; Reading is what was measured.
type Row struct {
	Problem string
	// Shape is the registry's own value, not its word: the renderer asks it for the
	// word, so a row cannot carry a spelling the closed set does not contain.
	Shape      registry.Shape
	InputBytes int
	// Limit is the problem's declared time limit, display only: the driver never
	// enforces it. An empty string means the registry declared none.
	Limit   string
	Reading Reading
}

// A Reading is one problem's measurement: its own cost, and the launch floor's,
// taken on the same input in the same session.
type Reading struct {
	// Wall is the solution's wall time per run, Floor is the empty program's, and
	// Work is what is left after the floor is taken out. CPU is the solution's own
	// user+system time per run, which separates real work from time spent waiting to
	// be scheduled.
	//
	// Floor is kept beside Work because Work is floored at zero: a reader that saw
	// only a zero could not tell a genuine zero from a solution that came in under
	// the empty program.
	Wall, Floor, Work, CPU time.Duration
	// Peak is the solution's peak resident set and PeakFloor the empty program's.
	// They are only meaningful when PeakKnown and FloorKnown are set: a caller that
	// printed the raw numbers would report an unmeasured run as "used no memory".
	Peak, PeakFloor int64
	// The flags are grouped at the tail rather than beside the values they qualify:
	// each bool declared between two words would pad to a word of its own, which is
	// 8 wasted bytes here. CPUKnown says whether CPU is a measurement or the
	// tick-quantised fallback; the renderer prints the column either way.
	CPUKnown, PeakKnown, FloorKnown bool
}

// A Campaign is the trials one program ran, and its best.
type Campaign struct {
	Trials []clock.Trial
	// Peak is the largest resident set observed in the winning trial, and PeakKnown
	// says whether any run of that trial reported one. Both numbers of a row then
	// describe one execution: taking the campaign-wide maximum would let a spike in
	// a slower trial be reported beside a wall time that never saw it.
	Peak      int64
	PeakKnown bool
	// Best is the fastest trial, and the source of every time in the Reading.
	Best clock.Trial
}

// Against reduces a solution's campaign and the floor's to one Reading.
//
// Wall and CPU come from the solution's best trial and the floor from its own, so
// each number describes the run it was measured in. Work floors at zero: a hiccup
// on the floor alone can put the solution "faster" than an empty program, and a
// negative duration would be a claim the measurement cannot support.
func (c Campaign) Against(floor Campaign) Reading {
	wall := c.Best.WallPerRun
	work := max(wall-floor.Best.WallPerRun, 0)
	return Reading{
		Wall:       wall,
		Floor:      floor.Best.WallPerRun,
		Work:       work,
		CPU:        c.Best.CPUPerRun,
		CPUKnown:   c.Best.CPUKnown,
		Peak:       c.Peak,
		PeakFloor:  floor.Peak,
		PeakKnown:  c.PeakKnown,
		FloorKnown: floor.PeakKnown,
	}
}
