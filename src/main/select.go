package main

import (
	"fmt"
	"regexp"
	"strconv"
	"strings"

	"github.com/LY-Xiang/benchsuite/src/registry"
)

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
func selectProblems(problems map[string]registry.Problem, shapes []registry.Shape,
	filter *regexp.Regexp, ids []string,
) ([]registry.Problem, error) {
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
func allShapes(shape registry.Shape, wanted []registry.Shape) bool {
	for _, want := range wanted {
		if shape != want {
			return false
		}
	}
	return true
}
