package gate

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/LY-Xiang/benchsuite/src/claim"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// A Prepared is everything a case needs before the solution runs: the binary, the
// input bytes, the claim and a path to that input.
//
// It is a value rather than a bundle of returns because two callers need it -- the
// gate runner, which then compares outputs, and the timing runner, which then
// measures -- and both must refuse on exactly the same grounds. Splitting the
// refusals out is what keeps a rejected case from being timed.
type Prepared struct {
	Solution  string
	Input     []byte
	Claim     []byte
	InputPath string
}

// Prepare builds the solution and generates the input, applying gates 1, 2 and 6.
// A non-nil Result is the gate that refused; nothing after it should run.
//
// The two halves are also callable on their own. A caller that phrases its own
// refusal -- the timing runner says "did not build" for one and "produced no
// usable input" for the other -- needs to know which half refused, and reading
// that from the detail text would be guesswork. Composing them here keeps the
// gate order in one place while leaving that distinction available.
func (r *Runner) Prepare(p registry.Problem, spec string, seed int, work string) (*Prepared, *Result) {
	solution, failed := r.BuildSolution(p)
	if failed != nil {
		return nil, failed
	}
	prepared, failed := r.GenerateInput(p, work, spec, seed)
	if failed != nil {
		return nil, failed
	}
	prepared.Solution = solution
	return prepared, nil
}

// BuildSolution compiles the candidate and applies gates 1 and 2.
//
// The binary is built beside its source, so 2267/A.cpp yields 2267/A.exe, and
// the registry records the hash of what was compiled. A later run compares the
// recorded hash against the source and the flags and reuses the binary when
// they still agree.
//
// It takes no work directory: the product lives beside its source, not in a
// scratch tree, so nothing here needs one.
func (r *Runner) BuildSolution(p registry.Problem) (string, *Result) {
	src := filepath.Join(r.root, filepath.FromSlash(p.Src))
	registryPath := filepath.FromSlash(p.Registry)
	return r.compileAndCheck(p, src, registryPath, solutionFlags)
}

// GenerateInput runs the generator and writes the input next to the solution,
// refusing anything the claim validator would reject: gates 1 and 6.
func (r *Runner) GenerateInput(p registry.Problem, work, spec string, seed int) (*Prepared, *Result) {
	generated, genErr := r.gen.Generate(work, spec, seed)
	if genErr != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBuild, "generator failed: "+genErr.Error())
		return nil, &failed
	}
	if !utf8.Valid(generated.Input) {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateClaim, "input is not valid UTF-8")
		return nil, &failed
	}
	var probe any
	if err := json.Unmarshal(generated.Claim, &probe); err != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateClaim, "the claim is not JSON: "+err.Error())
		return nil, &failed
	}
	inputPath := filepath.Join(work, "input.txt")
	if err := os.WriteFile(inputPath, generated.Input, 0o644); err != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateClaim, err.Error())
		return nil, &failed
	}
	if err := claim.ValidateClaim(generated.Input, generated.Claim); err != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateClaim, err.Error())
		return nil, &failed
	}
	return &Prepared{Input: generated.Input, Claim: generated.Claim, InputPath: inputPath}, nil
}

// resolveReference finds the reference, builds it and insists that what it says
// about itself matches the registry: locate, then gates 1, 2 and 1 again.
//
// It returns both the reference's own kind and the registry's shape. They are
// different questions -- the kind is what the binary will do, the shape is what the
// registry believes -- and the caller needs the registry's when building a result.
func (r *Runner) resolveReference(p registry.Problem) (RefKind, registry.Shape, string, *Result) {
	refs := filepath.Join(r.root, p.Contest, "refs")
	refSrc := filepath.Join(refs, p.ID+".verify.cpp")
	if !registry.IsFile(refSrc) {
		if refSrc = filepath.Join(refs, p.ID+".cpp"); !registry.IsFile(refSrc) {
			failed := result(p, registry.ShapeUnset, VerdictUnsupported, GateNone, "problem has no reference")
			return RefNone, registry.ShapeUnset, "", &failed
		}
	}
	// The reference is built beside its source too, so refs/2267A.cpp yields
	// refs/2267A.exe. Its hash is recorded in the same registry table as the
	// solution's, under the reference's own path, so the two never shadow one
	// another.
	registryPath := filepath.FromSlash(p.Registry)
	refBin, err := r.builder.Product(refSrc, registryPath, kernelFlags)
	if err != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBuild, "reference compilation failed")
		return RefNone, registry.ShapeUnset, "", &failed
	}
	if !r.builder.BinaryHasContent(refBin) {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBinary, "reference binary is missing or empty")
		return RefNone, registry.ShapeUnset, "", &failed
	}

	kind := r.referenceInterface(refBin)
	if kind == RefNone {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBuild, "reference --interface failed")
		return RefNone, registry.ShapeUnset, "", &failed
	}
	// The registry's shape comes from the reference filename; the reference's own
	// declaration is authoritative at run time. A disagreement means the registry
	// lies about the shape, and running anyway would compare the wrong things.
	if (p.Shape != registry.ShapeCheck && p.Shape != registry.ShapeVerify) ||
		kind.registryShape() != p.Shape {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBuild,
			"registry says "+p.Shape.String()+", reference --interface says "+kind.String())
		return RefNone, registry.ShapeUnset, "", &failed
	}
	return kind, p.Shape, refBin, nil
}

// Timeout bounds one reference probe. Exported so a caller with its own budget can
// keep the two in step.
const probeTimeout = 5 * time.Second

func (r *Runner) referenceInterface(refBin string) RefKind {
	run := runProcess([]string{refBin, "--interface"}, nil, true, probeTimeout)
	stdout := strings.ReplaceAll(string(run.Stdout), "\r\n", "\n")
	// A probe that printed its answer then hung must not be believed: a timed-out
	// child leaves Code at zero, so TimedOut is checked too.
	if run.TimedOut || run.Code != 0 || run.StdoutTruncated {
		return RefNone
	}
	switch stdout {
	case "compare\n":
		return RefCompare
	case "grade\n":
		return RefGrade
	}
	return RefNone
}
