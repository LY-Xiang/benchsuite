// Package text renders values for a terminal, a file or a report.
//
// Registry keys, ids and argv values are attacker-adjacent in the sense that they
// reach a gate line: a control byte in one could repaint the terminal or forge a
// result. Every layer that prints such a value goes through Text so the rule is
// stated once rather than re-derived per site.
package text

import (
	"strings"
	"unicode/utf8"
)

// Text renders a string for a terminal, a file or a report. Newline and tab stay;
// every other C0 byte, DEL and the C1 range become U+FFFD, so a registry or argv
// value cannot repaint the terminal or forge a gate line.
//
// The common case is a value with nothing to replace, which is returned as-is:
// the check is a byte scan and the rewrite only allocates when it has to.
func Text(s string) string {
	if !hasControl(s) {
		return s
	}
	var b strings.Builder
	b.Grow(len(s))
	for _, r := range s {
		if isUnsafeRune(r) {
			b.WriteRune('\uFFFD')
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}

// Quote renders s as a single-quoted, printable token for an error message.
//
// The value is sanitized here rather than at each call site, so a quoted name can
// never carry a control byte into a gate line: every caller wants both, and
// splitting the two is how one of them gets forgotten.
func Quote(s string) string { return "'" + Text(s) + "'" }

// isUnsafeRune reports whether r would be replaced by Text. C1 controls only
// appear as multi-byte UTF-8, so they are checked on the decoded rune.
func isUnsafeRune(r rune) bool {
	return (r < 0x20 && r != '\n' && r != '\t') || r == 0x7f || (r >= 0x80 && r <= 0x9f)
}

// hasControl is the byte-level fast path for Text: ASCII controls, DEL and a C1
// lead byte all fail it, and everything else is returned untouched.
func hasControl(s string) bool {
	for i := 0; i < len(s); i++ {
		c := s[i]
		if c < 0x20 {
			if c != '\n' && c != '\t' {
				return true
			}
			continue
		}
		if c == 0x7f {
			return true
		}
		if c >= 0x80 {
			// Only U+0080..U+009F is unsafe, and it starts with C2 then 80..9F.
			r, size := utf8.DecodeRuneInString(s[i:])
			if isUnsafeRune(r) {
				return true
			}
			i += size - 1
		}
	}
	return false
}
