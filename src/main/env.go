package main

import (
	"fmt"
	"os"

	"github.com/LY-Xiang/benchsuite/src/build"
	"github.com/LY-Xiang/benchsuite/src/gate"
	"github.com/LY-Xiang/benchsuite/src/gen"
)

// An Env is the CLI's view of the build cache: the cache itself, embedded so a
// call site reads the corpus root or the compiler command straight off it.
type Env struct {
	*build.Cache

	// gens is the run's generator table. It is the same instance the registry
	// loader was wired to, never a fresh one: see main().
	gens *gen.Registry

	// gates runs cases. It is built over this Env's cache and generator, so a case
	// and a timing row compile through the same cache and generate through the same
	// table.
	gates *gate.Runner
}

func NewEnv(root string, gens *gen.Registry) *Env {
	logf := func(format string, args ...any) { _, _ = fmt.Fprintf(os.Stderr, format, args...) }
	cache := build.New(root, logf)
	env := &Env{Cache: cache, gens: gens}
	env.gates = gate.New(builderAdapter{cache}, generatorAdapter{cache, gens}, root)
	return env
}

// builderAdapter makes *build.Cache satisfy gate.Builder. The compiler's two
// methods are the whole contract, so the adapter is only the name.
type builderAdapter struct{ c *build.Cache }

func (b builderAdapter) Product(src, registry string, flags []string) (string, error) {
	return b.c.Product(src, registry, flags)
}

func (builderAdapter) BinaryHasContent(path string) bool { return build.BinaryHasContent(path) }

// generatorAdapter makes the run's generator table satisfy gate.Generator. It
// carries the cache because a registry-declared generator must be compiled before
// it can run, and the cache is what compiles it.
type generatorAdapter struct {
	c *build.Cache
	r *gen.Registry
}

func (g generatorAdapter) Generate(work, spec string, seed int) (gen.Generated, error) {
	return g.r.Generate(g.c, work, spec, seed)
}
