// Package report renders what a run found: one line per case, one line per timing
// row, and the JUnit file a CI job consumes.
//
// It reads results and writes text. It never runs anything, so every format rule --
// column widths, the `n/a` for an unmeasured run, the class name a JUnit consumer
// sees -- is stated once here instead of at the call site that happened to print it.
package report

import (
	"fmt"
	"strings"

	"github.com/LY-Xiang/benchsuite/src/gate"
)

// ResultLine is one reported case row.
//
// The gate column is padded to three so a `7p` and a `1` line up under the same
// header: a report a human scans by column breaks if the width moves.
func ResultLine(r gate.Result) string {
	// The gate column is padded to three, so `7p` and `1` line up: a report a human
	// scans by column breaks if the width moves.
	return strings.TrimRight(
		fmt.Sprintf("%-9s %s gate=%-3s %s", r.Verdict.String(), r.Problem, r.Gate.String(), r.Detail), " ",
	)
}
