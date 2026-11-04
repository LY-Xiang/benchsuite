package build

import (
	"slices"
	"strings"
)

// A Note is one build that succeeded with diagnostics on stderr. It is data, not
// text: the caller decides how to render it and where to print it, so this package
// never writes to a report channel it does not own.
type Note struct {
	Src   string
	Lines []string
}

// DrainNotes returns the notes collected so far and forgets them.
//
// The order is by source path: a parallel sweep appends them in whatever order the
// compilers finished, and an author reading the report wants the same order on
// every run.
func (c *Cache) DrainNotes() []Note {
	c.notesMu.Lock()
	notes := c.notes
	c.notes = nil
	c.notesMu.Unlock()

	slices.SortFunc(notes, func(a, b Note) int { return strings.Compare(a.Src, b.Src) })
	return notes
}
