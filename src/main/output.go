package main

import (
	"bufio"
	"fmt"
	"os"

	"github.com/LY-Xiang/benchsuite/src/text"
)

// stdout buffers the report. A run prints one line per case, so an unbuffered
// write per line is a syscall per case on a sweep of hundreds.
var stdout = bufio.NewWriter(os.Stdout)

// flushStdout pushes the buffered report out. It is idempotent, and fail calls it
// before writing to stderr so a diagnostic never overtakes the report it
// explains.
func flushStdout() {
	_ = stdout.Flush()
}

// reportf writes one line of the report. A failed write is ignored here: the
// buffered writer surfaces a real failure at the next flush, and there is no
// channel left to report it on anyway.
func reportf(format string, args ...any) {
	_, _ = fmt.Fprintf(stdout, format, args...)
}

// fail prints a prefixed message to stderr and returns the error exit code. It is
// the single place a diagnostic leaves the program. A failed write is ignored:
// there is no channel left to report it on.
func fail(format string, args ...any) int {
	flushStdout()
	_, _ = fmt.Fprintf(os.Stderr, msgPrefix+format+"\n", args...)
	return exitError
}

// flushNotes renders every build diagnostic the cache collected, indented under
// one header per source. Nothing is printed when no build produced any.
//
// The rendering lives here rather than in the cache: the prefix and the indentation
// are this program's report format, and a package that compiles things has no
// business knowing how a terminal line looks. The cache hands over Notes; this
// decides what they say.
func flushNotes(env *Env) {
	for _, note := range env.DrainNotes() {
		reportf("%scompiled with warnings: %s\n", msgPrefix, text.Text(note.Src))
		for _, line := range note.Lines {
			reportf("  %s\n", line)
		}
	}
}

// usageError is a command-line error, already phrased for the user.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}
