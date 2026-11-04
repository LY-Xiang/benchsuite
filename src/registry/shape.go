package registry

import "slices"

// A Shape is what a problem's reference lets the harness do with it: compare the
// solution against the reference's output, hand both to a grading reference, or
// only time it.
//
// It is a closed set with an explicit unset member. Zero is ShapeUnset because the
// registry derives a shape from which reference file exists, and a problem with no
// reference has none -- a zero value that meant "check" would silently grant a
// comparison that cannot happen.
type Shape uint8

const (
	// ShapeUnset is a problem whose reference has not determined a shape yet.
	ShapeUnset Shape = iota
	ShapeCheck
	ShapeVerify
	ShapeBench
)

// String is the registry's word for the shape, and the empty string for ShapeUnset.
//
// It is the word a registry declares and a report prints, so a value that was never
// set must not acquire one: `--list` shows an empty shape column for a problem with
// no reference today, and that is the honest rendering.
func (s Shape) String() string {
	// ShapeUnset has its own case rather than falling to the default: it is a real
	// member with a real rendering (the empty column), not an unreachable leftover.
	switch s {
	case ShapeUnset:
		return ""
	case ShapeCheck:
		return "check"
	case ShapeVerify:
		return "verify"
	case ShapeBench:
		return "bench"
	}
	return ""
}

// ClassName is the JUnit class name for the shape. It differs from String in
// exactly one case: an unset shape reports "bench", because a case that failed
// before a reference was found still belongs to some suite, and "bench" is the
// harness's own name for itself.
//
// The two are separate methods rather than one with a flag: they answer different
// questions, and folding them together would make a report's class name leak into
// `--list`.
func (s Shape) ClassName() string {
	if name := s.String(); name != "" {
		return name
	}
	return "bench"
}

// shapes is the closed set in declaration order, which is the order the key tables
// below are built from.
var shapes = []Shape{ShapeCheck, ShapeVerify, ShapeBench}

// AllowedKeys is the key set this shape may declare. `check` is the full
// vocabulary: a key outside every shape is unknown, one belonging only to another
// shape is forbidden for this one.
func (s Shape) AllowedKeys() []string {
	switch s {
	case ShapeUnset:
		return nil
	case ShapeCheck:
		return []string{"src", "gen", "maxgen", "expect", "stale", "maxchk"}
	case ShapeVerify:
		return []string{"src", "gen", "maxgen", "stale"}
	case ShapeBench:
		return []string{"src", "maxgen"}
	}
	return nil
}

// RequiredKeys is the key set this shape must declare. `maxgen` is absent from
// every entry: it is optional now that an omitted one inherits `gen`, and a problem
// that never runs at its ceiling should not have to invent one.
func (s Shape) RequiredKeys() []string {
	switch s {
	case ShapeUnset:
		return nil
	case ShapeCheck:
		return []string{"gen", "expect"}
	case ShapeVerify:
		return []string{"gen"}
	case ShapeBench:
		return []string{"maxgen"}
	}
	return nil
}

// ShapeAllKeys is every key any shape may declare, so a key outside it is unknown
// rather than forbidden for this shape. It is the union of the closed set rather
// than a hand-written list: a shape that gains a key cannot be forgotten here.
func ShapeAllKeys() []string {
	var keys []string
	for _, shape := range shapes {
		keys = append(keys, shape.AllowedKeys()...)
	}
	return slices.Compact(slices.Sorted(slices.Values(keys)))
}

// ParseShape reads a registry's word for a shape. The bool is false for anything
// outside the closed set, which is what makes `-L typo` a usage error rather than a
// filter that selects nothing.
func ParseShape(s string) (Shape, bool) {
	switch s {
	case "check":
		return ShapeCheck, true
	case "verify":
		return ShapeVerify, true
	case "bench":
		return ShapeBench, true
	}
	return ShapeUnset, false
}
