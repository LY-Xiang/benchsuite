// Package genlib is the vocabulary a generator is written in.
//
// A generator declares one Spec: the fields its input binds, the claim those
// fields produce, and the code that writes the bytes. The harness compiles the
// generator (or reads a built-in one from the same interface), runs it once per
// case, and re-derives every fact in the claim from the bytes: a generator whose
// output drifts from its claim is refused rather than trusted.
//
// Both halves of a spec are typed, so a generator cannot emit a claim the
// validator would reject for shape alone. The remaining way to be wrong is to
// write bytes that do not match the claim, which gate 6 catches at run time.
package genlib

import (
	"fmt"
	"math/rand/v2"
	"os"
	"strconv"
	"strings"
)

// Intn is the generator's only source of randomness. The harness leaves it
// unseeded, so a run is not reproducible from the seed alone; correctness rests
// on the reference accepting the input, not on a byte match with an earlier run.
func Intn(lo, hi int) int {
	span := uint64(hi) - uint64(lo) + 1
	return lo + int(rand.Uint64N(span))
}

// Shuffle permutes p in place, top down, so every permutation is equally likely.
func Shuffle[T any](p []T) {
	for i := len(p) - 1; i > 0; i-- {
		j := Intn(0, i)
		p[i], p[j] = p[j], p[i]
	}
}

// Claim describes the shape of the bytes a generator wrote.
//
// The zero Claim describes nothing, which is never what a generator wants: a
// Spec with no claim is refused before it runs.
type Claim struct {
	nodes []Node
}

// Node is one line of the claim: the top level walks `nodes` in order.
type Node struct {
	// Tok is how many space-separated tokens this line has, or the name of a
	// bound field holding that count.
	Tok Operand
	// Field names the fields this line's tokens bind, one per token. Its length
	// equals Tok when both are literals.
	Field []string
	// Raw, when set, is the token index range the bytes must fall in.
	Raw *Raw
	// Range constrains a bound field's value. The grammar nests it as
	// `"range":{"<field>":[lo,hi]}`.
	Range []FieldRange
	// Len binds this line's single token to the name of a field saying how many
	// characters it holds.
	Len string
	// Repeat runs the whole node list again once per unit of the named field.
	Repeat string
	// Lines is the node list a Repeat runs, or nil when Repeat is empty.
	Lines []Node
}

// Raw is a token byte range, inclusive.
type Raw struct{ Lo, Hi int }

// FieldRange constrains the value bound to Field to [Lo,Hi].
type FieldRange struct {
	Field  string
	Lo, Hi Operand
}

// Operand is an integer literal or the name of a bound field.
//
// It is a closed type rather than an `any`, so a generator cannot put a float or
// a map where the grammar expects a count.
type Operand struct {
	literal *int
	field   string
}

// Lit makes an integer operand.
func Lit(n int) Operand { return Operand{literal: &n} }

// Ref makes an operand that reads a bound field.
func Ref(name string) Operand { return Operand{field: name} }

// IsZero reports whether the operand was never set.
func (o Operand) IsZero() bool { return o.literal == nil && o.field == "" }

// AppendTo appends the operand's JSON form to dst.
func (o Operand) appendJSON(dst []byte) []byte {
	if o.literal != nil {
		return strconv.AppendInt(dst, int64(*o.literal), 10)
	}
	return appendJSONString(dst, o.field)
}

// appendJSONString appends s as a JSON string. Claim values are field names,
// which are identifiers, so nothing outside that set can reach here; the escape
// still runs so a name with an unexpected byte cannot produce invalid JSON.
func appendJSONString(dst []byte, s string) []byte {
	dst = append(dst, '"')
	for i := range len(s) {
		c := s[i]
		switch c {
		case '"', '\\':
			dst = append(dst, '\\', c)
		default:
			if c < 0x20 {
				const hex = "0123456789abcdef"
				dst = append(dst, '\\', 'u', '0', '0', hex[c>>4], hex[c&0xf])
				continue
			}
			dst = append(dst, c)
		}
	}
	return append(dst, '"')
}

// New builds a claim from its nodes.
func New(nodes ...Node) Claim { return Claim{nodes: nodes} }

// Bytes renders the claim as the JSON the harness validates. The trailing newline
// matches what a generator writes to stdout and keeps the file a text file.
func (c Claim) Bytes() []byte {
	out := make([]byte, 0, 128)
	out = append(out, `{"lines":[`...)
	for i, node := range c.nodes {
		if i > 0 {
			out = append(out, ',')
		}
		out = node.appendJSON(out)
	}
	out = append(out, ']', '}', '\n')
	return out
}

func (n Node) appendJSON(dst []byte) []byte {
	dst = append(dst, '{')
	first := true
	key := func(name string) {
		if !first {
			dst = append(dst, ',')
		}
		first = false
		dst = appendJSONString(dst, name)
		dst = append(dst, ':')
	}

	if !n.Tok.IsZero() {
		key("tok")
		dst = n.Tok.appendJSON(dst)
	}
	if len(n.Field) > 0 {
		key("field")
		dst = append(dst, '[')
		for i, name := range n.Field {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJSONString(dst, name)
		}
		dst = append(dst, ']')
	}
	if n.Raw != nil {
		key("bytes")
		dst = append(dst, '[')
		dst = strconv.AppendInt(dst, int64(n.Raw.Lo), 10)
		dst = append(dst, ',')
		dst = strconv.AppendInt(dst, int64(n.Raw.Hi), 10)
		dst = append(dst, ']')
	}
	if len(n.Range) > 0 {
		key("range")
		dst = append(dst, '{')
		for i, r := range n.Range {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = appendJSONString(dst, r.Field)
			dst = append(dst, ':', '[')
			dst = r.Lo.appendJSON(dst)
			dst = append(dst, ',')
			dst = r.Hi.appendJSON(dst)
			dst = append(dst, ']')
		}
		dst = append(dst, '}')
	}
	if n.Len != "" {
		key("len")
		dst = appendJSONString(dst, n.Len)
	}
	if n.Repeat != "" {
		key("repeat")
		dst = appendJSONString(dst, n.Repeat)
	}
	if n.Repeat != "" {
		key("lines")
		dst = append(dst, '[')
		for i, child := range n.Lines {
			if i > 0 {
				dst = append(dst, ',')
			}
			dst = child.appendJSON(dst)
		}
		dst = append(dst, ']')
	}
	return append(dst, '}')
}

// Base is what a generator is handed once per case.
//
// It is a value, not an interface, so a generator reads fields and calls methods
// without a dispatch per byte. W is the byte sink: every write goes through it so
// the ceiling and the allocation guard cannot be bypassed.
type Base struct {
	// Args are the spec's parameters past the seed, already parsed as integers.
	// A textual parameter (`letters ... pan`) stays here as text only in Raw.
	Args []int
	// Raw holds the spec's words past the seed, verbatim, so a generator can read
	// a non-numeric parameter without a second grammar.
	Raw  []string
	Seed int
	w    W
}

// W is the byte sink. Every write goes through it, so a generator cannot reach a
// buffer that bypasses the ceiling.
func (b *Base) W() *W { return &b.w }

// Arg reports parameter i, defaulting to 1: zero would silently turn a count into
// "write nothing", which hides a spec typo behind an empty input.
func (b *Base) Arg(i int) int {
	if i < len(b.Args) {
		return b.Args[i]
	}
	return 1
}

// RawArg reports the i-th textual parameter, or "" when absent.
func (b *Base) RawArg(i int) string {
	if i < len(b.Raw) {
		return b.Raw[i]
	}
	return ""
}

// W is the byte sink a generator writes through.
//
// Every method appends under one byte ceiling, so a spec naming a huge count is a
// refused case rather than exhausted memory. A generator cannot reach the
// underlying buffer, so the ceiling holds for every kind of write.
type W struct {
	buf     strings.Builder
	overrun bool
}

// Overrun reports that a ceiling stopped the writes.
func (w *W) Overrun() bool { return w.overrun }

// Bytes returns what was written. It is read once, after the generator returns.
func (w *W) Bytes() []byte { return []byte(w.buf.String()) }

// MaxBytes bounds one generated input.
const MaxBytes = 64 << 20 // 64 MiB

// MaxCount bounds an element count where the byte ceiling is too loose: a buffer
// may be sized from the count before any byte is written.
const MaxCount = 1 << 24

// overrunSignal unwinds a generator that passed a ceiling. A panic rather than a
// flag, because a flag only stops the loop its author remembered to guard.
type overrunSignal struct{}

// Int writes a decimal integer.
func (w *W) Int(v int) {
	w.reserve(len(strconv.Itoa(v)))
	w.buf.WriteString(strconv.Itoa(v))
}

// IntLine writes a decimal integer and a newline.
func (w *W) IntLine(v int) { w.Int(v); w.Byte('\n') }

// Byte writes one byte.
func (w *W) Byte(c byte) {
	w.reserve(1)
	w.buf.WriteByte(c)
}

// Str writes a string verbatim.
func (w *W) Str(s string) {
	w.reserve(len(s))
	w.buf.WriteString(s)
}

// RawBytes writes b verbatim.
func (w *W) RawBytes(b []byte) {
	w.reserve(len(b))
	_, _ = w.buf.Write(b)
}

// Values writes n integers drawn from [lo,hi], space separated, then a newline.
// A zero count writes nothing at all, not an empty line.
func (w *W) Values(n, lo, hi int) {
	for i := range n {
		if i > 0 {
			w.Byte(' ')
		}
		w.Int(Intn(lo, hi))
	}
	if n > 0 {
		w.Byte('\n')
	}
}

// Pair writes two integers on one line.
func (w *W) Pair(a, b int) { w.Int(a); w.Byte(' '); w.IntLine(b) }

// Perm writes a permutation of 1..n on one line. n must be non-negative, since it
// sizes a slice; zero writes nothing.
func (w *W) Perm(n int) {
	w.GuardCount(n)
	p := make([]int, n)
	for i := range p {
		p[i] = i + 1
	}
	Shuffle(p)
	for i := range n {
		if i > 0 {
			w.Byte(' ')
		}
		w.Int(p[i])
	}
	if n > 0 {
		w.Byte('\n')
	}
}

// GuardCount rejects an element count before a buffer is sized from it.
func (w *W) GuardCount(n int) {
	if n > MaxCount {
		w.overrun = true
		panic(overrunSignal{})
	}
}

func (w *W) reserve(n int) {
	if w.overrun {
		panic(overrunSignal{})
	}
	if w.buf.Len()+n > MaxBytes {
		w.overrun = true
		panic(overrunSignal{})
	}
}

// RangeOf rejects a draw intn cannot make: an empty interval, or one so wide the
// span wraps to zero. Either would panic.
func RangeOf(lo, hi int) error {
	if hi < lo {
		return fmt.Errorf("random range %d..%d is empty", lo, hi)
	}
	if uint64(hi)-uint64(lo)+1 == 0 {
		return fmt.Errorf("random range %d..%d is too wide to draw from", lo, hi)
	}
	return nil
}

// Spec is one generator kind.
//
// Fields and Claim are read once per run, before Emit; Validate runs before Emit
// and its job is narrow -- prove no draw in Emit can panic. A nonsensical count
// that still emits is gate 6's business, not Validate's.
type Spec struct {
	// Name is the spec word that selects this generator.
	Name string
	// Fields are the claim field names this generator binds, for `expect`
	// validation. A generator that leaves this empty opens the set instead.
	Fields []string
	// Claim is the shape of the bytes Emit writes.
	Claim Claim
	// Validate refuses a spec whose draws cannot be made.
	Validate func(b *Base) error
	// ClaimFor, when set, builds the claim from the parameters instead of using
	// Claim, for a generator whose claim depends on a spec word (`queries`).
	ClaimFor func(b *Base) Claim
	// Emit writes the input bytes.
	Emit func(b *Base)
}

// Run executes one spec, returning the input and its claim.
//
// It is the single entry point for every generator: a built-in kind and a
// registry-declared one differ only in how the Spec was obtained, never in how it
// is run.
func Run(spec Spec, seed int, args []int, raw []string) (input, claim []byte, err error) {
	g := &Base{Args: args, Raw: raw, Seed: seed}
	if spec.Validate != nil {
		if err := spec.Validate(g); err != nil {
			return nil, nil, err
		}
	}
	emitted := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(overrunSignal); !ok {
					panic(r) // a real bug, not a ceiling overrun
				}
			}
		}()
		spec.Emit(g)
		emitted = true
	}()
	if !emitted || g.w.Overrun() {
		return nil, nil, fmt.Errorf("generator %s produced more than %d bytes", spec.Name, MaxBytes)
	}
	resolved := spec.Claim
	if spec.ClaimFor != nil {
		resolved = spec.ClaimFor(g)
	}
	return g.w.Bytes(), resolved.Bytes(), nil
}

// Main is the entry point a registry-declared generator calls.
//
// It has no caller inside this package by design: a generator is a separate
// program that imports genlib, so the harness never runs this function itself. The
// registry path in src/main compiles such a program and runs it; that is the only
// caller, and it lives outside this module at build time.
//
// The generator writes one function -- what to emit and what claim describes it --
// and Main supplies everything else: it parses argv (seed, claim path, spec
// parameters), runs the emit under the byte ceiling, writes the claim to the path
// argv[2] names, and writes the input to stdout.
//
// This is the same Spec a built-in kind is, so a generator declared in a registry
// and one shipped with the harness differ only in where the function came from.
//
//	func main() {
//	    genlib.Main(func(b *genlib.Base) genlib.Claim {
//	        n := b.Arg(0)
//	        b.W().IntLine(n)
//	        b.W().Values(n, 1, 100)
//	        return genlib.New(
//	            genlib.Node{Tok: genlib.Lit(1), Field: []string{"n"}},
//	            genlib.Node{Tok: genlib.Ref("n")},
//	        )
//	    })
//	}
//
//goland:noinspection GoUnusedExportedFunction
func Main(emit func(b *Base) Claim) {
	args := os.Args[1:]
	if len(args) < 2 {
		_, _ = fmt.Fprintln(os.Stderr, "usage: <generator> <seed> <claim-path> [params...]")
		os.Exit(2)
	}
	seed, err := strconv.Atoi(args[0])
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "seed %q is not an integer\n", args[0])
		os.Exit(2)
	}
	claimPath := args[1]
	raw := args[2:]
	params := make([]int, len(raw))
	for i, word := range raw {
		value, convErr := strconv.Atoi(word)
		if convErr != nil {
			_, _ = fmt.Fprintf(os.Stderr, "parameter %d is not a number: %q\n", i+1, word)
			os.Exit(2)
		}
		params[i] = value
	}

	b := &Base{Args: params, Raw: raw, Seed: seed}
	var claim Claim
	overrun := false
	func() {
		defer func() {
			if r := recover(); r != nil {
				if _, ok := r.(overrunSignal); !ok {
					panic(r)
				}
				overrun = true
			}
		}()
		claim = emit(b)
	}()
	if overrun || b.w.Overrun() {
		_, _ = fmt.Fprintf(os.Stderr, "input passed the %d byte ceiling\n", MaxBytes)
		os.Exit(1)
	}
	if err := os.WriteFile(claimPath, claim.Bytes(), 0o644); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "cannot write claim: %v\n", err)
		os.Exit(1)
	}
	if _, err := os.Stdout.Write(b.w.Bytes()); err != nil {
		os.Exit(1)
	}
}
