// Command bench runs the CodeForces benchmark harness: it generates an input from
// a problem's generator, re-derives the claim from the bytes, runs the solution
// and the reference, and reports which gate the pair passes. Every message
// carries the `bench.py: ` prefix, which downstream greps match on.
package main

import (
	"os"
	"regexp"
	"time"

	"github.com/LY-Xiang/benchsuite/src/gen"
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// msgPrefix begins every message the CLI prints, on stdout and stderr alike.
const msgPrefix = "bench.py: "

// Exit codes: success, a case failed, or a usage/registry/report error.
const (
	exitOK    = 0
	exitFail  = 1
	exitError = 2
)

// options is one parsed command line, written once by parseArgs and read after.
type options struct {
	wantList  bool
	wantAll   bool
	wantBench bool
	shapes    []registry.Shape
	// selection holds the positional arguments, still in argv order and form.
	selection selection
	filter    *regexp.Regexp
	junit     string
	reps      int
	timeout   time.Duration
	jobs      int
}

// repoRoot is the checkout the CLI runs against: the working directory.
func repoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func main() {
	// One generator table for the whole process. It must be the one installed
	// before any table is read: a second instance would leave a declared generator
	// visible to the loader and invisible to the runner, which reads as an unknown
	// kind at run time.
	gens := gen.NewRegistry()
	registry.SetLookups(gens.Lookups())
	os.Exit(run(gens, os.Args[1:]))
}
