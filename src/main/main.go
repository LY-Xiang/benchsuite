// Command bench runs the CodeForces benchmark harness: it generates an input from
// a problem's generator, re-derives the claim from the bytes, runs the solution
// and the reference, and reports which gate the pair passes. Every message
// carries the `bench.py: ` prefix, which downstream greps match on.
package main

import (
	"bufio"
	"encoding/xml"
	"errors"
	"fmt"
	"os"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"lyxiang/benchsuite/src/registry"
	"lyxiang/benchsuite/src/text"
)

// msgPrefix begins every message the CLI prints, on stdout and stderr alike.
const msgPrefix = "bench.py: "

// Exit codes: success, a case failed, or a usage/registry/report error.
const (
	exitOK    = 0
	exitFail  = 1
	exitError = 2
)

// Bench mode takes the best of benchTrials trials of benchRuns runs each.
const (
	benchTrials = 3
	benchRuns   = 10
)

// options is one parsed command line, written once by parseArgs and read after.
type options struct {
	wantList  bool
	wantAll   bool
	wantBench bool
	shapes    []string
	// selection holds the positional arguments, still in argv order and form.
	selection selection
	filter    *regexp.Regexp
	junit     string
	reps      int
	timeout   time.Duration
	jobs      int
}

// repoRoot is the checkout the CLI runs against: the working directory.
func repoRoot() string {
	cwd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return cwd
}

func main() {
	// Before any table is read: a load needs to know which generator kinds exist,
	// and the registry gets that answer from here.
	installLookups()
	os.Exit(run(os.Args[1:]))
}

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

// usageError is a command-line error, already phrased for the user.
type usageError struct{ msg string }

func (e *usageError) Error() string { return e.msg }

func usagef(format string, args ...any) error {
	return &usageError{msg: fmt.Sprintf(format, args...)}
}

// run is the whole CLI and returns the process exit code.
func run(args []string) int {
	// Notes are drained before the final flush. Defers run last-in-first-out, so
	// registering the flush first makes it run second.
	defer flushStdout()
	defer flushBuildNotes()

	opts, err := parseArgs(args)
	if err != nil {
		return fail("%s", err)
	}

	// --all plus explicit ids is contradictory. Shape and id-regex filters are
	// compatible: they narrow the full set, and an empty result exits zero.
	if opts.wantAll && len(opts.selection.raw) > 0 {
		return fail("--all cannot be combined with problem ids")
	}

	root := repoRoot()
	// An opaque id is only resolvable against the whole tree, so one of them
	// turns narrowing off for the entire command line.
	narrowed := opts.selection.narrow()
	contests, whole, wantedIDs := opts.selection.resolve(narrowed)
	if !narrowed {
		contests = nil
	}
	problems, err := registry.LoadAll(root, contests...)
	if err != nil {
		if gate, ok := errors.AsType[*registry.Gate](err); ok {
			return fail("gate %s: %s", text.Text(gate.ID), text.Text(gate.Detail))
		}
		return fail("%s", text.Text(err.Error()))
	}

	// Whole-contest arguments become the ids that contest actually holds. They are
	// unioned with any individually named problems, so `2268 2269/A` selects all of
	// 2268 plus one problem from 2269 -- the approved mixed form.
	wantedIDs = append(wantedIDs, expandContest(whole, problems)...)
	chosen, err := selectProblems(problems, opts.shapes, opts.filter, wantedIDs)
	if err != nil {
		return fail("%s", text.Text(err.Error()))
	}

	if opts.wantList {
		return runList(chosen)
	}
	if len(chosen) == 0 {
		reportf("%sno problems selected\n", msgPrefix)
		return exitOK
	}

	env := NewEnv(root)
	defer env.closeLaunchFloor()
	if opts.wantBench {
		return runBenchMode(env, chosen, opts.timeout)
	}
	return reportResults(collectResults(env, chosen, opts), opts.junit)
}

// parseArgs reads the whole command line. The returned error is a usage error:
// run prints it and exits 2. A value-taking flag never reads past its value.
func parseArgs(args []string) (options, error) {
	opts := options{reps: 1, jobs: defaultJobs()}

	for i := 0; i < len(args); i++ {
		arg := args[i]
		switch arg {
		case "--list":
			opts.wantList = true
		case "--all":
			opts.wantAll = true
		case "--bench":
			opts.wantBench = true
		case "-L", "-R", "--reps", "--timeout", "--junit", "--jobs":
			if i+1 >= len(args) {
				return options{}, usagef("%s needs a value", arg)
			}
			i++
			if err := opts.setValueFlag(arg, args[i]); err != nil {
				return options{}, err
			}
		default:
			// A bare `-` is a positional id, not an option.
			if strings.HasPrefix(arg, "-") && arg != "-" {
				return options{}, usagef("unrecognised option '%s'", arg)
			}
			opts.selection.add(arg)
		}
	}
	return opts, nil
}

// setValueFlag applies one value-taking flag. Only -L accumulates; a repeated -R,
// --reps, --timeout or --junit overwrites.
func (o *options) setValueFlag(flag, value string) error {
	switch flag {
	case "-L":
		if value != "check" && value != "verify" && value != "bench" {
			return usagef("unknown shape '%s'", value)
		}
		o.shapes = append(o.shapes, value)
	case "-R":
		filter, err := regexp.Compile(value)
		if err != nil {
			return usagef("bad -R pattern: %v", err)
		}
		o.filter = filter
	case "--reps":
		n, err := parseCount(value)
		if err != nil {
			return usagef("--reps must be a positive integer")
		}
		o.reps = n
	case "--timeout":
		seconds, err := strconv.ParseFloat(value, 64)
		if err != nil {
			return usagef("--timeout must be a number")
		}
		o.timeout = time.Duration(seconds * float64(time.Second))
	case "--junit":
		o.junit = value
	case "--jobs":
		n, err := parseCount(value)
		if err != nil {
			return usagef("--jobs must be a positive integer")
		}
		o.jobs = n
	}
	return nil
}

// defaultJobs is the worker count a sweep uses when --jobs is not given. Check
// cases are independent, so the pool is bounded by the cores rather than by the
// case count; the cap keeps a machine with many cores from starting a compiler
// per core, which is where peak memory would come from.
func defaultJobs() int {
	return min(runtime.GOMAXPROCS(0), 8)
}

// selection is what the positional arguments asked for. Three spellings are
// accepted:
//
//	2269    a contest    every problem it registers
//	2269/A  one problem  problem 2269A
//	2267C   a legacy id  problem 2267C
//
// raw keeps every argument verbatim, duplicates included: an unrecognised
// argument must reach the registry unchanged, so the unknown-id message echoes
// what the user typed.
type selection struct {
	raw []string
}

// add records one positional argument exactly as typed.
func (s *selection) add(arg string) {
	s.raw = append(s.raw, arg)
}

// selector reports whether arg is an approved spelling. Only these narrow the
// load; anything else is an opaque id (see narrow).
func selector(arg string) bool {
	if isAllDigits(arg) {
		return true
	}
	_, _, ok := splitSelector(arg)
	return ok
}

// splitSelector parses the approved problem form `2269/A`: an all-digit contest
// and a name starting with an upper-case letter, the suffix registry ids use.
//
// The strictness keeps messages honest: `2269//A`, `2269/a` and `2269/` are not
// selectors, so they are echoed back as typed instead of rewritten into an id the
// user never mentioned.
func splitSelector(arg string) (contest, problem string, ok bool) {
	contest, problem, found := strings.Cut(arg, "/")
	if !found || !isAllDigits(contest) {
		return "", "", false
	}
	if problem == "" || problem[0] < 'A' || problem[0] > 'Z' {
		return "", "", false
	}
	for i := 1; i < len(problem); i++ {
		c := problem[i]
		if (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') {
			continue
		}
		return "", "", false
	}
	return contest, problem, true
}

// narrow reports whether every positional is an approved selector.
//
// A legacy id resolves only against the whole tree, so one of them turns
// narrowing off for the command. Otherwise `2267C 2269/A` would load contest 2269
// and report 2267C as unknown -- a problem the same line finds on its own.
func (s *selection) narrow() bool {
	for _, arg := range s.raw {
		if !selector(arg) {
			return false
		}
	}
	return true
}

// resolve splits the positionals into the contest directories to load, the
// contests requested whole, and the problem ids to keep. An opaque argument stays
// verbatim, duplicates included: selectProblems echoes its unknown list from it.
func (s *selection) resolve(narrowed bool) (contests, whole, problems []string) {
	for _, arg := range s.raw {
		if contest, problem, ok := splitSelector(arg); ok {
			contests = append(contests, contest)
			problems = append(problems, contest+problem)
			continue
		}
		// A bare contest counts only when the load can be narrowed to it. Left
		// opaque, the name reaches selectProblems and is reported unknown -- which
		// is what stops `--list 9999 2267C` from silently listing 2267C.
		if narrowed && isAllDigits(arg) {
			contests = append(contests, arg)
			whole = append(whole, arg)
			continue
		}
		problems = append(problems, arg)
	}
	return contests, whole, problems
}

// expandContest turns whole-contest arguments into the ids that contest
// registers, which is only knowable after loading. A contest with no registry
// never reaches here: the loader refuses it by name first.
func expandContest(whole []string, problems map[string]registry.Problem) []string {
	if len(whole) == 0 {
		return nil
	}
	wanted := make(map[string]bool, len(whole))
	for _, contest := range whole {
		wanted[contest] = true
	}
	var ids []string
	for _, id := range registry.SortedIDs(problems) {
		if wanted[contestOf(id)] {
			ids = append(ids, id)
		}
	}
	return ids
}

// contestOf is the contest part of an id: its leading digits.
func contestOf(id string) string {
	end := 0
	for end < len(id) && id[end] >= '0' && id[end] <= '9' {
		end++
	}
	if end == 0 {
		return id
	}
	return id[:end]
}

// isAllDigits reports whether s is a non-empty run of ASCII digits.
func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// runList prints one row per selected problem and exits 0.
func runList(chosen []registry.Problem) int {
	for _, problem := range chosen {
		reportf("%s\n", listLine(problem))
	}
	return exitOK
}

// listLine is one --list row: id, shape, source, and the display-only time limit.
// Trailing empty columns are trimmed.
func listLine(p registry.Problem) string {
	return strings.TrimRight(
		fmt.Sprintf("%s\t%s\t%s\t%s", text.Text(p.ID), text.Text(p.Shape), text.Text(p.Src), text.Text(timeLimitLabel(p))), " \t",
	)
}

// timeLimitLabel renders the display-only time limit, if any.
func timeLimitLabel(p registry.Problem) string {
	if p.TL == "" {
		return ""
	}
	return "TL " + p.TL
}

// runBenchMode measures each selected problem and prints a row, or a refusal.
//
// Timing runs stay serial: the row is a measurement, and a worker pool competing
// for cores would report each other's scheduling jitter as the solution's cost.
func runBenchMode(env *Env, chosen []registry.Problem, timeout time.Duration) int {
	failed := false
	for _, problem := range chosen {
		row, err := env.RunBench(problem, timeout)
		if err != nil {
			// The reason names the step that refused, so an author can tell a
			// source that does not compile from a registry whose generator
			// produces an input the solution cannot read.
			reportf("FAIL      %s gate=-   timing refused: %s\n", problem.ID, text.Text(err.Error()))
			failed = true
			continue
		}
		reportf("%s\n", FormatBenchRow(row))
	}
	if failed {
		return exitFail
	}
	return exitOK
}

// collectResults runs every selected problem at every rep, plus the extra specs
// a problem asks for. Everything runs before anything is reported, so the summary
// is the last line printed.
//
// Cases are independent, so they run across a bounded worker pool. Each case
// writes its own result slot, so the report keeps the order a serial sweep would
// have printed -- id order, then reps, then the largest-input and stale checks --
// no matter which worker finished first.
func collectResults(env *Env, chosen []registry.Problem, opts options) []Result {
	type caseJob struct {
		problem registry.Problem
		spec    string
		seed    int
	}

	var jobs []caseJob
	for _, problem := range chosen {
		spec := problem.Gen
		if spec == "" {
			spec = problem.MaxGen
		}
		// The sweep, then the largest input when `maxchk` is set, then each
		// `stale` spec under two seeds.
		for rep := 1; rep <= opts.reps; rep++ {
			jobs = append(jobs, caseJob{problem, spec, rep})
		}
		if problem.MaxChk {
			jobs = append(jobs, caseJob{problem, problem.MaxGen, 1})
		}
		for _, stale := range problem.Stale {
			// Seeds continue past the sweep's own, so a stale spec that repeats the
			// sweep spec does not regenerate an input the reps already produced.
			for _, seed := range []int{opts.reps + 1, opts.reps + 2} {
				jobs = append(jobs, caseJob{problem, stale, seed})
			}
		}
	}

	results := make([]Result, len(jobs))
	workers := min(opts.jobs, len(jobs))
	if workers <= 1 {
		for i, job := range jobs {
			results[i] = env.RunCase(job.problem, job.spec, job.seed, opts.timeout)
		}
		return results
	}

	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range work {
				job := jobs[i]
				results[i] = env.RunCase(job.problem, job.spec, job.seed, opts.timeout)
			}
		})
	}
	for i := range jobs {
		work <- i
	}
	close(work)
	wg.Wait()
	return results
}

// reportResults prints one row per result, the pass summary, and optionally a
// JUnit report, then returns the process exit code.
func reportResults(results []Result, junit string) int {
	failed := 0
	for _, result := range results {
		reportf("%s\n", resultLine(result))
		if !passed(result.Kind) {
			failed++
		}
	}
	reportf("\n%d/%d passed\n", len(results)-failed, len(results))

	if junit != "" {
		if err := writeJUnit(junit, results); err != nil {
			return fail("cannot write %s: %v", junit, err)
		}
	}
	if failed > 0 {
		return exitFail
	}
	return exitOK
}

// passed reports whether a case counts as a pass: PASS and UNSUPPORTED do;
// TIMEOUT and every FAIL do not.
func passed(kind string) bool { return kind == "PASS" || kind == "UNSUPPORTED" }

// resultLine is one reported case row.
func resultLine(r Result) string {
	gate := r.GateID
	if gate == "" {
		gate = "-"
	}
	return strings.TrimRight(
		fmt.Sprintf("%-9s %s gate=%-3s %s", r.Kind, r.Problem, gate, r.Detail), " ",
	)
}

// parseCount accepts ASCII digits only: a count that reads as a number in one
// encoding and is rejected in another is a trap, so the narrow rule is stated.
func parseCount(value string) (int, error) {
	if value == "" {
		return 0, strconv.ErrSyntax
	}
	for _, r := range value {
		if r < '0' || r > '9' {
			return 0, strconv.ErrSyntax
		}
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < 1 {
		return 0, strconv.ErrSyntax
	}
	return n, nil
}

// selectProblems applies -L/-R/positional selection and returns the chosen
// problems in id order.
func selectProblems(problems map[string]registry.Problem, shapes []string, filter *regexp.Regexp, ids []string) ([]registry.Problem, error) {
	wanted := make(map[string]bool, len(ids))
	for _, id := range ids {
		wanted[id] = true
	}

	var chosen []registry.Problem
	for _, id := range registry.SortedIDs(problems) {
		problem := problems[id]
		if len(shapes) > 0 && !allShapes(problem.Shape, shapes) {
			continue
		}
		// -R is a search, not an anchored match.
		if filter != nil && !filter.MatchString(id) {
			continue
		}
		if len(ids) > 0 && !wanted[id] {
			continue
		}
		chosen = append(chosen, problem)
	}

	var unknown []string
	for _, id := range ids {
		if _, ok := problems[id]; !ok {
			unknown = append(unknown, id)
		}
	}
	if len(unknown) > 0 {
		return chosen, fmt.Errorf("unknown problem id(s): %s", strings.Join(unknown, ", "))
	}
	return chosen, nil
}

// allShapes ANDs the requested shapes: a problem has exactly one, so asking for
// two different ones selects nothing. That is deliberate -- a typo narrowing the
// sweep to nothing is visible, where an OR would quietly run everything.
func allShapes(shape string, wanted []string) bool {
	for _, want := range wanted {
		if shape != want {
			return false
		}
	}
	return true
}

type junitFailure struct {
	XMLName xml.Name `xml:"failure"`
	Message string   `xml:"message,attr"`
	Text    string   `xml:",chardata"`
}

type junitCase struct {
	XMLName   xml.Name      `xml:"testcase"`
	Name      string        `xml:"name,attr"`
	Classname string        `xml:"classname,attr"`
	Failure   *junitFailure `xml:"failure,omitempty"`
}

type junitSuite struct {
	XMLName xml.Name    `xml:"testsuite"`
	Name    string      `xml:"name,attr"`
	Tests   int         `xml:"tests,attr"`
	Cases   []junitCase `xml:"testcase"`
}

// shapeClass maps the reference's own shape word to the registry vocabulary, so a
// JUnit consumer sees the same shape names. "none" is the "bench" shape.
func shapeClass(shape string) string {
	switch shape {
	case "compare":
		return "check"
	case "grade":
		return "verify"
	case "", "none":
		return "bench"
	}
	return shape
}

// writeJUnit records one testcase per result. The close error is returned when
// nothing earlier failed: a report that did not flush is not a report.
func writeJUnit(path string, results []Result) (err error) {
	suite := junitSuite{Name: "bench", Tests: len(results)}
	for _, result := range results {
		entry := junitCase{Name: result.Problem, Classname: shapeClass(result.Shape)}
		if !passed(result.Kind) {
			entry.Failure = &junitFailure{
				Message: strings.TrimSpace("gate " + result.GateID),
				Text:    result.Detail,
			}
		}
		suite.Cases = append(suite.Cases, entry)
	}

	file, err := os.Create(path)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := file.Close(); err == nil {
			err = closeErr
		}
	}()

	body, err := xml.MarshalIndent(suite, "", "  ")
	if err != nil {
		return err
	}
	_, err = file.WriteString(xml.Header + string(body) + "\n")
	return err
}
