package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"

	execpkg "lyxiang/benchsuite/src/exec"
	"lyxiang/benchsuite/src/genlib"
	"lyxiang/benchsuite/src/text"
)

// CompileTimeout bounds one compiler invocation. The per-case deadline lives in
// the exec package as execpkg.DefaultTimeout; a compile is separate because a
// legitimate -O3 compile outlasts a short case deadline.
const CompileTimeout = 5 * time.Minute

// Env carries everything one run needs: the checkout it works against, the
// compiler command, and the binary cache.
type Env struct {
	Root string   // repo root
	CXX  []string // shell-split compiler command
}

func NewEnv(root string) *Env {
	cxx := []string{"g++"}
	if raw := os.Getenv("CXX"); strings.TrimSpace(raw) != "" {
		cxx = shellSplit(raw)
	}
	return &Env{
		Root: root,
		CXX:  cxx,
	}
}

// binarySuffix is the executable extension this platform expects, which both the
// cache key and the per-case work files have to carry.
func binarySuffix() string {
	if runtime.GOOS == "windows" {
		return ".exe"
	}
	return ""
}

// compileTo compiles a source to an exact output path.
//
// The compiler writes to a temporary file beside the target and that file is
// renamed into place, so a concurrent run never executes a half-written binary.
// A source with no recorded hash -- a launch floor, a generator -- is compiled
// here rather than through the product cache.
func (e *Env) compileTo(src, out string, flags []string) (string, error) {
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return "", err
	}
	staged, err := os.CreateTemp(filepath.Dir(out), filepath.Base(out)+".tmp-*")
	if err != nil {
		return "", err
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	// The compiler can only open the staged file once it is closed, so a failed
	// close is a build failure. The remove above is only an attempt: on Windows
	// a failed close usually leaves the handle open and removal fails.
	if err := staged.Close(); err != nil {
		return "", err
	}

	argv := make([]string, 0, len(e.CXX)+len(flags)+3)
	argv = append(argv, e.CXX...)
	argv = append(argv, flags...)
	argv = append(argv, src, "-o", stagedPath)

	// Through RunProcess like any other child, so a wedged cc1plus is bounded and
	// its process group dies with it.
	result := execpkg.Run(argv, nil, false, CompileTimeout)
	if result.TimedOut || result.Code != 0 {
		return "", fmt.Errorf("compilation failed")
	}
	// A non-zero exit is the build failing; a warning is not. The compiler says
	// so on stderr without changing its status, and a solution that compiles and
	// runs correctly still has a timing to report -- refusing it would hide a real
	// measurement over a stylistic complaint. Stderr is kept instead of discarded:
	// it reaches the user through BuildNote, and the row still gets gated on the
	// binary actually running.
	if len(result.Stderr) > 0 {
		noteBuildOutput(src, argv, result.Stderr)
	}
	if err := os.Rename(stagedPath, out); err != nil {
		return "", err
	}
	return out, nil
}

// buildNotes collects compiler diagnostics for sources that nonetheless built. It
// is process-wide because Build is called from three places (solution, reference,
// generator) and none of them owns a reporting channel; the CLI drains it once at
// the end so the notes land after the rows they belong to.
var buildNotes []buildNote

type buildNote struct {
	src  string
	argv []string
	out  []byte
}

// noteBuildOutput records a build that succeeded with diagnostics on stderr.
//
// Warnings are not failures: the compiler exited zero, so the binary exists and
// runs. Refusing the build would turn a stylistic complaint into a missing
// measurement, which is the harness inventing a failure the compiler did not
// report. The note keeps the text instead of dropping it.
func noteBuildOutput(src string, argv []string, stderr []byte) {
	buildNotes = append(buildNotes, buildNote{
		src:  src,
		argv: slices.Clone(argv),
		out:  slices.Clone(stderr),
	})
}

// flushBuildNotes prints every recorded build diagnostic, indented under one
// header per source. Nothing is printed when no build produced any.
func flushBuildNotes() {
	for _, note := range buildNotes {
		reportf("%scompiled with warnings: %s\n", msgPrefix, text.Text(note.src))
		for line := range strings.SplitSeq(strings.TrimRight(string(note.out), "\n"), "\n") {
			reportf("  %s\n", line)
		}
	}
	buildNotes = nil
}

// fileHasContent reports whether path is a file with bytes in it. Gate 2 is this
// question asked of a binary the compile step claimed to produce.
func fileHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// shellSplit splits a command as a POSIX shell would; only $CXX goes through it.
// A compiler wrapper is often a quoted multi-word command, so a backslash escapes
// the next character outside quotes, escapes only a quote or backslash inside
// double quotes, and is literal inside single quotes.
func shellSplit(s string) []string {
	var words []string
	var word strings.Builder
	inWord := false

	flush := func() {
		if inWord {
			words = append(words, word.String())
			word.Reset()
			inWord = false
		}
	}

	for i := 0; i < len(s); i++ {
		switch c := s[i]; c {
		case ' ', '\t', '\n', '\r':
			flush()
		case '\'':
			inWord = true
			for i++; i < len(s) && s[i] != '\''; i++ {
				word.WriteByte(s[i])
			}
		case '"':
			inWord = true
			for i++; i < len(s) && s[i] != '"'; i++ {
				if s[i] == '\\' && i+1 < len(s) && (s[i+1] == '"' || s[i+1] == '\\') {
					i++
				}
				word.WriteByte(s[i])
			}
		case '\\':
			inWord = true
			if i+1 < len(s) {
				i++
				word.WriteByte(s[i])
			}
		default:
			inWord = true
			word.WriteByte(c)
		}
	}
	flush()
	return words
}

// BuildGo compiles one registry-declared generator into out, reusing the
// content-addressed cache.
//
// The build happens in a scratch module this function assembles, not in the tree
// being tested:
//
//	<scratch>/go.mod            module lyxiang/benchsuite
//	<scratch>/src/genlib/*.go   written from genlib.Source
//	<scratch>/src/main/main.go  the generator
//
// Assembling it is what keeps a registry entry a one-line declaration. The
// alternative -- building against the tested tree's own module -- would require
// that tree to carry a go.mod with a replace directive pointing back at the
// harness, turning "declare a generator" into a build-system chore.
//
// The cache key covers everything the built binary depends on: the generator
// bytes, the embedded vocabulary, and the Go toolchain's own version. Without the
// last one, upgrading Go would reuse a binary the previous toolchain produced --
// the exact case where the bytes on disk are no longer the bytes the source
// describes.
func (e *Env) BuildGo(src, registryPath string) (string, error) {
	absSrc := src
	if !filepath.IsAbs(absSrc) {
		absSrc = filepath.Join(e.Root, filepath.FromSlash(src))
	}
	body, err := os.ReadFile(absSrc)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot read %s: %v\n", absSrc, err)
		return "", err
	}

	digest := sha256.New()
	digest.Write(normaliseNewlines(body))
	digest.Write([]byte("\x00"))
	vocabulary, err := genlibSourceDigest()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot read the embedded vocabulary: %v\n", err)
		return "", err
	}
	digest.Write([]byte("\x00"))
	digest.Write(vocabulary)
	digest.Write([]byte("\x00"))
	digest.Write([]byte(goToolchainVersion()))
	hash := hex.EncodeToString(digest.Sum(nil))

	// The generator is built beside its source, like any other product: a
	// generator at 99001/gen.go yields 99001/gen.exe.
	product := binaryFor(absSrc)
	if reused, ok := e.productIntact(product, registryPath, hash); ok {
		return reused, nil
	}
	if err := os.MkdirAll(filepath.Dir(product), 0o755); err != nil {
		return "", err
	}
	scratch, err := os.MkdirTemp("", "genplugin-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot make a scratch module: %v\n", err)
		return "", err
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	if err := writeScratchModule(scratch, body); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: %v\n", err)
		return "", err
	}

	staged, err := os.CreateTemp(filepath.Dir(product), filepath.Base(product)+".tmp-*")
	if err != nil {
		return "", err
	}
	stagedPath := staged.Name()
	_ = staged.Close()
	_ = os.Remove(stagedPath)

	result := execpkg.RunIn(scratch, []string{"go", "build", "-o", stagedPath, "./src/main"},
		nil, false, CompileTimeout)
	if result.TimedOut || result.Code != 0 {
		if len(result.Stderr) > 0 {
			_, _ = fmt.Fprintf(os.Stderr, "go build: %s\n", strings.TrimSpace(string(result.Stderr)))
		}
		return "", fmt.Errorf("generator did not compile")
	}
	if err := os.Rename(stagedPath, product); err != nil {
		return "", err
	}
	if registryPath != "" {
		// The generator is valid; only the record is missing, so the next run
		// rebuilds once. That is not a build failure, but it is worth saying:
		// a lock that cannot be updated means every run recompiles.
		if err := recordProductHash(lockPath(registryPath), e.relativeToRoot(product), hash); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "go build: cannot record the build hash in the lock: %v\n", err)
		}
	}
	return product, nil
}

// goToolchainVersion reports the version a `go build` here would use.
//
// It shells out to the go tool, so it is memoised: BuildGo consults it once per
// case, and a solver sweep runs hundreds of them. The answer cannot change during
// a run, so asking twice buys nothing but a process spawn.
//
// A failure to ask is not fatal: the empty string still keys a cache that a
// toolchain change cannot invalidate.
var goToolchainVersion = sync.OnceValue(func() string {
	out, err := exec.Command("go", "env", "GOVERSION").Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
})

// goModuleVersion is the go directive form of the toolchain version: a go.mod
// reads `go 1.27.3`, while GOWERSION reports `go1.27.3`. Reusing the reported
// string verbatim makes every scratch module fail to parse, so the prefix is
// stripped here rather than hard-coding a version that can drift.
func goModuleVersion() string {
	return strings.TrimPrefix(goToolchainVersion(), "go")
}

// genlibSourceDigest hashes the embedded vocabulary, so the cache key changes when
// the package a generator compiles against does.
//
// The embedded FS is bundled into the binary and cannot change while it runs, so
// the digest is computed once and reused.
var genlibSourceDigest = sync.OnceValues(func() ([]byte, error) {
	names, err := fs.Glob(genlib.Source, "*.go")
	if err != nil {
		return nil, err
	}
	slices.Sort(names)
	digest := sha256.New()
	for _, name := range names {
		body, err := genlib.Source.ReadFile(name)
		if err != nil {
			return nil, err
		}
		digest.Write([]byte(name))
		digest.Write([]byte("\x00"))
		digest.Write(body)
		digest.Write([]byte("\x00"))
	}
	return digest.Sum(nil), nil
})

// writeScratchModule lays out the module a generator compiles inside: the module
// file, the vocabulary package, and the generator itself as `package main`.
func writeScratchModule(dir string, generator []byte) error {
	if err := os.MkdirAll(filepath.Join(dir, "src", "genlib"), 0o755); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Join(dir, "src", "main"), 0o755); err != nil {
		return err
	}
	// The scratch module shares the harness's module path, so a generator's import
	// of lyxiang/benchsuite/src/genlib resolves to the copy written here. The go
	// directive comes from the toolchain that will build this module, so the two
	// cannot disagree; an unknown version omits the directive, leaving it at the
	// toolchain's own default.
	module := "module lyxiang/benchsuite\n"
	if version := goModuleVersion(); version != "" {
		module += "\ngo " + version + "\n"
	}
	if err := os.WriteFile(filepath.Join(dir, "go.mod"), []byte(module), 0o644); err != nil {
		return err
	}
	names, err := fs.Glob(genlib.Source, "*.go")
	if err != nil {
		return err
	}
	for _, name := range names {
		body, err := genlib.Source.ReadFile(name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(dir, "src", "genlib", name), body, 0o644); err != nil {
			return err
		}
	}
	return os.WriteFile(filepath.Join(dir, "src", "main", "main.go"), generator, 0o644)
}
