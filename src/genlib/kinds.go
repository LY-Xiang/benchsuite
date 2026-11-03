package genlib

import "fmt"

// The built-in kinds. Each is a Spec like any other: the harness reaches these
// directly rather than through a compiled generator, but Run treats both the
// same, so a built-in cannot quietly acquire a privilege a plugin lacks.

// Kind is the registry every built-in generator lands in.
var builtinKinds []Spec

func register(specs ...Spec) {
	builtinKinds = append(builtinKinds, specs...)
}

// Kinds returns the built-in generators, in registration order.
func Kinds() []Spec { return builtinKinds }

func init() {
	register(
		oneshotSpec(),
		fixedSpec(),
		intsSpec(),
		t2Spec(),
		nkSpec(),
		permSpec(),
		progSpec(),
		lettersSpec(),
		palinSpec(),
		pairsSpec(),
		queriesSpec(),
	)
}

// Claim shapes. Each is the descriptor a family of generators shares.
var (
	// claimBare: one line, one token, printable ASCII.
	claimBare = New(Node{Tok: Lit(1), Raw: &Raw{33, 126}})
	// claimNott: an n line, then exactly n values.
	claimNott = New(
		Node{Tok: Lit(1), Field: []string{"n"}},
		Node{Tok: Ref("n")},
	)
	// claimStr: an n line, then one token of exactly n characters.
	claimStr = New(
		Node{Tok: Lit(1), Field: []string{"n"}},
		Node{Tok: Lit(1), Len: "n"},
	)
	// claimTn: a t line, then per case an n line and exactly n values.
	claimTn = New(
		Node{Tok: Lit(1), Field: []string{"t"}},
		Node{Repeat: "t", Lines: []Node{
			{Tok: Lit(1), Field: []string{"n"}},
			{Tok: Ref("n")},
		}},
	)
	// claimTn2: a t line, then per case an "n x" line and n values.
	claimTn2 = New(
		Node{Tok: Lit(1), Field: []string{"t"}},
		Node{Repeat: "t", Lines: []Node{
			{Tok: Lit(2), Field: []string{"n", "x"}},
			{Tok: Ref("n")},
		}},
	)
	// claimNk: a t line, then per case an "n k" line with k in [1,n], then n
	// values.
	claimNk = New(
		Node{Tok: Lit(1), Field: []string{"t"}},
		Node{Repeat: "t", Lines: []Node{
			{Tok: Lit(2), Field: []string{"n", "k"}, Range: []FieldRange{{Field: "k", Lo: Lit(1), Hi: Ref("n")}}},
			{Tok: Ref("n")},
		}},
	)
	// claimTs2: a t line, then per case an "n c" line and a token of n chars.
	claimTs2 = New(
		Node{Tok: Lit(1), Field: []string{"t"}},
		Node{Repeat: "t", Lines: []Node{
			{Tok: Lit(2), Field: []string{"n", "c"}},
			{Tok: Lit(1), Len: "n"},
		}},
	)
	// claimTh2: a t line, then per case one two-token line.
	claimTh2 = New(
		Node{Tok: Lit(1), Field: []string{"t"}},
		Node{Repeat: "t", Lines: []Node{
			{Tok: Lit(2)},
		}},
	)
)

// claim1nq is the query shape a single-test problem writes: a literal 1, an
// "n q" header, n values, then q lines of `wire` tokens each.
func claim1nq(wire int) Claim {
	return New(
		Node{Tok: Lit(1)},
		Node{Tok: Lit(2), Field: []string{"n", "q"}},
		Node{Tok: Ref("n")},
		Node{Repeat: "q", Lines: []Node{{Tok: Lit(wire)}}},
	)
}

func oneshotSpec() Spec {
	return Spec{
		Name:   "oneshot",
		Fields: []string{"n"},
		Claim:  claimNott,
		Validate: func(b *Base) error {
			if err := RangeOf(1, b.Arg(0)); err != nil {
				return err
			}
			return RangeOf(b.Arg(1), b.Arg(2))
		},
		Emit: func(b *Base) {
			n := Intn(1, b.Arg(0))
			b.W().IntLine(n)
			b.W().Values(n, b.Arg(1), b.Arg(2))
		},
	}
}

func fixedSpec() Spec {
	return Spec{
		Name:   "fixed",
		Fields: []string{"n"},
		Claim:  claimNott,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil // writes nothing, so no range is reached
			}
			return RangeOf(b.Arg(1), b.Arg(2))
		},
		Emit: func(b *Base) {
			n := b.Arg(0)
			b.W().IntLine(n)
			b.W().Values(n, b.Arg(1), b.Arg(2))
		},
	}
}

func intsSpec() Spec {
	return Spec{
		Name:   "ints",
		Fields: []string{"t", "n"},
		Claim:  claimTn,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			if err := RangeOf(1, b.Arg(1)); err != nil {
				return err
			}
			return RangeOf(b.Arg(2), b.Arg(3))
		},
		Emit: func(b *Base) {
			cases := b.Arg(0)
			b.W().IntLine(cases)
			for range cases {
				n := Intn(1, b.Arg(1))
				b.W().IntLine(n)
				b.W().Values(n, b.Arg(2), b.Arg(3))
			}
		},
	}
}

func t2Spec() Spec {
	return Spec{
		Name:   "t2",
		Fields: []string{"t", "n", "x"},
		Claim:  claimTn2,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			if err := RangeOf(1, b.Arg(1)); err != nil {
				return err
			}
			return RangeOf(b.Arg(2), b.Arg(3))
		},
		Emit: func(b *Base) {
			cases := b.Arg(0)
			b.W().IntLine(cases)
			for range cases {
				n := Intn(1, b.Arg(1))
				b.W().Pair(n, Intn(b.Arg(2), b.Arg(3)))
				b.W().Values(n, b.Arg(2), b.Arg(3))
			}
		},
	}
}

func nkSpec() Spec {
	return Spec{
		Name:   "nk",
		Fields: []string{"t", "n", "k"},
		Claim:  claimNk,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			if err := RangeOf(1, b.Arg(1)); err != nil {
				return err
			}
			return RangeOf(b.Arg(2), b.Arg(3))
		},
		Emit: func(b *Base) {
			cases := b.Arg(0)
			b.W().IntLine(cases)
			for range cases {
				n := Intn(1, b.Arg(1))
				b.W().Pair(n, Intn(1, n))
				b.W().Values(n, b.Arg(2), b.Arg(3))
			}
		},
	}
}

func permSpec() Spec {
	return Spec{
		Name:   "perm",
		Fields: []string{"t", "n"},
		Claim:  claimTn,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			return RangeOf(1, b.Arg(1))
		},
		Emit: func(b *Base) {
			cases := b.Arg(0)
			b.W().IntLine(cases)
			for range cases {
				n := Intn(1, b.Arg(1))
				b.W().IntLine(n)
				b.W().Perm(n)
			}
		},
	}
}

// progSpec emits a single line of printable ASCII. It is the HQ9+ program shape:
// a seed divisible by three draws from the printable range minus the four
// instruction characters, so that seed yields a program printing NO.
func progSpec() Spec {
	const instructions = "HQ9+"
	return Spec{
		Name:  "prog",
		Claim: claimBare,
		Validate: func(b *Base) error {
			return RangeOf(1, b.Arg(0))
		},
		Emit: func(b *Base) {
			n := Intn(1, b.Arg(0))
			b.W().GuardCount(n)
			w := b.W()
			if b.Seed%3 == 0 {
				for range n {
					for {
						c := byte(33 + Intn(0, 93))
						if !contains(instructions, c) {
							w.Byte(c)
							break
						}
					}
				}
			} else {
				for range n {
					r := Intn(0, 99)
					switch {
					case r < 12:
						w.Byte(instructions[r%4])
					case r < 18:
						w.Byte(byte('a' + Intn(0, 25)))
					default:
						w.Byte(byte(33 + Intn(0, 93)))
					}
				}
			}
			w.Byte('\n')
		},
	}
}

func contains(s string, c byte) bool {
	for i := range len(s) {
		if s[i] == c {
			return true
		}
	}
	return false
}

func lettersSpec() Spec {
	return Spec{
		Name:   "letters",
		Fields: []string{"n"},
		Claim:  claimStr,
		Validate: func(b *Base) error {
			return RangeOf(1, b.Arg(0))
		},
		Emit: func(b *Base) {
			pangram := b.RawArg(1) == "pan"
			n := Intn(1, b.Arg(0))
			b.W().IntLine(n)
			w := b.W()
			if pangram && n >= 26 {
				b.W().GuardCount(n)
				letters := make([]byte, 26, n)
				for i := range letters {
					letters[i] = byte('A' + i)
				}
				Shuffle(letters)
				for range n - 26 {
					letters = append(letters, byte('A'+Intn(0, 25)))
				}
				w.RawBytes(letters)
			} else {
				b.W().GuardCount(n)
				for range n {
					w.Byte(byte('A' + Intn(0, 25)))
				}
			}
			w.Byte('\n')
		},
	}
}

func palinSpec() Spec {
	return Spec{
		Name:   "palin",
		Fields: []string{"t", "n", "c"},
		Claim:  claimTs2,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			// The alphabet size is not validated: the emit path clamps below 1.
			return RangeOf(1, b.Arg(1))
		},
		Emit: func(b *Base) {
			cases, length, alpha := b.Arg(0), b.Arg(1), b.Arg(2)
			if alpha < 1 {
				alpha = 1
			}
			w := b.W()
			w.IntLine(cases)
			for range cases {
				n := Intn(1, length)
				alphabet := Intn(1, alpha)
				w.Int(n)
				w.Byte(' ')
				w.Byte(byte('a' + Intn(0, 25)))
				w.Byte('\n')
				for range n {
					w.Byte(byte('a' + Intn(0, alphabet-1)))
				}
				w.Byte('\n')
			}
		},
	}
}

func pairsSpec() Spec {
	return Spec{
		Name:   "pairs",
		Fields: []string{"t"},
		Claim:  claimTh2,
		Validate: func(b *Base) error {
			if b.Arg(0) <= 0 {
				return nil
			}
			return RangeOf(b.Arg(2), b.Arg(3))
		},
		Emit: func(b *Base) {
			cases, first := b.Arg(0), b.Arg(1)
			w := b.W()
			w.IntLine(cases)
			for range cases {
				w.Pair(first, Intn(b.Arg(2), b.Arg(3)))
			}
		},
	}
}

func queriesSpec() Spec {
	return Spec{
		Name:   "queries",
		Fields: []string{"n", "q"},
		Validate: func(b *Base) error {
			if wire := b.Arg(3); wire < 1 || wire > 2 {
				return fmt.Errorf("query line width %d must be 1 or 2", wire)
			}
			// A 2-wide query line is a position and a value. The position indexes
			// the n elements, so it is drawn from [1,n] and never from
			// [0,ceiling]: a solution is entitled to reject 0 there, and emitting
			// it turns a generator bug into a crashing solution.
			//
			// This matches the frozen reference generator, which BASELINE.md names
			// as the oracle for generator output: `b8` in tools/gen.cpp, the kind
			// this spec replaced, writes ri(1, a3) for the position and ri(0, 15)
			// for the value.
			//
			// Checked before the n<=0 early return below, because Emit still draws
			// a position when n is zero and [1,0] would panic.
			if b.Arg(3) == 2 {
				if err := RangeOf(1, b.Arg(0)); err != nil {
					return fmt.Errorf("query position range needs n >= 1: %w", err)
				}
			}
			if b.Arg(0) <= 0 || b.Arg(1) <= 0 {
				return nil
			}
			return RangeOf(0, b.Arg(2))
		},
		Emit: func(b *Base) {
			n, q, ceiling := b.Arg(0), b.Arg(1), b.Arg(2)
			wire := b.Arg(3)
			w := b.W()
			w.Str("1\n")
			w.Pair(n, q)
			w.Values(n, 0, ceiling)
			for range q {
				// A 2-wide line is "position value": the position is 1-based over
				// the n elements, the value comes from [0,ceiling]. A 1-wide line
				// is that value alone.
				if wire == 2 {
					w.Int(Intn(1, n))
					w.Byte(' ')
					w.Int(Intn(0, ceiling))
				} else {
					w.Int(Intn(0, ceiling))
				}
				w.Byte('\n')
			}
		},
		// The claim pins the wire count, so it is built per spec rather than once.
		ClaimFor: func(b *Base) Claim { return claim1nq(b.Arg(3)) },
	}
}
