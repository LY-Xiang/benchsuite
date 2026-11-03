#!/usr/bin/env python3
"""Capture the harness's behaviour so a refactor can be checked against it.

The deterministic modes (--list, the two fixture self-tests, and every gate row
of --all) are compared byte-for-byte. --bench is captured separately with its
numbers masked, because timings and memory readings vary between runs and only
the row shape is meaningful.

Usage:
    python bench/tools/capture.py <harness-binary> <repo-tree> <output-dir>
    python bench/tools/capture.py --diff <before-dir> <after-dir>
"""

import re
import subprocess
import sys
from pathlib import Path

# Only modes this harness actually implements. A capture of a mode that does not
# exist would record the same usage error on both sides and read as a pass.
MODES = [
    ("list", ["--list"]),
    ("list-check", ["-L", "check", "--list"]),
    ("list-bench", ["-L", "bench", "--list"]),
    ("all-reps1", ["--all", "--reps", "1"]),
    ("all-reps3", ["--all", "--reps", "3"]),
]

# Numbers that legitimately vary between runs of the same code.
UNSTABLE = [
    # The generator runs unseeded, so a spec whose size is drawn at random gives a
    # different input length on every run. Only the row shape is comparable.
    (re.compile(r"input=\d+B"), "input=<n>B"),
    # Work time can land either side of the floor between runs, so both forms
    # collapse to one token: the row is compared for shape, not for magnitude.
    (re.compile(r"wall=\d+us"), "wall=<us>"),
    (re.compile(r"work=<floor \(\d+ us\)>"), "work=<t>"),
    (re.compile(r"work=\d+us"), "work=<t>"),
    (re.compile(r"mem=[\d.]+(?:[KMG]i?B|B)"), "mem=<n>"),
    (re.compile(r"peak=[\d.]+(?:[KMG]i?B|B)"), "peak=<n>"),
    (re.compile(r"floor=[\d.]+(?:[KMG]i?B|B)"), "floor=<n>"),
]


def capture(binary: str, tree: str, out: Path) -> None:
    out.mkdir(parents=True, exist_ok=True)
    for name, argv in MODES:
        run = subprocess.run([binary, *argv], cwd=tree, capture_output=True, text=True)
        # A mode that fails is a mistake in this table, not a behaviour to record:
        # capturing it would make two broken runs compare equal. --all refuses on
        # a corpus error, so its exit code is checked here too.
        expect = 0 if name.startswith(("list", "all")) else run.returncode
        if run.returncode != expect:
            raise SystemExit(
                f"capture: {' '.join(argv)} exited {run.returncode}, expected {expect}\n"
                f"{(run.stdout + run.stderr).strip()[:400]}")
        body = run.stdout + run.stderr + f"exit={run.returncode}\n"
        (out / f"{name}.txt").write_text(body, encoding="utf-8", newline="\n")

    run = subprocess.run([binary, "--bench"], cwd=tree, capture_output=True, text=True)
    text = run.stdout + run.stderr + f"exit={run.returncode}\n"
    for pattern, replacement in UNSTABLE:
        text = pattern.sub(replacement, text)
    (out / "bench-shape.txt").write_text(text, encoding="utf-8", newline="\n")
    print(f"captured {len(MODES) + 1} files into {out}")


def diff(before: Path, after: Path) -> int:
    names = sorted({p.name for p in before.iterdir()} | {p.name for p in after.iterdir()})
    failed = 0
    for name in names:
        a, b = before / name, after / name
        if not a.exists() or not b.exists():
            print(f"MISSING  {name}: before={a.exists()} after={b.exists()}")
            failed += 1
            continue
        left, right = a.read_text(encoding="utf-8"), b.read_text(encoding="utf-8")
        if left == right:
            print(f"SAME     {name}")
            continue
        failed += 1
        print(f"DIFFERS  {name}")
        for i, (x, y) in enumerate(zip(left.splitlines(), right.splitlines())):
            if x != y:
                print(f"  first diff at line {i + 1}")
                print(f"  before: {x}")
                print(f"  after : {y}")
                break
        else:
            print(f"  line counts differ: before={len(left.splitlines())} after={len(right.splitlines())}")
    return 1 if failed else 0


def main(argv: list[str]) -> int:
    if len(argv) >= 4 and argv[1] == "--diff":
        return diff(Path(argv[2]), Path(argv[3]))
    if len(argv) != 4:
        print(__doc__.strip())
        return 2
    capture(argv[1], argv[2], Path(argv[3]))
    return 0


if __name__ == "__main__":
    sys.exit(main(sys.argv))
