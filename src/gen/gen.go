// Package gen turns a generator spec into an input and its claim.
//
// A spec is `<kind> %S <params...>`: the seed replaces %S before the kind sees its
// parameters. A spec that cannot be drawn yields an error and no bytes at all.
//
// The kinds themselves live in package genlib, so a built-in generator and one
// declared by a registry are the same shape and run through the same code. This
// package is the adapter: it parses a spec's words and hands them to genlib.
package gen

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"github.com/LY-Xiang/benchsuite/src/build"
	"github.com/LY-Xiang/benchsuite/src/genlib"
)

// Generated is one run's input and its claim.
type Generated struct {
	Input []byte
	Claim []byte
}

// A Registry is the generator table one run uses: the built-in kinds genlib ships,
// plus whatever contests declared in their own `[gen.*]` tables.
//
// It is a struct rather than package state because the declared table is built
// while registries load and consulted for every case afterwards. Two instances
// would mean two tables, and a generator declared into one would read as an
// unknown kind from the other -- the failure that looks like a typo in the
// registry.
type Registry struct {
	customGens map[string]customGen
}

// NewRegistry returns an empty table. Built-in kinds are always available; the
// declared ones arrive as registries load.
func NewRegistry() *Registry {
	return &Registry{customGens: map[string]customGen{}}
}

// Generate produces one case's input and claim, resolving declared generators
// through the table and compiling them through c.
func (r *Registry) Generate(c *build.Cache, work, spec string, seed int) (Generated, error) {
	return r.generate(c, work, spec, seed)
}

// generate is the shared body: it splits a composed spec, generates each part and
// joins the results. Any failure yields an error and no bytes, so a bad spec never
// leaves a partial input.
func (r *Registry) generate(c *build.Cache, work, spec string, seed int) (Generated, error) {
	parts := strings.Split(spec, " + ")
	inputs := make([][]byte, 0, len(parts))
	claims := make([][]byte, 0, len(parts))
	for _, part := range parts {
		one, err := r.generatePart(c, work, part, seed)
		if err != nil {
			return Generated{}, err
		}
		inputs = append(inputs, one.Input)
		claims = append(claims, one.Claim)
	}
	if len(parts) == 1 {
		return Generated{Input: inputs[0], Claim: claims[0]}, nil
	}
	claim, err := joinClaims(claims)
	if err != nil {
		return Generated{}, err
	}
	return Generated{Input: bytes.Join(inputs, nil), Claim: claim}, nil
}

// joinClaims merges the node lists of several part claims into one. Parts are
// concatenated in the same order as the inputs, so the merged claim walks the
// joined input exactly as the parts walked their own.
func joinClaims(claims [][]byte) ([]byte, error) {
	var nodes []json.RawMessage
	for i, claim := range claims {
		var top map[string]json.RawMessage
		if err := json.Unmarshal(claim, &top); err != nil {
			return nil, fmt.Errorf("part %d claim is not an object: %w", i+1, err)
		}
		var part []json.RawMessage
		if err := json.Unmarshal(top["lines"], &part); err != nil {
			return nil, fmt.Errorf("part %d claim has no node list: %w", i+1, err)
		}
		nodes = append(nodes, part...)
	}
	merged, err := json.Marshal(map[string]any{"lines": nodes})
	if err != nil {
		return nil, err
	}
	return append(merged, '\n'), nil
}

// generatePart runs one kind spec and returns its input and claim.
func (r *Registry) generatePart(c *build.Cache, work, spec string, seed int) (Generated, error) {
	// Exactly one %S carries the seed: with none every seed would emit the same
	// bytes, with two a substitution shifts every parameter.
	switch strings.Count(spec, "%S") {
	case 0:
		return Generated{}, fmt.Errorf("generator spec %q has no %%S placeholder for the seed", spec)
	case 1:
	default:
		return Generated{}, fmt.Errorf("generator spec %q has more than one %%S placeholder", spec)
	}
	words := strings.Fields(strings.ReplaceAll(spec, "%S", strconv.Itoa(seed)))
	if len(words) < 2 {
		return Generated{}, errors.New("generator spec is missing a seed")
	}
	name := words[0]
	rawParams := words[2:]

	spec2, ok := builtinKind(name)
	if !ok {
		if _, isCustom := r.customGens[name]; isCustom {
			return r.runCustomGen(c, work, name, seed, rawParams)
		}
		return Generated{}, fmt.Errorf("unknown generator kind %q", name)
	}
	params := make([]int, len(rawParams))
	for i, raw := range rawParams {
		value, err := strconv.Atoi(raw)
		if err != nil {
			// `letters` takes one textual argument, `pan`, a flag rather than a
			// number. Every numeric parameter stays strict.
			if name == "letters" && i == 1 && raw == "pan" {
				continue
			}
			return Generated{}, fmt.Errorf(
				"generator %s parameter %d is not a number: %q", name, i+1, raw)
		}
		params[i] = value
	}
	input, claim, err := genlib.Run(spec2, seed, params, rawParams)
	if err != nil {
		return Generated{}, fmt.Errorf("generator %s: %w", name, err)
	}
	return Generated{Input: input, Claim: claim}, nil
}

// GeneratorFields reports the field names a kind binds, for `expect` validation.
//
// A registry-declared generator reports no fields and no kind: its claim is written
// by the generator itself, so the field set is not knowable here. The caller treats
// an unknown kind that is a declared generator as opening the set, which is the
// price of letting a registry define one; a wrong `expect` still fails at run time
// against the claim gate.
func GeneratorFields(name string) ([]string, bool) {
	spec, ok := builtinKind(name)
	if !ok {
		return nil, false
	}
	return slices.Clone(spec.Fields), true
}

// builtinKind resolves one of the generators genlib ships.
func builtinKind(name string) (genlib.Spec, bool) {
	for _, spec := range genlib.Kinds() {
		if spec.Name == name {
			return spec, true
		}
	}
	return genlib.Spec{}, false
}
