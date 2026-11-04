package gen

// Registry-declared generators.
//
// A contest may declare a generator of its own in its bench.toml:
//
//	[gen.tree2]
//	src = "gen/tree2.go"
//
// and then name it in a spec like any built-in kind: `tree2 %S 100 5`.
//
// The source is a Go program that imports github.com/LY-Xiang/benchsuite/src/genlib and calls
// genlib.Main with the function that writes its input and returns its claim. That
// is the same vocabulary a built-in kind is written in, so the two differ only in
// where the function came from -- never in the ceiling, the claim grammar or the
// field set. The source is compiled once per content digest through the cache the
// solution build uses.
//
// This is the one place a registry can name code the harness then executes, so the
// path is held to the same confinement as a solution src: it must stay inside the
// repository. There is no separate `lang` key: a generator is Go, because that is
// what the typed vocabulary is written in.

import (
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/LY-Xiang/benchsuite/src/build"
	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
	"github.com/LY-Xiang/benchsuite/src/registry"
	"github.com/LY-Xiang/benchsuite/src/text"
)

// customGen is one registry-declared generator.
type customGen struct {
	Name string // the kind name a spec names
	Src  string // repo-root relative, confined by CheckSrcConfined
	// Contest is the directory of the registry that declared it, so a load error
	// can name the table that caused it.
	Contest string
	// Registry is the bench.toml that declared this generator. It is where the
	// hash of the compiled generator is recorded.
	Registry string
}

// loadCustomGens reads the `[gen.*]` tables of one registry.
func loadCustomGens(contest, path string, tables map[string]any) ([]customGen, error) {
	var gens []customGen
	for _, name := range slices.Sorted(maps.Keys(tables)) {
		values, ok := tables[name].(map[string]any)
		if !ok {
			return nil, registry.LoadErrorf("%s: gen.%s must be a table", filepath.ToSlash(path), name)
		}
		gen := customGen{Name: name, Contest: contest, Registry: filepath.ToSlash(path)}

		src, _ := values["src"].(string)
		if strings.TrimSpace(src) == "" {
			return nil, registry.LoadErrorf("%s: gen.%s is missing the required key \"src\"",
				filepath.ToSlash(path), name)
		}
		if err := registry.CheckSrcConfined(src); err != nil {
			return nil, registry.LoadErrorf("%s: gen.%s: %v", filepath.ToSlash(path), name, err)
		}
		gen.Src = src

		for key := range values {
			if key != "src" {
				return nil, registry.LoadErrorf("%s: gen.%s has unknown key %s (a generator "+
					"declares its own shape in Go, so only src is read here)",
					filepath.ToSlash(path), name, text.Text(text.Quote(key)))
			}
		}
		gens = append(gens, gen)
	}
	return gens, nil
}

// registerCustomGens installs the generators one registry declares. A name already
// taken by a built-in kind, or by another contest's generator at a different path,
// is refused: either would make one spec word mean two things.
func (r *Registry) registerCustomGens(gens []customGen) error {
	for _, gen := range gens {
		if _, isBuiltin := builtinKind(gen.Name); isBuiltin {
			return registry.LoadErrorf("gen.%s collides with the built-in kind of the same name", gen.Name)
		}
		if prior, taken := r.customGens[gen.Name]; taken && prior.Src != gen.Src {
			return registry.LoadErrorf("gen.%s is declared twice: %s and %s",
				gen.Name, text.Text(prior.Src), text.Text(gen.Src))
		}
		r.customGens[gen.Name] = gen
	}
	return nil
}

// runCustomGen builds the generator and runs it once for this case. The program
// owns nothing but its emit function: the harness passes the seed and the
// parameters, and takes back the input on stdout and the claim from the file it
// named, exactly as for a built-in kind.
func (r *Registry) runCustomGen(c *build.Cache, work, name string, seed int, rawParams []string) (Generated, error) {
	gen, ok := r.customGens[name]
	if !ok {
		return Generated{}, fmt.Errorf("unknown generator kind %q", name)
	}
	if c == nil {
		// The pure entry point has no cache or work directory to compile into, so a
		// registry-declared generator is only reachable through a runner.
		return Generated{}, fmt.Errorf(
			"generator %s is declared by a registry and needs the runner's build cache", name)
	}
	prog, err := buildCustomGen(c, gen, filepath.FromSlash(gen.Registry))
	if err != nil {
		return Generated{}, err
	}

	claimPath := filepath.Join(work, "customgen-claim.json")
	argv := make([]string, 0, len(rawParams)+3)
	argv = append(argv, prog, strconv.Itoa(seed), claimPath)
	argv = append(argv, rawParams...)

	result := execpkg.Run(argv, nil, true, build.CompileTimeout)
	if result.TimedOut {
		return Generated{}, fmt.Errorf("generator %s timed out", name)
	}
	if result.Code != 0 {
		return Generated{}, fmt.Errorf("generator %s exited %d: %s",
			name, result.Code, strings.TrimSpace(string(result.Stderr)))
	}
	claim, err := os.ReadFile(claimPath)
	if err != nil {
		return Generated{}, fmt.Errorf("generator %s wrote no claim at its argument path", name)
	}
	return Generated{Input: result.Stdout, Claim: claim}, nil
}

// buildCustomGen compiles a generated program beside its source, so a generator
// at 99001/gen.go yields 99001/gen.exe, with its hash recorded in the registry.
func buildCustomGen(c *build.Cache, custom customGen, registryPath string) (string, error) {
	if err := registry.CheckSrcConfined(custom.Src); err != nil {
		return "", err
	}
	source := filepath.Join(c.Root, filepath.FromSlash(custom.Src))
	if info, err := os.Stat(source); err != nil || info.IsDir() {
		return "", fmt.Errorf("generator source %s is missing", text.Text(custom.Src))
	}
	out, err := c.GoProduct(source, registryPath)
	if err != nil {
		return "", fmt.Errorf("generator source %s did not compile", text.Text(custom.Src))
	}
	return out, nil
}
