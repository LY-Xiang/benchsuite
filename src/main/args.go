package main

import (
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/LY-Xiang/benchsuite/src/registry"
)

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
		// The closed set decides what is a shape, so a spelling it does not contain
		// is a usage error rather than a filter that silently selects nothing.
		shape, ok := registry.ParseShape(value)
		if !ok {
			return usagef("unknown shape '%s'", value)
		}
		o.shapes = append(o.shapes, shape)
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
