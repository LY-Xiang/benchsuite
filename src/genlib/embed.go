package genlib

import "embed"

// Source is this package's own source, embedded so a generator can be compiled
// without the harness module being reachable from the generator's directory.
//
// The harness writes these files into a scratch module as
// `lyxiang/benchsuite/src/genlib` and builds the generator against it. That keeps a
// registry entry a one-line declaration: the alternative -- requiring the tree to
// carry a go.mod with a replace directive pointing back here -- would make adding
// a generator a build-system chore.
//
//go:embed *.go
var Source embed.FS
