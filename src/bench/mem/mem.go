// Package mem reads a child's memory and CPU cost from the handle exec hands it.
//
// Two observations live here because they answer two questions and can fail
// independently: a platform may report a peak resident set and no CPU time, and a
// measurement may fail for one while succeeding for the other. Keeping them as
// separate observers is what lets a raise report say which fact is missing instead
// of refusing both.
package mem

import (
	"time"

	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
)

// Peak is a child's peak resident set in bytes.
type Peak int64

// CPU is a child's own user+system time.
//
// It comes from the OS's per-process accounting rather than from cycles divided by
// a clock: a core's frequency moves during a run, so a cycle count converted at
// the frequency observed at exit is not a duration. The accounting is quantised to
// the scheduler's tick, which costs accuracy only for runs shorter than that tick.
type CPU time.Duration

// The observers this package exports. bench.Measure takes them as a list and
// unpacks each reading by position, so a caller must pass them in an order it can
// describe back to itself; Observers is that order, spelled once.
//
// ObservePeak reads the peak resident set. It reports absence rather than zero
// when the platform cannot answer: zero would claim the child used no memory,
// which is a stronger and false statement.
func ObservePeak(proc execpkg.Process) (any, bool) {
	value, ok := peakOf(proc)
	return Peak(value), ok
}

// ObserveCPU reads the child's own CPU time.
func ObserveCPU(proc execpkg.Process) (any, bool) {
	value, ok := cpuOf(proc)
	return CPU(value), ok
}

// Observers is every observation this package offers, in a fixed order. A caller
// that passes this slice gets both; one that passes a prefix gets those in the
// same order, which is what keeps the readings aligned.
func Observers() []execpkg.Observer {
	return []execpkg.Observer{ObservePeak, ObserveCPU}
}
