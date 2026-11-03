package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"lyxiang/benchsuite/src/claim"
	execpkg "lyxiang/benchsuite/src/exec"
	"lyxiang/benchsuite/src/registry"
)

var (
	solutionFlags = []string{"-Wall", "-Wextra", "-pedantic", "--std=c++23", "-O3"}
	kernelFlags   = []string{"-std=c++23", "-O2"}
)

// A Result is one case's outcome. Shape is the reference's own word (compare,
// grade, none), since that is what the run used; the registry's word is checked
// against it before anything runs.
type Result struct {
	Problem string
	Shape   string // "", "none", "compare", "grade"
	Kind    string // "PASS", "FAIL", "UNSUPPORTED", "TIMEOUT"
	GateID  string
	Detail  string
}

func result(p registry.Problem, shape, kind, gateID, detail string) Result {
	return Result{Problem: p.ID, Shape: shape, Kind: kind, GateID: gateID, Detail: detail}
}

// lossyString decodes process output with replacement: a stray byte in a
// diagnostic must not stop the harness reporting the gate that failed.
func lossyString(b []byte) string {
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// stderrDetail renders a failed child's stderr for a gate detail, the only way
// that stream reaches a user: the capture is capped at execpkg.MaxCapturedStderr, so a
// truncated head is marked rather than presented as the whole diagnostic.
func stderrDetail(res execpkg.RunResult) string {
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
func clippedRefusal(p registry.Problem, shape, gate, stream string, res execpkg.RunResult) *Result {
	if !res.StdoutTruncated {
		return nil
	}
	failed := result(p, shape, "FAIL", gate,
		fmt.Sprintf("%s output exceeded %d bytes and was clipped", stream, execpkg.MaxCapturedStdout))
	return &failed
}

// RunCase runs one generated input through the structural, output and reference
// gates, in this order: build solution -> gate 1/2 -> generate -> gate 1/6 ->
// validate claim -> gate 6 -> locate reference -> UNSUPPORTED -> build the
// reference -> gate 1/2 -> --interface -> gate 1 on disagreement -> run the
// solution -> gate 3 or timeout -> grade or compare.
//
// Each step is one helper below, called in that sequence: the first to hand back a
// *Result is the gate that failed and nothing after it runs. The boundaries are
// load-bearing: moving one earlier would let a case past a check that refused it.
// Claim validation sits before the UNSUPPORTED return, so a claim contradicting
// its bytes is not reported as an absent reference.
func (e *Env) RunCase(p registry.Problem, spec string, seed int, timeout time.Duration) Result {
	work, err := os.MkdirTemp("", "bench-"+p.ID+"-*")
	if err != nil {
		return result(p, "", "FAIL", "1", "cannot create a work directory: "+err.Error())
	}
	defer func() { _ = os.RemoveAll(work) }()

	solution, failed := e.buildSolution(work, p)
	if failed != nil {
		return *failed
	}
	input, claimBytes, inputPath, failed := generateInput(e, p, work, spec, seed)
	if failed != nil {
		return *failed
	}
	shape, refBin, failed := e.resolveReference(work, p)
	if failed != nil {
		return *failed
	}

	sol := execpkg.Run([]string{solution}, input, true, timeout)
	if sol.TimedOut {
		return result(p, shape, "TIMEOUT", "", "solution exceeded the case timeout")
	}
	if sol.StdoutTruncated {
		return *clippedRefusal(p, shape, "3", "solution", sol)
	}
	if sol.Code != 0 {
		return result(p, shape, "FAIL", "3", stderrDetail(sol))
	}
	if len(bytes.TrimSpace(sol.Stdout)) == 0 {
		return result(p, shape, "FAIL", "3", "solution output is empty")
	}

	if shape == "grade" {
		return e.gradeCandidate(p, shape, work, refBin, inputPath, sol, timeout)
	}
	return e.compareWithReference(p, shape, refBin, claimBytes, input, sol, timeout)
}

// buildSolution compiles the candidate: gates 1 and 2.
//
// The binary is built beside its source, so 2267/A.cpp yields 2267/A.exe, and
// the registry records the hash of what was compiled. A later run compares the
// recorded hash against the source and the flags and reuses the binary when
// they still agree.
func (e *Env) buildSolution(work string, p registry.Problem) (string, *Result) {
	src := filepath.Join(e.Root, filepath.FromSlash(p.Src))
	registryPath := filepath.FromSlash(p.Registry)
	solution, err := e.buildProduct(src, registryPath, solutionFlags)
	if err != nil {
		failed := result(p, "", "FAIL", "1", "solution compilation failed")
		return "", &failed
	}
	if !fileHasContent(solution) {
		failed := result(p, "", "FAIL", "2", "solution binary is missing or empty")
		return "", &failed
	}
	return solution, nil
}

// generateInput runs the generator and writes the input next to the solution,
// refusing anything the claim validator would reject: gates 1 and 6.
func generateInput(env *Env, p registry.Problem, work, spec string, seed int) ([]byte, []byte, string, *Result) {
	generated, genErr := GenerateIn(env, work, spec, seed)
	if genErr != nil {
		failed := result(p, "", "FAIL", "1", "generator failed: "+genErr.Error())
		return nil, nil, "", &failed
	}
	if !utf8.Valid(generated.Input) {
		failed := result(p, "", "FAIL", "6", "input is not valid UTF-8")
		return nil, nil, "", &failed
	}
	var probe any
	if err := json.Unmarshal(generated.Claim, &probe); err != nil {
		failed := result(p, "", "FAIL", "6", "the claim is not JSON: "+err.Error())
		return nil, nil, "", &failed
	}
	inputPath := filepath.Join(work, "input.txt")
	if err := os.WriteFile(inputPath, generated.Input, 0o644); err != nil {
		failed := result(p, "", "FAIL", "6", err.Error())
		return nil, nil, "", &failed
	}
	if err := claim.ValidateClaim(generated.Input, generated.Claim); err != nil {
		failed := result(p, "", "FAIL", "6", err.Error())
		return nil, nil, "", &failed
	}
	return generated.Input, generated.Claim, inputPath, nil
}

// resolveReference finds the reference, builds it and insists that what it says
// about itself matches the registry: locate, then gates 1, 2 and 1 again.
func (e *Env) resolveReference(work string, p registry.Problem) (string, string, *Result) {
	refs := filepath.Join(e.Root, p.Contest, "refs")
	refSrc := filepath.Join(refs, p.ID+".verify.cpp")
	if !registry.IsFile(refSrc) {
		if refSrc = filepath.Join(refs, p.ID+".cpp"); !registry.IsFile(refSrc) {
			failed := result(p, "none", "UNSUPPORTED", "", "problem has no reference")
			return "", "", &failed
		}
	}
	// The reference is built beside its source too, so refs/2267A.cpp yields
	// refs/2267A.exe. Its hash is recorded in the same registry table as the
	// solution's, under the reference's own path, so the two never shadow one
	// another.
	registryPath := filepath.FromSlash(p.Registry)
	refBin, err := e.buildProduct(refSrc, registryPath, kernelFlags)
	if err != nil {
		failed := result(p, "", "FAIL", "1", "reference compilation failed")
		return "", "", &failed
	}
	if !fileHasContent(refBin) {
		failed := result(p, "", "FAIL", "2", "reference binary is missing or empty")
		return "", "", &failed
	}

	shape := e.referenceInterface(refBin)
	if shape == "" {
		failed := result(p, "", "FAIL", "1", "reference --interface failed")
		return "", "", &failed
	}
	// The registry's shape comes from the reference filename; the reference's own
	// declaration is authoritative at run time. A disagreement means the registry
	// lies about the shape, and running anyway would compare the wrong things.
	if (p.Shape != "check" && p.Shape != "verify") || p.Shape != shapeClass(shape) {
		failed := result(p, shape, "FAIL", "1",
			"registry says "+p.Shape+", reference --interface says "+shape)
		return "", "", &failed
	}
	return shape, refBin, nil
}

// gradeCandidate hands the solution's output to a grading reference and takes
// its verdict: gates 4 and 7p.
func (e *Env) gradeCandidate(p registry.Problem, shape, work, refBin, inputPath string,
	sol execpkg.RunResult, timeout time.Duration) Result {
	candidate := filepath.Join(work, "solution.txt")
	if err := os.WriteFile(candidate, sol.Stdout, 0o644); err != nil {
		return result(p, shape, "FAIL", "4", err.Error())
	}
	grade := execpkg.Run([]string{refBin, inputPath, candidate}, nil, true, timeout)
	if grade.TimedOut {
		return result(p, shape, "TIMEOUT", "", "grader exceeded the case timeout")
	}
	if grade.StdoutTruncated {
		return *clippedRefusal(p, shape, "4", "grader", grade)
	}
	if grade.Code != 0 || len(bytes.TrimSpace(grade.Stdout)) == 0 {
		detail := stderrDetail(grade)
		if detail == "" {
			detail = "grader output is empty"
		}
		return result(p, shape, "FAIL", "4", detail)
	}
	verdict := strings.TrimSpace(string(grade.Stdout))
	if verdict != "BAD=0" {
		return result(p, shape, "FAIL", "7p", verdict)
	}
	return result(p, shape, "PASS", "", "grader accepted candidate")
}

// compareWithReference runs the reference on the same input and insists the two
// agree: gates 4, 5 and 7.
func (e *Env) compareWithReference(p registry.Problem, shape, refBin string, claimBytes, input []byte,
	sol execpkg.RunResult, timeout time.Duration) Result {
	ref := execpkg.Run([]string{refBin}, input, true, timeout)
	if ref.TimedOut {
		return result(p, shape, "TIMEOUT", "", "reference exceeded the case timeout")
	}
	if ref.StdoutTruncated {
		return *clippedRefusal(p, shape, "4", "reference", ref)
	}
	if ref.Code != 0 || len(bytes.TrimSpace(ref.Stdout)) == 0 {
		detail := stderrDetail(ref)
		if detail == "" {
			detail = "reference output is empty"
		}
		return result(p, shape, "FAIL", "4", detail)
	}
	if p.Expect != "" {
		expected, err := claim.ExpectTokens(p.Expect, claimBytes, string(input))
		if err != nil {
			return result(p, shape, "FAIL", "5", err.Error())
		}
		if len(bytes.Fields(ref.Stdout)) != expected || len(bytes.Fields(sol.Stdout)) != expected {
			return result(p, shape, "FAIL", "5",
				fmt.Sprintf("expected %d output token(s)", expected))
		}
	}
	if !bytes.Equal(sol.Stdout, ref.Stdout) {
		return result(p, shape, "FAIL", "7", "solution bytes differ from reference")
	}
	return result(p, shape, "PASS", "", "outputs match")
}

// referenceInterface asks the reference what it is: `compare` or `grade`; anything
// else is a build that cannot be trusted to grade anything.
func (e *Env) referenceInterface(refBin string) string {
	run := execpkg.Run([]string{refBin, "--interface"}, nil, true, 0)
	stdout := strings.ReplaceAll(string(run.Stdout), "\r\n", "\n")
	// A probe that printed its answer then hung must not be believed: a timed-out
	// child leaves Code at zero, so TimedOut is checked too.
	if run.TimedOut || run.Code != 0 || run.StdoutTruncated {
		return ""
	}
	switch stdout {
	case "compare\n":
		return "compare"
	case "grade\n":
		return "grade"
	}
	return ""
}
