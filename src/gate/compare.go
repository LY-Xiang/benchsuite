package gate

import (
	"fmt"
	"strings"
	"time"

	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// Running a child and rendering what it said.

// runResult is exec's outcome, named here so the gate helpers read without the
// package qualifier at every use.
type runResult = execpkg.RunResult

// runProcess starts a child through the one place in the harness that does.
func runProcess(argv []string, input []byte, captureStdout bool, timeout time.Duration) runResult {
	return execpkg.Run(argv, input, captureStdout, timeout)
}

// lossyString decodes process output with replacement: a stray byte in a
// diagnostic must not stop the harness reporting the gate that failed.
func lossyString(b []byte) string {
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// stderrDetail renders a failed child's stderr for a gate detail, the only way
// that stream reaches a user: the capture is capped at execpkg.MaxCapturedStderr, so a
// truncated head is marked rather than presented as the whole diagnostic.
func stderrDetail(res runResult) string {
	detail := lossyString(res.Stderr)
	if res.StderrTruncated {
		detail += fmt.Sprintf("\n[stderr truncated at %d bytes; the rest was discarded]", execpkg.MaxCapturedStderr)
	}
	return detail
}

// clippedRefusal refuses a run whose captured stdout hit execpkg.MaxCapturedStdout. A
// clip is not a shorter answer but one the harness could not hold, so the flag is
// refused rather than the prefix compared -- in one place, so no call site can
// quietly compare a prefix.
func clippedRefusal(p registry.Problem, shape registry.Shape, g Gate, stream string, res runResult) *Result {
	if !res.StdoutTruncated {
		return nil
	}
	failed := result(p, shape, VerdictFail, g,
		fmt.Sprintf("%s output exceeded %d bytes and was clipped", stream, execpkg.MaxCapturedStdout))
	return &failed
}
