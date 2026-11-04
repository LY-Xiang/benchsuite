package gate

import (
	"bytes"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/LY-Xiang/benchsuite/src/claim"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// RunCase runs one generated input through the structural, output and reference
// gates, in this order: build solution -> gate 1/2 -> generate -> gate 1/6 ->
// validate claim -> gate 6 -> locate reference -> UNSUPPORTED -> build the
// reference -> gate 1/2 -> --interface -> gate 1 on disagreement -> run the
// solution -> gate 3 or timeout -> grade or compare.
//
// Each step is one helper, called in that sequence: the first to hand back a
// *Result is the gate that failed and nothing after it runs. The boundaries are
// load-bearing: moving one earlier would let a case past a check that refused it.
// Claim validation sits before the UNSUPPORTED return, so a claim contradicting
// its bytes is not reported as an absent reference.
func (r *Runner) RunCase(p registry.Problem, spec string, seed int, timeout time.Duration) Result {
	work, err := os.MkdirTemp("", "bench-"+p.ID+"-*")
	if err != nil {
		return result(p, registry.ShapeUnset, VerdictFail, GateBuild, "cannot create a work directory: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(work) }()

	prepared, failed := r.Prepare(p, spec, seed, work)
	if failed != nil {
		return *failed
	}
	kind, shape, refBin, failed := r.resolveReference(p)
	if failed != nil {
		return *failed
	}

	sol := runProcess([]string{prepared.Solution}, prepared.Input, true, timeout)
	if sol.TimedOut {
		return result(p, shape, VerdictTimeout, GateNone, "solution exceeded the case timeout")
	}
	if sol.StdoutTruncated {
		return *clippedRefusal(p, shape, "3", "solution", sol)
	}
	if sol.Code != 0 {
		return result(p, shape, VerdictFail, GateRun, stderrDetail(sol))
	}
	if len(bytes.TrimSpace(sol.Stdout)) == 0 {
		return result(p, shape, VerdictFail, GateRun, "solution output is empty")
	}

	if kind == RefGrade {
		return r.gradeCandidate(p, shape, work, refBin, prepared.InputPath, sol, timeout)
	}
	return r.compareWithReference(p, shape, refBin, prepared.Claim, prepared.Input, sol, timeout)
}

// gradeCandidate hands the solution's output to a grading reference and takes
// its verdict: gates 4 and 7p.
func (r *Runner) gradeCandidate(p registry.Problem, shape registry.Shape, work, refBin, inputPath string,
	sol runResult, timeout time.Duration,
) Result {
	candidate := filepath.Join(work, "solution.txt")
	if err := os.WriteFile(candidate, sol.Stdout, 0o644); err != nil {
		return result(p, shape, VerdictFail, GateReference, err.Error())
	}
	grade := runProcess([]string{refBin, inputPath, candidate}, nil, true, timeout)
	if grade.TimedOut {
		return result(p, shape, VerdictTimeout, GateNone, "grader exceeded the case timeout")
	}
	if grade.StdoutTruncated {
		return *clippedRefusal(p, shape, "4", "grader", grade)
	}
	if grade.Code != 0 || len(bytes.TrimSpace(grade.Stdout)) == 0 {
		detail := stderrDetail(grade)
		if detail == "" {
			detail = "grader output is empty"
		}
		return result(p, shape, VerdictFail, GateReference, detail)
	}
	verdict := strings.TrimSpace(string(grade.Stdout))
	if verdict != "BAD=0" {
		return result(p, shape, VerdictFail, GateGrade, verdict)
	}
	return result(p, shape, VerdictPass, GateNone, "grader accepted candidate")
}

// compareWithReference runs the reference on the same input and insists the two
// agree: gates 4, 5 and 7.
func (r *Runner) compareWithReference(p registry.Problem, shape registry.Shape, refBin string, claimBytes, input []byte,
	sol runResult, timeout time.Duration,
) Result {
	ref := runProcess([]string{refBin}, input, true, timeout)
	if ref.TimedOut {
		return result(p, shape, VerdictTimeout, GateNone, "reference exceeded the case timeout")
	}
	if ref.StdoutTruncated {
		return *clippedRefusal(p, shape, "4", "reference", ref)
	}
	if ref.Code != 0 || len(bytes.TrimSpace(ref.Stdout)) == 0 {
		detail := stderrDetail(ref)
		if detail == "" {
			detail = "reference output is empty"
		}
		return result(p, shape, VerdictFail, GateReference, detail)
	}
	if p.Expect != "" {
		expected, err := claim.ExpectTokens(p.Expect, claimBytes, string(input))
		if err != nil {
			return result(p, shape, VerdictFail, GateTokens, err.Error())
		}
		if len(bytes.Fields(ref.Stdout)) != expected || len(bytes.Fields(sol.Stdout)) != expected {
			return result(p, shape, VerdictFail, GateTokens,
				fmt.Sprintf("expected %d output token(s)", expected))
		}
	}
	if !bytes.Equal(sol.Stdout, ref.Stdout) {
		return result(p, shape, VerdictFail, GateCompare, "solution bytes differ from reference")
	}
	return result(p, shape, VerdictPass, GateNone, "outputs match")
}
