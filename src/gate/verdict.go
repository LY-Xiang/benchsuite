package gate

import "github.com/LY-Xiang/benchsuite/src/registry"

// A Verdict is what happened to a case. It is a closed set rather than a word, so
// a report cannot print a spelling the harness never produces and a caller cannot
// compare against a literal that happens to be misspelt.
type Verdict uint8

const (
	// VerdictPass is a case that met every gate.
	VerdictPass Verdict = iota
	// VerdictFail is a case a gate refused.
	VerdictFail
	// VerdictTimeout is a case that ran past its deadline. It is separate from
	// VerdictFail because the fix differs: a timeout is a slow solution, not a wrong
	// one.
	VerdictTimeout
	// VerdictUnsupported is a case with no reference to compare against. It counts
	// as a pass: an absent reference is a statement about the corpus, not a failure
	// of the solution.
	VerdictUnsupported
)

// String is the report's word for the verdict.
func (v Verdict) String() string {
	switch v {
	case VerdictPass:
		return "PASS"
	case VerdictFail:
		return "FAIL"
	case VerdictTimeout:
		return "TIMEOUT"
	case VerdictUnsupported:
		return "UNSUPPORTED"
	}
	return ""
}

// Passed reports whether the verdict counts as a pass. UNSUPPORTED does: the
// harness reached a conclusion it can defend, and the corpus is what has no
// reference.
func (v Verdict) Passed() bool { return v == VerdictPass || v == VerdictUnsupported }

// A Gate names the check that refused a case. It is a string because the ids are
// the harness's published vocabulary -- `gate=7p` in a report is a name a reader
// looks up -- and a number would have to be re-spelled at every print site.
type Gate string

// The gates, in the order a case meets them. A case stops at the first one that
// refuses it.
const (
	GateNone      Gate = "-" // no gate ran: the case passed, or it never got that far
	GateBuild     Gate = "1"
	GateBinary    Gate = "2"
	GateRun       Gate = "3"
	GateReference Gate = "4"
	GateTokens    Gate = "5"
	GateClaim     Gate = "6"
	GateCompare   Gate = "7"
	GateGrade     Gate = "7p"
)

// String renders the gate for a report. An empty gate is the one that never ran,
// which prints as `-`; the width used by the row's format is the caller's business.
func (g Gate) String() string {
	if g == "" {
		return string(GateNone)
	}
	return string(g)
}

// A RefKind is what a reference implementation said it is: something to compare
// against, something to grade with, or nothing it will admit to.
type RefKind uint8

const (
	// RefNone is a reference that answered with a word the harness does not know,
	// which makes it untrustworthy rather than absent.
	RefNone RefKind = iota
	RefCompare
	RefGrade
)

// String is the reference's own word, and the empty string for RefNone.
func (k RefKind) String() string {
	switch k {
	case RefNone:
		return ""
	case RefCompare:
		return "compare"
	case RefGrade:
		return "grade"
	}
	return ""
}

// registryShape is the shape this kind of reference implies. It is the bridge
// between the reference's own vocabulary and the registry's, used to check that the
// two agree -- the comparison a registry author needs when a refs/ file is renamed.
func (k RefKind) registryShape() registry.Shape {
	switch k {
	case RefNone:
		return registry.ShapeUnset
	case RefCompare:
		return registry.ShapeCheck
	case RefGrade:
		return registry.ShapeVerify
	}
	return registry.ShapeUnset
}
