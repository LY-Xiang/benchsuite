// Package clock aggregates the wall and CPU time of repeated executions.
//
// It knows nothing about processes, memory or problems: it takes samples and
// answers one question -- which trial was fastest, and what that trial cost per
// run. Keeping it separate from the measuring code is what lets the aggregation
// rules be read and checked without a compiler or a corpus.
package clock

import "time"

// A Sample is one execution's cost.
//
// CPU is the child's own user+system time as one total. A split is not offered
// because the precise measurement Windows can give (retired cycles) does not have
// one, and a caller averaging two numbers that came from different sources would be
// reporting a precision that does not exist.
type Sample struct {
	Wall time.Duration
	CPU  time.Duration
	// CPUKnown says whether CPU is a measured value rather than the tick-quantised
	// ProcessState fallback.
	CPUKnown bool
}

// A Trial is one group of executions reduced to a per-run cost.
type Trial struct {
	Runs int
	// WallPerRun and CPUPerRun are the trial's means. The mean is what a trial
	// reports because a trial is the unit of contention: one scheduling hiccup
	// inside it is noise the next trial does not share.
	WallPerRun time.Duration
	CPUPerRun  time.Duration
	// CPUKnown is set when at least one run of this trial reported a measured CPU
	// time. A trial whose runs all fell back to ProcessState still has a number --
	// the tick-quantised one -- but it is a different claim, and a report that
	// printed it as measured would overstate what the harness knows.
	CPUKnown bool
}

// Average reduces a trial's samples to one Trial. An empty set has no answer, so
// it returns the zero Trial and false rather than dividing by zero.
func Average(samples []Sample) (Trial, bool) {
	if len(samples) == 0 {
		return Trial{}, false
	}
	var wall, cpu time.Duration
	known := false
	for _, s := range samples {
		wall += s.Wall
		cpu += s.CPU
		known = known || s.CPUKnown
	}
	n := time.Duration(len(samples))
	return Trial{
		Runs:       len(samples),
		WallPerRun: wall / n,
		CPUPerRun:  cpu / n,
		CPUKnown:   known,
	}, true
}

// Best returns the index of the trial with the smallest wall cost per run: the
// fastest average, not the mean of means, so one scheduling hiccup cannot be
// reported as the program's cost.
//
// The index, not the value, because every metric of the winner is taken from that
// same trial and a caller may hold measurements the Trial does not carry -- peak
// memory, for one. Picking the wall from one trial and the memory from another
// would be describing two runs as one. Ties keep the earliest trial, so a run's
// report does not depend on which of two equal trials happened to be scanned first.
// An empty set has no best trial, so -1 and false come back.
func Best(trials []Trial) (int, bool) {
	best := -1
	for i, trial := range trials {
		if best < 0 || trial.WallPerRun < trials[best].WallPerRun {
			best = i
		}
	}
	return best, best >= 0
}
