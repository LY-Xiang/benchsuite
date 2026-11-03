package main

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"maps"
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
	Root     string   // repo root
	CXX      []string // shell-split compiler command
	CacheDir string
}

func NewEnv(root string) *Env {
	cxx := []string{"g++"}
	if raw := os.Getenv("CXX"); strings.TrimSpace(raw) != "" {
		cxx = shellSplit(raw)
	}
	return &Env{
		Root:     root,
		CXX:      cxx,
		CacheDir: filepath.Join(root, ".bench", "bin"),
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

// Build compiles one source, reusing `.bench/bin/<key>` when the bytes and flags
// match. The key is sha256(source || NUL || flags)[:16], so an unchanged source
// compiles once per (content, flags). A miss compiles to a temporary file and is
// renamed into place, so a concurrent run never executes a half-written binary.
func (e *Env) Build(src, out string, flags []string) bool {
	digest := sha256.New()
	if err := digestFile(digest, src, 0); err != nil {
		return false
	}
	digest.Write([]byte("\x00" + strings.Join(flags, "\x00")))
	entry := filepath.Join(e.CacheDir, hex.EncodeToString(digest.Sum(nil))[:16]+binarySuffix())

	if reused, ok := e.cachedBinary(entry, out); ok {
		return reused
	}
	if !prepareCacheDirs(e.CacheDir, out) {
		return false
	}

	// A per-build temporary name, so two concurrent runs never compile into the
	// same path.
	staged, err := os.CreateTemp(e.CacheDir, "bench-*.tmp")
	if err != nil {
		return false
	}
	stagedPath := staged.Name()
	defer func() { _ = os.Remove(stagedPath) }()
	// The compiler can only open the staged file once it is closed, so a failed
	// close is a build failure. The remove above is only an attempt: on Windows
	// a failed close usually leaves the handle open and removal fails.
	if err := staged.Close(); err != nil {
		return false
	}

	argv := make([]string, 0, len(e.CXX)+len(flags)+3)
	argv = append(argv, e.CXX...)
	argv = append(argv, flags...)
	argv = append(argv, src, "-o", stagedPath)

	// Through RunProcess like any other child, so a wedged cc1plus is bounded and
	// its process group dies with it.
	result := execpkg.Run(argv, nil, false, CompileTimeout)
	if result.TimedOut || result.Code != 0 {
		return false
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
	return e.installStaged(stagedPath, entry, out)
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

// cachedBinary reports whether entry is a verified hit. The bool says the caller
// is done either way: true means a binary was installed, false with ok true means
// the caller must return false. ok false means a miss, so it should build.
func (e *Env) cachedBinary(entry, out string) (installed bool, ok bool) {
	info, err := os.Stat(entry)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return false, false
	}
	// A non-empty entry at the right name is not the same as an entry this harness
	// produced. Verify it against what was recorded on install; a mismatch, a
	// missing record or an oversized file is a miss, so the source is recompiled
	// instead of an unverified binary being executed.
	if cacheEntryIntact(e.CacheDir, entry) {
		return installBinary(entry, out), true
	}
	_ = os.Remove(entry)
	return false, false
}

// prepareCacheDirs creates the cache directory and the output's parent.
func prepareCacheDirs(cacheDir, out string) bool {
	if err := os.MkdirAll(cacheDir, 0o755); err != nil {
		return false
	}
	return os.MkdirAll(filepath.Dir(out), 0o755) == nil
}

// installStaged moves a freshly built binary into the cache and installs it at out.
//
// A staged file that is missing or empty is not a build failure: the compiler
// exited cleanly, so gate 2 reports the missing binary and nothing is cached.
func (e *Env) installStaged(stagedPath, entry, out string) bool {
	if info, err := os.Stat(stagedPath); err != nil || info.Size() == 0 {
		return true
	}
	if err := os.Rename(stagedPath, entry); err != nil {
		// Another process won the race. Its bytes came from the same content
		// hash, so adopt theirs.
		if _, statErr := os.Stat(entry); statErr != nil {
			return false
		}
	} else if digest, err := fileDigest(entry); err == nil {
		recordEntryDigest(e.CacheDir, filepath.Base(entry), digest)
	}
	return installBinary(entry, out)
}

// maxCachedBinary bounds a cache entry the harness will adopt: generous for a
// real binary, and small enough to refuse one padded into a memory hazard.
const maxCachedBinary = 32 << 20 // 32 MiB

// errCacheEntryTooLarge is a miss, not a crash: the entry is discarded and the
// source recompiled.
var errCacheEntryTooLarge = errors.New("cache entry is larger than any binary this harness builds")

// fileDigest is the hex sha256 of a file's bytes, streamed rather than read
// whole: the file is a cache entry, the one thing that may have been replaced
// with a huge one, and verifying it must not load it into memory.
func fileDigest(path string) (string, error) {
	digest := sha256.New()
	if err := digestFile(digest, path, maxCachedBinary+1); err != nil {
		return "", err
	}
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// digestFile streams a file into a digest. A non-zero ceiling reads at most that
// many bytes and reports errCacheEntryTooLarge rather than hashing an oversized
// entry in part; zero reads to EOF.
func digestFile(digest io.Writer, path string, ceiling int64) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	if ceiling == 0 {
		_, err = io.Copy(digest, file)
		return err
	}
	// One byte past the ceiling tells "at the limit" from "over it".
	read, err := io.Copy(digest, io.LimitReader(file, ceiling))
	if err != nil {
		return err
	}
	if read >= ceiling {
		return errCacheEntryTooLarge
	}
	return nil
}

// cacheManifest is where each installed entry's digest is recorded. It lives
// beside the cache directory, not inside it: that listing is a characterized
// contract of <16 hex><suffix> entry files, so a bookkeeping file there would
// change it. The digest cannot ride in the entry's filename for the same reason.
func cacheManifest(dir string) string {
	return filepath.Join(filepath.Dir(dir), ".bench-cache.digest")
}

// readManifest parses the cache manifest into entry name -> digest. A missing or
// unreadable manifest is an empty map: every entry then misses and recompiles.
func readManifest(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	entries := make(map[string]string)
	for line := range strings.SplitSeq(string(raw), "\n") {
		if key, value, ok := strings.Cut(line, " "); ok && key != "" {
			entries[key] = value
		}
	}
	return entries
}

// recordEntryDigest notes the digest of an entry just installed. A failed write
// is not a build failure: the entry is still valid, the next hit merely
// recompiles once because it cannot verify it.
func recordEntryDigest(dir, name, digest string) {
	path := cacheManifest(dir)
	entries := readManifest(path)
	entries[name] = digest

	var out strings.Builder
	for _, key := range slices.Sorted(maps.Keys(entries)) {
		out.WriteString(key)
		out.WriteByte(' ')
		out.WriteString(entries[key])
		out.WriteByte('\n')
	}
	// A failed write only costs a recompile later, so it is not an error.
	_ = os.WriteFile(path, []byte(out.String()), 0o644)
}

// cacheEntryIntact reports whether an entry still hashes to what was recorded
// when it was installed. No record, or bytes that no longer match, is a miss.
//
// This catches a corrupt, truncated or misplaced entry and stops a binary
// dropped in under a guessed key from being adopted. It does not authenticate
// against a writer who can also rewrite the manifest.
func cacheEntryIntact(dir, path string) bool {
	want := readManifest(cacheManifest(dir))[filepath.Base(path)]
	if len(want) != 64 {
		return false
	}
	got, err := fileDigest(path)
	return err == nil && got == want
}

// installBinary makes out hold the cached binary, reporting whether a usable one
// is there. A concurrent build renames the same content-hashed entry into place
// and Windows refuses to open a file being replaced; the bytes are identical
// either way, so a transient sharing violation is retried.
func installBinary(entry, out string) bool {
	for range installAttempts {
		if installOnce(entry, out) {
			return true
		}
		if _, err := os.Stat(entry); err != nil {
			return false
		}
		time.Sleep(installRetry)
	}
	return false
}

const (
	installAttempts = 50
	installRetry    = 10 * time.Millisecond
)

func installOnce(entry, out string) bool {
	if outInfo, err := os.Stat(out); err == nil && !outInfo.IsDir() {
		if entryInfo, err := os.Stat(entry); err == nil && os.SameFile(outInfo, entryInfo) {
			return true
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return false
	}
	data, err := os.ReadFile(entry)
	if err != nil {
		return false
	}
	if err := os.WriteFile(out, data, 0o755); err != nil {
		return false
	}
	return fileHasContent(out)
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
func (e *Env) BuildGo(src, out string) bool {
	absSrc := src
	if !filepath.IsAbs(absSrc) {
		absSrc = filepath.Join(e.Root, filepath.FromSlash(src))
	}
	body, err := os.ReadFile(absSrc)
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot read %s: %v\n", absSrc, err)
		return false
	}

	digest := sha256.New()
	digest.Write(body)
	digest.Write([]byte("\x00"))
	vocabulary, err := genlibSourceDigest()
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot read the embedded vocabulary: %v\n", err)
		return false
	}
	digest.Write([]byte("\x00"))
	digest.Write(vocabulary)
	digest.Write([]byte("\x00"))
	digest.Write([]byte(goToolchainVersion()))

	entry := filepath.Join(e.CacheDir, "go-"+hex.EncodeToString(digest.Sum(nil))[:16]+binarySuffix())
	if reused, ok := e.cachedBinary(entry, out); ok {
		return reused
	}
	if !prepareCacheDirs(e.CacheDir, out) {
		return false
	}

	scratch, err := os.MkdirTemp("", "genplugin-*")
	if err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: cannot make a scratch module: %v\n", err)
		return false
	}
	defer func() { _ = os.RemoveAll(scratch) }()

	if err := writeScratchModule(scratch, body); err != nil {
		_, _ = fmt.Fprintf(os.Stderr, "go build: %v\n", err)
		return false
	}

	staged, err := os.CreateTemp(e.CacheDir, "benchgo-*.tmp")
	if err != nil {
		return false
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
		return false
	}
	return e.installStaged(stagedPath, entry, out)
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
