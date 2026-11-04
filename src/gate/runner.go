package gate

import (
	"github.com/LY-Xiang/benchsuite/src/build"
	"github.com/LY-Xiang/benchsuite/src/gen"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// A Builder compiles a candidate and answers the second gate's question about it.
//
// It is an interface rather than the concrete cache so a caller can drive the
// gates without a compiler: the two methods are the whole contract, and `build`
// satisfies it unchanged.
type Builder interface {
	Product(src, registry string, flags []string) (string, error)
	BinaryHasContent(path string) bool
}

// A Generator produces one case's input and claim.
type Generator interface {
	Generate(work, spec string, seed int) (gen.Generated, error)
}

// A Runner runs one case against a corpus root.
type Runner struct {
	builder Builder
	gen     Generator
	root    string
}

// New returns a Runner over the given compiler and generator.
func New(b Builder, g Generator, root string) *Runner {
	return &Runner{builder: b, gen: g, root: root}
}

// compileAndCheck compiles src and applies gates 1 and 2: a failed compile, then a
// binary that is missing or empty. Both are reported under the flags they were
// built with, so the caller passes them rather than reading a package constant.
func (r *Runner) compileAndCheck(p registry.Problem, src, registryPath string, flags []string) (string, *Result) {
	product, err := r.builder.Product(src, registryPath, flags)
	if err != nil {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBuild, "solution compilation failed")
		return "", &failed
	}
	if !r.builder.BinaryHasContent(product) {
		failed := result(p, registry.ShapeUnset, VerdictFail, GateBinary, "solution binary is missing or empty")
		return "", &failed
	}
	return product, nil
}

// The compiler command lines gate uses. A solution is built with the flags that
// report warnings; a reference is built as a kernel and is never graded on them.
var (
	solutionFlags = build.SolutionFlags
	kernelFlags   = build.KernelFlags
)
