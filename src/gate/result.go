// Package gate runs one case through the harness's structural, output and
// reference checks, and reports the first gate that refused it.
//
// A gate is not an assertion library: each one corresponds to a failure a solver
// can act on (it did not compile, it exited non-zero, it disagreed with the
// reference), and the id is the part of that story a report carries. Keeping the
// ids and their order here is what lets a reader compare two runs.
package gate

import "github.com/LY-Xiang/benchsuite/src/registry"

// A Result is one case's outcome.
type Result struct {
	Problem string
	// Shape is the registry's own value, or ShapeUnset when the case failed before
	// a reference was found. Zero is the honest answer there: nothing determined a
	// shape, and a report that named one would be inventing it.
	Shape   registry.Shape
	Verdict Verdict
	Gate    Gate
	Detail  string
}

// result builds one outcome. Shape is a parameter because the reference's own word
// and the registry's word are checked against each other: a disagreement is a gate
// failure whose report names both.
func result(p registry.Problem, shape registry.Shape, v Verdict, g Gate, detail string) Result {
	return Result{Problem: p.ID, Shape: shape, Verdict: v, Gate: g, Detail: detail}
}
