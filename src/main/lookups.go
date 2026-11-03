package main

// The bridge from the registry loader to the generator runtime.
//
// Loading a table is a data question, but answering it needs the generator table
// the harness will later run against. The registry states what it needs as an
// interface (registry.Lookups); this file supplies the one implementation, so the
// dependency points from the runtime into the data layer rather than back.

import (
	"lyxiang/benchsuite/src/registry"
)

// generatorLookups answers the registry's questions from the built-in kinds and
// the process-wide declared-generator table.
type generatorLookups struct{}

// BuiltinFields reports the field names a built-in kind binds.
func (generatorLookups) BuiltinFields(kind string) ([]string, bool) {
	return GeneratorFields(kind)
}

// DeclaredGenerator reports whether a registry declared this kind. Such a
// generator writes its own claim, so its fields are not knowable here and the
// caller opens the set instead of closing it.
func (generatorLookups) DeclaredGenerator(kind string) bool {
	_, ok := customGens[kind]
	return ok
}

// LoadGenerators validates one `[gen.*]` table and installs what it declares.
// Both halves belong to the generator runtime: the table's shapes become compiled
// generators, and a collision with a built-in name is only detectable there.
func (generatorLookups) LoadGenerators(contest, path string, tables map[string]any) error {
	gens, err := loadCustomGens(contest, path, tables)
	if err != nil {
		return err
	}
	return registerCustomGens(gens)
}

// installLookups wires the registry loader to this implementation. It runs once,
// before any table is read, so a load can never observe a nil table.
func installLookups() {
	registry.SetLookups(generatorLookups{})
}
