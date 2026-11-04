package registry

import (
	"bytes"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/BurntSushi/toml"

	"github.com/LY-Xiang/benchsuite/src/text"
)

// Lookups is what loading a registry needs to know about generator kinds.
//
// It is an interface rather than a call into the generator package because loading
// a table is a data question: which kind names exist, and which of them are
// declared by a registry itself. Asking the generator runtime directly would make
// the data layer depend on the runtime that consumes it, which is the direction
// that lets a load error pull in process spawning.
type Lookups interface {
	// BuiltinFields reports the field names a built-in kind binds, and whether the
	// kind exists at all.
	BuiltinFields(kind string) ([]string, bool)
	// DeclaredGenerator reports whether kind is declared by a registry `[gen.*]`
	// table, whose claim is written by the generator itself and so binds fields
	// this layer cannot know.
	DeclaredGenerator(kind string) bool
	// LoadGenerators validates one `[gen.*]` table and installs what it declares.
	// Parsing the shapes is the generator package's business: a table here is data,
	// but its contents become a compiled generator.
	LoadGenerators(contest, path string, tables map[string]any) error
}

// lookups is installed once before any table is read, by the caller that knows
// both the built-in kinds and the declared ones.
var lookups Lookups

// SetLookups installs the generator knowledge loading needs. It must be called
// before LoadAll or CheckTree; a load that runs without it would silently treat
// every kind as unknown.
func SetLookups(l Lookups) {
	if l == nil {
		panic("registry: nil Lookups")
	}
	lookups = l
}

// A Gate is a named harness failure. The id lets a caller tell a bad registry
// from a bad claim without matching on message text.
type Gate struct {
	ID     string
	Detail string
}

func (g *Gate) Error() string { return g.Detail }

func LoadErrorf(format string, args ...any) error {
	return &Gate{ID: "L1", Detail: fmt.Sprintf(format, args...)}
}

func TreeErrorf(format string, args ...any) error {
	return &Gate{ID: "L2", Detail: fmt.Sprintf(format, args...)}
}

// A Problem is one registered benchmark problem. Src is repo-root relative with
// forward slashes, matching the registry text that declared it.
type Problem struct {
	ID     string // "2267C"
	Src    string // "2267/C.cpp"
	Gen    string // "" when absent
	MaxGen string
	Expect string // "" when absent
	Stale  []string
	MaxChk bool
	Shape  Shape // which reference questions the harness may ask
	// Contest is the declaring directory, which also holds its refs/. Recorded
	// rather than re-derived from Src, which may be overridden to a bare name.
	Contest string // "2267"
	TL      string // display only; the driver never enforces it
	// Registry is the bench.toml that declared this problem. The lock holding
	// the build hashes sits beside it.
	Registry string
}

// idRe is a contest number followed by a letter: `[C]` under `2267/` is `2267C`.
var idRe = regexp.MustCompile(`\A([0-9]+)([A-Z][A-Za-z0-9]*)\z`)

var expectFieldRe = regexp.MustCompile(`\Acases\.([A-Za-z_][A-Za-z0-9_]*)\z`)

// expectFields confirms an `expect` sum names only fields the generator binds.
// `expect` is an independent oracle: it is never derived from the reference.
func expectFields(gen, expect string) error {
	// A composed gen binds the union of its parts, so a repeated field name in
	// two parts is one binding here.
	binds := map[string]bool{}
	// A registry-declared generator writes its own claim, so the fields it binds
	// are not knowable here. It opens the set rather than closing it, which is the
	// price of letting a registry define a kind at all; a wrong `expect` still
	// fails at run time against the claim gate.
	open := false
	for part := range strings.SplitSeq(gen, " + ") {
		words := strings.Fields(part)
		if len(words) == 0 {
			return LoadErrorf("gen must not be empty")
		}
		kind := words[0]
		bound, known := lookups.BuiltinFields(kind)
		if !known {
			if lookups.DeclaredGenerator(kind) {
				open = true
				continue
			}
			return LoadErrorf("unknown generator kind %s", text.Quote(kind))
		}
		for _, name := range bound {
			binds[name] = true
		}
	}
	atoms := strings.Split(expect, "+")
	for _, atom := range atoms {
		if strings.TrimSpace(atom) == "" {
			return LoadErrorf("expect must use 1, cases, or cases.<field> joined by +")
		}
	}
	for _, atom := range atoms {
		atom = strings.TrimSpace(atom)
		switch atom {
		case "1":
		case "cases":
			if !binds["t"] && !open {
				return LoadErrorf("expect cases refers to unbound field t")
			}
		default:
			match := expectFieldRe.FindStringSubmatch(atom)
			if match == nil {
				return LoadErrorf("invalid expect atom %s", text.Quote(atom))
			}
			if !binds[match[1]] && !open {
				return LoadErrorf("expect refers to unbound field %s", text.Quote(match[1]))
			}
		}
	}
	return nil
}

// emptySolutionBodies is the source body that carries no code at all, once
// whitespace is dropped.
const emptySolutionBodies = "intmain(){}"

// isEmptySolutionAt reports whether a registered solution carries no code,
// reading it through an opened repository root so the path resolves inside it
// rather than by the kernel's own walk from the process root. The error reports a
// file that exists but cannot be read; the caller tells that apart from an empty
// one -- and from an unreadable one, which is never called empty.
func isEmptySolutionAt(repo *os.Root, name string) (bool, error) {
	data, err := repo.ReadFile(name)
	if err != nil {
		return false, err
	}
	return isEmptySource(data), nil
}

// isEmptySource reports whether the solution source carries no code. Content that
// is not valid UTF-8 is never empty: it holds something this reader cannot see,
// and calling it empty would hide a real compile failure.
func isEmptySource(data []byte) bool {
	// Nobody writes a solution whose code is a comment, so comment removal is
	// the fast path out of a real file: the first visible byte that is not part
	// of the body refuses the file without copying it.
	if !utf8.Valid(data) {
		return false
	}
	matched := 0
	for i := 0; i < len(data); {
		// Skip a comment as a leftmost-first scan would match it: a line comment
		// runs to the next newline, a block comment to the first closing pair,
		// and an unterminated block opener is text.
		if n, ok := skipComment(data[i:]); ok {
			i += n
			continue
		}
		r, size := utf8.DecodeRune(data[i:])
		i += size
		if unicode.IsSpace(r) {
			continue
		}
		if matched == len(emptySolutionBodies) || r != rune(emptySolutionBodies[matched]) {
			return false
		}
		matched++
	}
	return matched == 0 || matched == len(emptySolutionBodies)
}

// skipComment reports the length of the comment at the start of b, if b starts
// one: `//` to the end of the line, `/*` to the first closing pair, and an
// unclosed block opener is not a comment at all.
func skipComment(b []byte) (int, bool) {
	if len(b) < 2 || b[0] != '/' {
		return 0, false
	}
	switch b[1] {
	case '/':
		for i := 2; i < len(b); i++ {
			if b[i] == '\r' || b[i] == '\n' {
				return i, true
			}
		}
		return len(b), true
	case '*':
		end := bytes.Index(b[2:], []byte("*/"))
		if end < 0 {
			return 0, false
		}
		return 2 + end + 2, true
	}
	return 0, false
}

var sourceRe = regexp.MustCompile(`\A([0-9]+)/([A-Z][A-Za-z0-9]*)\.cpp\z`)

// CheckTree requires every registered solution to exist and be non-empty, and
// rejects a solution sitting in a contest directory the registry owns. The root is
// a general practice checkout, so the check is scoped to the owned contests
// rather than "every solution under root is registered".
func CheckTree(problems map[string]Problem, root string) error {
	// The root is opened once: every read below goes through it, so a src that
	// is lexically confined cannot still resolve outside the repository through
	// a symbolic link. checkSrcConfined bounds the text; this bounds the file
	// system.
	repoRoot, err := os.OpenRoot(root)
	if err != nil {
		return TreeErrorf("cannot open the repository root: %v", err)
	}
	defer func() { _ = repoRoot.Close() }()

	for _, id := range SortedIDs(problems) {
		problem := problems[id]
		relative := filepath.FromSlash(problem.Src)
		info, err := repoRoot.Stat(relative)
		if err != nil || info.IsDir() {
			return TreeErrorf("%s: solution file is missing: %s", text.Text(id), text.Text(problem.Src))
		}
		empty, err := isEmptySolutionAt(repoRoot, relative)
		if err != nil {
			// A solution that exists but cannot be read is not a missing one:
			// reporting it as empty would claim a compile failure that has not
			// happened.
			return TreeErrorf("%s: cannot read solution file: %v", text.Text(id), err)
		}
		if empty {
			return TreeErrorf("%s: solution file is empty: %s", text.Text(id), text.Text(problem.Src))
		}
	}

	owned := map[string]bool{}
	for _, problem := range problems {
		if problem.Contest != "" {
			owned[problem.Contest] = true
		}
		// The declaring contest is audited, and so is any contest a src names:
		// such a directory can hold an unregistered solution of its own.
		if named, _, found := strings.Cut(problem.Src, "/"); found && named != "" {
			owned[named] = true
		}
	}
	for _, contest := range slices.Sorted(maps.Keys(owned)) {
		entries, err := os.ReadDir(filepath.Join(root, contest))
		if err != nil {
			// Every registered solution was stat'd above, so the directory
			// exists. What remains is a permission or volume error, which could
			// hide an unregistered solution, so it fails closed rather than
			// reading as an empty contest.
			return TreeErrorf("%s: cannot scan contest directory: %v", contest, err)
		}
		var names []string
		for _, entry := range entries {
			if !entry.IsDir() {
				names = append(names, entry.Name())
			}
		}
		slices.Sort(names)
		for _, name := range names {
			relative := contest + "/" + name
			match := sourceRe.FindStringSubmatch(relative)
			if match == nil {
				continue
			}
			discovered := match[1] + match[2]
			if _, registered := problems[discovered]; !registered {
				return TreeErrorf("%s: solution is not registered: %s", text.Text(discovered), text.Text(relative))
			}
		}
	}
	return nil
}

var tlRe = regexp.MustCompile(`\bTL\s+(\S+)`)

// timeLimits recovers each table's time limit from the comment block above it.
// It is documentation, not a harness input: `--list` and `--bench` display it,
// nothing enforces it.
func timeLimits(raw string) map[string]string {
	limits := map[string]string{}
	pending := ""
	for line := range strings.SplitSeq(raw, "\n") {
		stripped := strings.TrimSpace(line)
		if strings.HasPrefix(stripped, "#") {
			if match := tlRe.FindStringSubmatch(stripped); match != nil {
				pending = match[1]
			}
			continue
		}
		if strings.HasPrefix(stripped, "[") && strings.HasSuffix(stripped, "]") {
			if pending != "" {
				limits[strings.TrimSpace(stripped[1:len(stripped)-1])] = pending
			}
			pending = ""
		}
	}
	return limits
}

// LoadAll discovers every `<contest>/bench.toml` under root, validates them all,
// then runs the tree check over the combined set.
//
// The optional contests narrow the scan; naming none scans every contest. A named
// contest with no registry is refused rather than loading zero problems, since
// `bench 9999` reporting "0/0 passed" is the failure this gate stops.
func LoadAll(root string, contests ...string) (map[string]Problem, error) {
	paths, err := filepath.Glob(filepath.Join(root, "*", "bench.toml"))
	if err != nil {
		return nil, LoadErrorf("cannot scan %s for bench.toml: %v", root, err)
	}
	slices.Sort(paths)

	if len(contests) > 0 {
		wanted := make(map[string]bool, len(contests))
		for _, contest := range contests {
			wanted[contest] = true
		}
		kept := make([]string, 0, len(paths))
		found := make(map[string]bool, len(contests))
		for _, path := range paths {
			if contest := filepath.Base(filepath.Dir(path)); wanted[contest] {
				kept = append(kept, path)
				found[contest] = true
			}
		}
		var missing []string
		for _, contest := range slices.Sorted(maps.Keys(wanted)) {
			if !found[contest] {
				missing = append(missing, contest)
			}
		}
		if len(missing) > 0 {
			return nil, LoadErrorf("no bench.toml found for contest %s under %s",
				strings.Join(missing, ", "), root)
		}
		paths = kept
	}

	if len(paths) == 0 {
		return nil, LoadErrorf("no bench.toml found under %s", root)
	}

	problems := map[string]Problem{}
	origin := map[string]string{}
	for _, path := range paths {
		contest := filepath.Base(filepath.Dir(path))
		if err := loadContest(path, contest, problems, origin); err != nil {
			return nil, err
		}
	}
	if err := CheckTree(problems, root); err != nil {
		return nil, err
	}
	return problems, nil
}

// CheckSrcConfined reports whether a registry src stays inside the repository.
// The check is lexical, made before anything resolves the value, so it cannot
// depend on what exists on disk: a traversal naming a readable file would
// otherwise be accepted.
func CheckSrcConfined(src string) error {
	slash := filepath.ToSlash(src)
	if strings.HasPrefix(slash, "/") || filepath.IsAbs(src) || filepath.VolumeName(src) != "" {
		return fmt.Errorf("src must be relative to the repository root: %q", slash)
	}
	depth := 0
	for part := range strings.SplitSeq(slash, "/") {
		switch part {
		case "", ".":
			continue
		case "..":
			depth--
			if depth < 0 {
				return fmt.Errorf("src escapes the repository root: %q", slash)
			}
		default:
			depth++
		}
	}
	return nil
}

// maxRegistryBytes is far above any real bench.toml (the largest in this tree is
// a few kilobytes) and far below what makes the parser's superlinear cost felt.
const maxRegistryBytes = 4 << 20 // 4 MiB

// maxRegistryDepth bounds bracket and inline-table nesting in a registry. Parse
// cost is superlinear in depth and the size cap does not bound it, so depth is
// checked on the raw text before Decode runs.
//
// The scan is a TOML lexer state machine, not a byte count: a byte-level counter
// is desynchronised by braces inside """...""" or an apostrophe inside a comment,
// so it reads real nesting as depth 0 and refuses valid registries. Tracking
// strings, multi-line forms and comments makes the depth the parser will see.
const maxRegistryDepth = 32

// checkRegistryDepth reports an error when the registry nests brackets or inline
// tables deeper than maxRegistryDepth.
func checkRegistryDepth(raw string) error {
	depth, deepest := 0, 0
	var (
		basic, literal, mlBasic, mlLiteral bool
		comment                            bool
	)
	for i := 0; i < len(raw); i++ {
		c := raw[i]
		if comment {
			if c == '\n' {
				comment = false
			}
			continue
		}
		if mlBasic {
			if c == '\\' {
				i++ // skip the escaped byte, including an escaped quote
			} else if c == '"' && i+2 < len(raw) && raw[i+1] == '"' && raw[i+2] == '"' {
				mlBasic = false
				i += 2
			}
			continue
		}
		if mlLiteral {
			if c == '\'' && i+2 < len(raw) && raw[i+1] == '\'' && raw[i+2] == '\'' {
				mlLiteral = false
				i += 2
			}
			continue
		}
		if basic {
			switch c {
			case '\\':
				i++
			case '"':
				basic = false
			}
			continue
		}
		if literal {
			if c == '\'' {
				literal = false
			}
			continue
		}
		switch {
		case c == '#':
			comment = true
		case c == '"' && i+2 < len(raw) && raw[i+1] == '"' && raw[i+2] == '"':
			mlBasic = true
			i += 2
		case c == '\'' && i+2 < len(raw) && raw[i+1] == '\'' && raw[i+2] == '\'':
			mlLiteral = true
			i += 2
		case c == '"':
			basic = true
		case c == '\'':
			literal = true
		case c == '[' || c == '{':
			depth++
			if depth > deepest {
				deepest = depth
			}
		case c == ']' || c == '}':
			if depth > 0 {
				depth--
			}
		}
	}
	if deepest > maxRegistryDepth {
		return fmt.Errorf("nested %d deep, over the %d-level limit", deepest, maxRegistryDepth)
	}
	return nil
}

// decodeRegistry reads one contest's bench.toml, returning the decoded document
// and the raw text, because time limits are read from the raw comments.
func decodeRegistry(path string) (map[string]any, string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return nil, "", LoadErrorf("cannot read registry %s: %v", path, err)
	}
	// Refused rather than truncated: a registry cut short is not the registry the
	// author wrote.
	if info.Size() > maxRegistryBytes {
		return nil, "", LoadErrorf(
			"registry %s is %d bytes, over the %d-byte limit",
			filepath.ToSlash(path), info.Size(), maxRegistryBytes)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, "", LoadErrorf("cannot read registry %s: %v", path, err)
	}
	// Checked before the parser sees the text, since the parse is where the cost
	// is paid.
	if err := checkRegistryDepth(string(raw)); err != nil {
		return nil, "", LoadErrorf("registry %s is %v", filepath.ToSlash(path), err)
	}
	var document map[string]any
	if _, err := toml.Decode(string(raw), &document); err != nil {
		return nil, "", LoadErrorf("invalid TOML in %s: %v", path, err)
	}
	return document, string(raw), nil
}

// loadContest registers every table in one contest's registry.
func loadContest(path, contest string, problems map[string]Problem, origin map[string]string) error {
	document, raw, err := decodeRegistry(path)
	if err != nil {
		return err
	}

	limits := timeLimits(raw)
	refs := filepath.Join(filepath.Dir(path), "refs")
	// No problem tables means nothing registers, so the contest never enters
	// CheckTree's owned set and its coverage is never audited.
	if len(document) == 0 {
		return LoadErrorf("%s: registry must contain problem tables", filepath.ToSlash(path))
	}
	// `[gen.*]` holds registry-declared generators, not problems, so it is split
	// out before the problem tables are read.
	if raw, declared := document["gen"]; declared {
		tables, ok := raw.(map[string]any)
		if !ok {
			return LoadErrorf("%s: gen must be a table of generator declarations",
				filepath.ToSlash(path))
		}
		if err := lookups.LoadGenerators(contest, path, tables); err != nil {
			return err
		}
		delete(document, "gen")
	}
	for _, name := range slices.Sorted(maps.Keys(document)) {
		values, ok := document[name].(map[string]any)
		id := contest + name
		if !ok {
			return LoadErrorf("%s: expected a TOML table", id)
		}
		if err := loadProblem(id, contest, name, values, refs, limits, problems, origin, path); err != nil {
			return err
		}
	}
	return nil
}

// loadProblem validates one table and records it. The checks keep their order --
// id shape, clash, reference-derived shape, src, key set, values, stale, maxchk
// -- because the first refusal is the message an author actually sees.
//
// id, contest and name are passed in from loadContest rather than re-derived, so
// a Problem's ID and Src cannot disagree.
func loadProblem(id, contest, name string, values map[string]any, refs string, limits map[string]string,
	problems map[string]Problem, origin map[string]string, path string) error {

	match := idRe.FindStringSubmatch(id)
	if match == nil {
		return LoadErrorf("%s: cannot derive src from problem id", text.Text(id))
	}
	if _, clash := problems[id]; clash {
		return LoadErrorf("%s: two config tables produce this id: %s and %s",
			id, origin[id], filepath.ToSlash(path))
	}

	// The shape is derived from which reference file exists, so a problem with
	// neither is timed only.
	shape := ShapeBench
	switch {
	case IsFile(filepath.Join(refs, id+".verify.cpp")):
		shape = ShapeVerify
	case IsFile(filepath.Join(refs, id+".cpp")):
		shape = ShapeCheck
	}

	src := contest + "/" + name + ".cpp"
	if raw, present := values["src"]; present {
		value, ok := raw.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return LoadErrorf("%s: src must be a non-empty string", id)
		}
		if err := CheckSrcConfined(value); err != nil {
			return LoadErrorf("%s: %v", id, err)
		}
		src = value
	}

	// The declared keys as a set. The refusals below name the lowest-sorting
	// offender, so no particular iteration order is needed here.
	declared := make(map[string]bool, len(values))
	for key := range values {
		declared[key] = true
	}
	names := slices.Sorted(maps.Keys(values))
	if key := firstExtra(names, ShapeAllKeys()); key != "" {
		return LoadErrorf("%s: unknown key %s", text.Text(id), text.Quote(key))
	}
	if key := firstExtra(names, shape.AllowedKeys()); key != "" {
		return LoadErrorf("%s: key %s is forbidden for %s", text.Text(id), text.Quote(key), text.Text(shape.String()))
	}
	if key := firstExtra(shape.RequiredKeys(), names); key != "" {
		return LoadErrorf("%s: missing required key %s", text.Text(id), text.Quote(key))
	}

	texts := map[string]string{}
	for _, key := range []string{"gen", "maxgen", "expect"} {
		if !declared[key] {
			continue
		}
		value, ok := values[key].(string)
		if !ok || strings.TrimSpace(value) == "" {
			return LoadErrorf("%s: %s must be a non-empty string", id, key)
		}
		texts[key] = value
	}
	// maxgen is optional and inherits twice over.
	//
	// An omitted maxgen takes gen's value outright: a problem whose sweep already
	// covers its ceiling should declare that once, not twice. A maxgen with no kind
	// word borrows only the kind, so `"%S 500 100"` under `gen = "ints %S 12 7 1 6"`
	// runs as `ints %S 500 100`. The first word counts as a kind only when it is in
	// the table; anything else is a parameter list.
	if gen, has := texts["gen"]; has {
		switch maxgen, present := texts["maxgen"]; {
		case !present:
			texts["maxgen"] = gen
		case !startsWithKind(maxgen):
			texts["maxgen"] = inheritSpec(gen, maxgen)
		}
	} else if maxgen, has := texts["maxgen"]; has && !startsWithKind(maxgen) {
		// Nothing to inherit from: a problem with no `gen` has no kind for its
		// maxgen to borrow, and letting it through would fail at run time with
		// "unknown generator kind" naming the seed.
		return LoadErrorf("%s: maxgen must name a generator kind when there is no gen", id)
	}

	if expect, has := texts["expect"]; has {
		if err := expectFields(texts["gen"], expect); err != nil {
			return err
		}
	}

	stale, err := readStale(id, values, declared)
	if err != nil {
		return err
	}
	maxchk := false
	if declared["maxchk"] {
		flag, ok := values["maxchk"].(bool)
		if !ok {
			return LoadErrorf("%s: maxchk must be a boolean", id)
		}
		maxchk = flag
	}
	// A maxgen that nothing runs is a claim the harness never checks. Only `bench`
	// reads it unconditionally (it times the worst case) and only `maxchk` asks for
	// it otherwise, so a check or verify table may write one only when it opts in.
	// Without this, the key reads as a tested ceiling while no case ever reaches it.
	if declared["maxgen"] && shape != ShapeBench && !maxchk {
		return LoadErrorf("%s: maxgen is never run without maxchk = true "+
			"(the sweep covers gen; drop maxgen or opt into the ceiling)", id)
	}

	problems[id] = Problem{
		ID: id, Src: src, Gen: texts["gen"], MaxGen: texts["maxgen"],
		Expect: texts["expect"], Stale: stale, MaxChk: maxchk,
		Shape: shape, Contest: contest, TL: limits[name],
		Registry: filepath.ToSlash(path),
	}
	origin[id] = filepath.ToSlash(path)
	return nil
}

// startsWithKind reports whether a spec's first word names a registered generator
// kind, which is what tells an independent spec from one that inherits.
func startsWithKind(spec string) bool {
	words := strings.Fields(spec)
	if len(words) == 0 {
		return false
	}
	_, known := lookups.BuiltinFields(words[0])
	return known
}

// inheritSpec prefixes an inheriting maxgen spec with the kind its gen spec names,
// so `maxgen = "%S 500 100"` under `gen = "ints %S 12 7 1 6"` runs as
// `ints %S 500 100`. With no kind word of its own, the spec's first word is the
// seed slot, which the generator replaces anyway.
func inheritSpec(gen, maxgen string) string {
	words := strings.Fields(gen)
	if len(words) == 0 {
		return maxgen
	}
	return words[0] + " " + maxgen
}

func readStale(id string, values map[string]any, declared map[string]bool) ([]string, error) {
	if !declared["stale"] {
		return nil, nil
	}
	entries, ok := values["stale"].([]any)
	if !ok || len(entries) == 0 {
		return nil, LoadErrorf("%s: stale must be a non-empty list of non-empty strings", id)
	}
	stale := make([]string, 0, len(entries))
	for _, entry := range entries {
		value, ok := entry.(string)
		if !ok || strings.TrimSpace(value) == "" {
			return nil, LoadErrorf("%s: stale must be a non-empty list of non-empty strings", id)
		}
		stale = append(stale, value)
	}
	return stale, nil
}

// firstExtra reports the lowest-sorting name in names that permitted does not
// hold, so the reported key is deterministic: an error naming a different key on
// each run is not actionable.
func firstExtra(names, permitted []string) string {
	allowed := make(map[string]bool, len(permitted))
	for _, name := range permitted {
		allowed[name] = true
	}
	first := ""
	for _, name := range names {
		if !allowed[name] && (first == "" || name < first) {
			first = name
		}
	}
	return first
}

// SortedIDs is the id order every listing and every gate walks.
func SortedIDs(problems map[string]Problem) []string {
	return slices.Sorted(maps.Keys(problems))
}

// IsFile reports whether path names a regular file.
func IsFile(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}
