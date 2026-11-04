package build

// Package build compiles the three kinds of program a run needs -- a solution, a
// reference implementation and a registry-declared generator -- and remembers what
// it compiled.
//
// Nothing here knows what a problem is: the caller names a source, the flags and
// the registry that records the result. That is what lets gate, bench and gen all
// share one cache without any of them owning it.

import "time"

// SolutionFlags compile a candidate: warnings on, optimisation on, the language
// standard pinned. A solution is built with these and nothing else, so two runs
// of the same source always produce the same binary.
var SolutionFlags = []string{"-Wall", "-Wextra", "-pedantic", "--std=c++23", "-O3"}

// KernelFlags compile a reference implementation. A reference is a kernel, not a
// candidate: it is never graded on warnings and is built at -O2 because its only
// job is to produce the right bytes fast enough not to dominate a row.
var KernelFlags = []string{"-std=c++23", "-O2"}

// nopSource is the empty program whose launch and dynamic-link cost is the floor a
// timing row subtracts. Kept as source, since the harness writes it beside the
// solution it times.
const nopSource = "int main() { return 0; }\n"

// CompileTimeout bounds one compiler invocation. The per-case deadline lives in
// the exec package as exec.DefaultTimeout; a compile is separate because a
// legitimate -O3 compile outlasts a short case deadline.
const CompileTimeout = 5 * time.Minute
