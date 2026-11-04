package gen

// The bridge from the registry loader to the generator runtime.
//
// Loading a table is a data question, but answering it needs the generator table
// the harness will later run against. The registry states what it needs as an
// interface (registry.Lookups); this file supplies the implementation, so the
// dependency points from the runtime into the data layer rather than back.

import (
	"github.com/LY-Xiang/benchsuite/src/registry"
)

// Lookups answers the registry's questions from the built-in kinds and the
// declared-generator table this Registry holds.
func (r *Registry) Lookups() registry.Lookups { return lookups{r} }

type lookups struct{ reg *Registry }

// BuiltinFields reports the field names a built-in kind binds.
func (lookups) BuiltinFields(kind string) ([]string, bool) {
	return GeneratorFields(kind)
}

// DeclaredGenerator reports whether a registry declared this kind. Such a
// generator writes its own claim, so its fields are not knowable here and the
// caller opens the set instead of closing it.
func (l lookups) DeclaredGenerator(kind string) bool {
	_, ok := l.reg.customGens[kind]
	return ok
}

// LoadGenerators validates one `[gen.*]` table and installs what it declares.
// Both halves belong to the generator runtime: the table's shapes become compiled
// generators, and a collision with a built-in name is only detectable there.
func (l lookups) LoadGenerators(contest, path string, tables map[string]any) error {
	gens, err := loadCustomGens(contest, path, tables)
	if err != nil {
		return err
	}
	return l.reg.registerCustomGens(gens)
}
