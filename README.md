# benchsuite

A harness for timing, checking and verifying algorithm solutions against
generated inputs.

It serves a **corpus**: a directory of contests, each holding solutions and a
`bench.toml` registry. The harness itself knows nothing about competitive
programming, C++ or any particular site — those live in the corpus. Point it at
any tree that follows the layout below and it works.

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

Flags: `-L check|verify|bench` (repeatable), `-R <regex>`, `--reps <n>`,
`--timeout <seconds>`, `--junit <path>`, `--list`, `--all`, `--bench`.

Exit codes: `0` success, `1` a case failed, `2` a usage or registry error.

## The binary cache

Each source is compiled to a binary beside it: `2267/A.cpp` yields `2267/A.exe`,
and a reference at `2267/refs/2267A.cpp` yields `2267/refs/2267A.exe`. Nothing
is built ahead of time -- the binary appears when it is first needed.

The registry records the hash of what was compiled, so a later run can tell
whether the binary still matches:

```toml
[A]
maxgen = "letters %S 25"
hash = { "2267/A.exe" = "3f2a...", "2267/refs/2267A.cpp" = "9c81..." }
```

The hash covers the source bytes and the compile flags, so editing either
rebuilds the binary. The binaries are not in version control; deleting one only
costs the next rebuild.

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

## Selection semantics

A refused timing always says which stage refused and why — a build that did not
produce a binary, a generator the solution cannot read, a child that exited
non-zero (with the code and the failing trial and run), or a run killed at the
deadline — instead of collapsing every cause into one message.
