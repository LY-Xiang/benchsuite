# benchsuite

A harness for timing, checking and verifying algorithm solutions against
generated inputs.

It serves a **corpus**: a directory of contests, each holding solutions and a
`bench.toml` registry. What a contest *is* lives in the corpus, not here: the
harness reads a directory layout and a registry format, and knows nothing about
competitive programming or any particular site. It does know how to compile and
run a solution, which is why the language-specific part is confined to two
places — the path conventions `registry` derives ids and reference names from,
and the compiler command `build` runs. Point it at any tree that follows the
layout below and it works.

## Corpus layout

```
<corpus>/
  2267/
    bench.toml          registry: one table per solution
    A.cpp               the solution
    refs/2267A.cpp      optional reference, makes the entry checkable
```

Contest directories are numeric; a table `[A]` under `2267/` is problem `2267A`.
A `bench.toml` must register every `.cpp` its contest directory contains.

## Running

Build and run from the corpus root:

```
go build -o bench ./src/main          # run inside this repo
cd <corpus>
<path>/bench --bench                   # time every registered solution
<path>/bench --all                     # check every entry that has a reference
<path>/bench 2267 --bench              # one contest
<path>/bench 2267/A --bench            # one problem
<path>/bench -R '^226[0-9]B$' --bench  # regex selection
```

All timing is CPU-accounted as well as wall-clocked: a `--bench` row ends in
`cpu=<n>us`, the child's own user+system time per run. Wall time that `cpu=` does
not explain is time spent waiting to be scheduled.

The module path is `github.com/LY-Xiang/benchsuite`, so a corpus's
`[gen.<name>]` table imports `github.com/LY-Xiang/benchsuite/src/genlib` — that
import is resolved against a scratch module the harness writes at compile time,
not against the network.

`cpu=` comes from the OS's own per-process accounting — `GetProcessTimes` on
Windows, the `wait4` rusage on Linux and macOS — not from a cycle count divided by
a clock. A core's frequency moves during a run, so cycles converted at the
frequency observed at exit are not a duration; the OS's accounting is a duration by
construction.

The cost is resolution: that accounting is quantised to the scheduler's tick
(15.625 ms on Windows), so a solution shorter than one tick reports `cpu=0us`. For
a real workload the figure is within one tick of the truth, which is the scale a
benchmark asks about, and it is never inflated by a wrong clock.

Flags: `-L check|verify|bench` (repeatable), `-R <regex>`, `--reps <n>`,
`--timeout <seconds>`, `--jobs <n>`, `--junit <path>`, `--list`, `--all`,
`--bench`.

`--jobs` bounds how many check cases run at once (default: the cores available,
capped at 8); `--jobs 1` is the serial sweep. Timing runs under `--bench` are
always serial and `--timeout` bounds each measured execution, so a slow solution
refuses its row at the deadline instead of running every trial.

Exit codes: `0` success, `1` a case failed, `2` a usage or registry error.

## The binary cache

Each source is compiled to a binary beside it: `2267/A.cpp` yields `2267/A.exe`,
and a reference at `2267/refs/2267A.cpp` yields `2267/refs/2267A.exe`. Nothing
is built ahead of time -- the binary appears when it is first needed.

The hash of what was compiled is recorded in `bench.lock`, beside the registry:

```
2267/A.exe 3f2a...
2267/refs/2267A.exe 9c81...
```

The hash covers the source bytes and the compile flags, so editing either
rebuilds the binary. The lock and the binaries are build state the harness
owns, not declarations a human maintains: both are gitignored, and deleting
either only costs the next rebuild. The registry stays purely declarative.

## The three shapes

An entry's shape follows from what it declares, and each shape gates a
different question:

| Shape | Requires | Answers |
|---|---|---|
| `bench` | `maxgen` | how long does it take on the largest input |
| `verify` | `gen` | does the reference agree with the declared input shape |
| `check` | `gen`, `expect` | does the solution agree with an independent oracle |

`maxgen` and `gen` name a generator: a built-in kind plus its arguments, or a
`[gen.<name>]` table pointing at a Go source that is compiled on demand. See
`src/genlib/kinds.go` for the built-in byte shapes.

No reference registered means no correctness claim. `--all` reports such entries
as `UNSUPPORTED` and counts them as a pass — the entry is simply not claiming
anything to check.

## A warning is not a failure

A compile that exits zero produced a binary. Compiler diagnostics are printed
under the results (`compiled with warnings: <source>`) rather than refusing the
entry, so a warning cannot silently remove a timing row.

## The source layout

`src/` is split so the file structure states the division of labour:

```
src/
  exec/        runs one child: deadline, capture ceilings, process-group kill
  build/       compiles solutions, references and generators; the binary cache
  gen/         turns a generator spec into an input and its claim
  gate/        runs a case through the gates, in order, and names the first refusal
  bench/       times a solution against the launch floor
    clock/     aggregates wall and CPU time into trials
    mem/       reads a child's peak resident set, per platform
  report/      renders result rows, timing rows and the JUnit report
  registry/    reads the corpus: ids, shapes, generator declarations
  genlib/      the built-in generator kinds and the claim grammar
  claim/       validates a claim against the bytes it describes
  text/        sanitises a value before it reaches a terminal
  main/        assembles the above and is the process entry
```

Each layer may import the ones below it and never the ones above. Two edges are
worth naming because they look like violations and are not: `exec` defines the
`Process` and `Observer` types that `bench/mem` builds on, so `exec` never imports
a measurement package — it opens one handle and hands it out, and each measurement
module reads what it knows how to read. And `bench` imports `exec` for those types
and `gen` for the generated bytes, but never `main`. Platform differences are
files with build tags (`build/suffix_*.go`, `bench/mem/mem_*.go`), never a
`runtime.GOOS` branch in shared code.

## Selection semantics

A refused timing always says which stage refused and why — a build that did not
produce a binary, a generator the solution cannot read, a child that exited
non-zero (with the code and the failing trial and run), or a run killed at the
deadline — instead of collapsing every cause into one message.
