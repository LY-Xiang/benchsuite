package main

import (
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/LY-Xiang/benchsuite/src/bench"
	"github.com/LY-Xiang/benchsuite/src/bench/mem"
	"github.com/LY-Xiang/benchsuite/src/gate"
	"github.com/LY-Xiang/benchsuite/src/gen"
	"github.com/LY-Xiang/benchsuite/src/registry"
	"github.com/LY-Xiang/benchsuite/src/report"
	"github.com/LY-Xiang/benchsuite/src/text"
)

// run is the whole CLI and returns the process exit code.
func run(gens *gen.Registry, args []string) int {
	// Notes are drained before the final flush. Defers run last-in-first-out, so
	// registering the flush first makes it run second.
	defer flushStdout()

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
		if refusal, ok := errors.AsType[*registry.Gate](err); ok {
			return fail("gate %s: %s", text.Text(refusal.ID), text.Text(refusal.Detail))
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

	env := NewEnv(root, gens)
	defer env.Close()
	defer flushNotes(env)
	if opts.wantBench {
		return runBenchMode(env, chosen, opts.timeout)
	}
	return reportResults(collectResults(env, chosen, opts), opts.junit)
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
		fmt.Sprintf("%s\t%s\t%s\t%s",
			text.Text(p.ID), text.Text(p.Shape.String()), text.Text(p.Src),
			text.Text(timeLimitLabel(p))), " \t",
	)
}

// timeLimitLabel renders the display-only time limit for the --list column. The
// label lives in report so the column and a --bench row cannot spell it two ways.
func timeLimitLabel(p registry.Problem) string {
	if p.TL == "" {
		return ""
	}
	return "TL " + p.TL
}

// RunBench times one problem's solution against the empty-program launch floor,
// always on the problem's largest input (MaxGen), since a timing row is about the
// advertised worst case. The returned Row is usable only when err is nil;
// otherwise err says which step refused, because a bare "could not be measured"
// leaves an author with no idea whether to fix the source, the registry or the
// machine.
//
// timeout bounds each timed execution, so `--timeout` can tighten a run that would
// otherwise sit at the default for its whole trial. Zero keeps the default.
func (e *Env) RunBench(p registry.Problem, timeout time.Duration) (bench.Row, error) {
	work, err := os.MkdirTemp("", "bench-time-"+p.ID+"-*")
	if err != nil {
		return bench.Row{}, fmt.Errorf("cannot create a work directory: %w", err)
	}
	defer func() { _ = os.RemoveAll(work) }()

	// The same preparation the gate runner does: a case that would fail a gate is
	// not one whose timing means anything. The two halves are called separately so
	// the refusal can say which one it was -- an author reads "did not build"
	// differently from "produced no usable input".
	solution, failed := e.gates.BuildSolution(p)
	if failed != nil {
		return bench.Row{}, fmt.Errorf("solution did not build: %s", text.Text(failed.Detail))
	}
	prepared, failed := e.gates.GenerateInput(p, work, p.MaxGen, 1)
	if failed != nil {
		return bench.Row{}, fmt.Errorf("generator produced no usable input: %s", text.Text(failed.Detail))
	}
	input := prepared.Input

	// The launch floor is the same empty program for every problem, so it is
	// compiled once per process and reused; only the timing is per problem. Its
	// build cost used to be paid again for each row of a sweep.
	nop, err := e.LaunchFloor()
	if err != nil {
		return bench.Row{}, err
	}

	reading, err := bench.Measure([]string{solution}, input, nop, timeout,
		mem.Observers()...)
	if err != nil {
		return bench.Row{}, err
	}
	return bench.Row{
		Problem: p.ID, Shape: p.Shape, InputBytes: len(input),
		Limit:   timeLimitLabel(p),
		Reading: reading,
	}, nil
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
		reportf("%s\n", report.BenchRow(row))
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
func collectResults(env *Env, chosen []registry.Problem, opts options) []gate.Result {
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

	results := make([]gate.Result, len(jobs))
	workers := min(opts.jobs, len(jobs))
	if workers <= 1 {
		for i, job := range jobs {
			results[i] = env.gates.RunCase(job.problem, job.spec, job.seed, opts.timeout)
		}
		return results
	}

	work := make(chan int)
	var wg sync.WaitGroup
	for range workers {
		wg.Go(func() {
			for i := range work {
				job := jobs[i]
				results[i] = env.gates.RunCase(job.problem, job.spec, job.seed, opts.timeout)
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
func reportResults(results []gate.Result, junit string) int {
	failed := 0
	for _, result := range results {
		reportf("%s\n", report.ResultLine(result))
		if !result.Verdict.Passed() {
			failed++
		}
	}
	reportf("\n%d/%d passed\n", len(results)-failed, len(results))

	if junit != "" {
		if err := report.JUnit(junit, results); err != nil {
			return fail("cannot write %s: %v", junit, err)
		}
	}
	if failed > 0 {
		return exitFail
	}
	return exitOK
}
