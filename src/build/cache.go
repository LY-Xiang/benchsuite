package build

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/LY-Xiang/benchsuite/src/genlib"
)

// A build product sits beside the source it was compiled from: a solution at
// 2267/A.cpp compiles to 2267/A.exe, and the name follows the source path
// rather than a content hash. A reader looking for the binary of a problem finds
// it under the problem.
//
// The hash of what was compiled is recorded in bench.lock, beside the registry,
// so the next run can tell whether the binary still matches the source and the
// flags. That record is what makes the binary a cache: without it, a stale
// binary would be indistinguishable from a fresh one.
//
// The lock is state the harness owns, not a declaration the author edits: it is
// regenerated whenever the sources change, and it is not version-controlled.
// The registry stays purely declarative, and editing a source does not dirty a
// file a human maintains.

// A Cache is one run's compiler and binary cache: the checkout it works against,
// the compiler command, the launch floor it compiles at most once, and the
// diagnostics its builds collected.
//
// It is a struct rather than a set of package functions because there is real
// per-run state here -- the floor, the note list, the root -- and two runs in one
// process must not share it.
type Cache struct {
	Root string   // repo root
	CXX  []string // shell-split compiler command

	// logf reports a failure that is not the caller's answer: a lock that cannot
	// be written costs a rebuild, not a failed case. The CLI passes a writer to
	// stderr; a test would collect the text.
	logf func(format string, args ...any)

	// floorOnce compiles the launch floor at most once per process: it is the same
	// empty program for every problem, and a sweep used to rebuild it per row.
	floorOnce sync.Once
	floorDir  string
	floorBin  string
	floorErr  error

	// notesMu guards notes: cases run in parallel, and a note is appended by
	// whichever worker compiled the source.
	notesMu sync.Mutex
	notes   []Note
}

// New returns a Cache rooted at root. $CXX, when set, replaces the compiler
// command; it is shell-split once here so a quoted path with spaces in it stays
// one argument.
func New(root string, logf func(format string, args ...any)) *Cache {
	cxx := []string{"g++"}
	if raw := os.Getenv("CXX"); strings.TrimSpace(raw) != "" {
		cxx = shellSplit(raw)
	}
	if logf == nil {
		logf = func(string, ...any) {}
	}
	return &Cache{
		Root: root,
		CXX:  cxx,
		logf: logf,
	}
}

// Compile compiles a source to an exact output path. It is the door for a source
// with no recorded hash -- a launch floor, a generator -- which therefore has no
// cache to consult.
func (c *Cache) Compile(src, out string, flags []string) (string, error) {
	return c.compileTo(src, out, flags)
}

// Product compiles a source into the product beside it, reusing the existing
// binary when the recorded hash still matches.
//
// registry is the bench.toml whose lock records the hash. When it is empty the
// hash is not recorded anywhere and the binary is rebuilt on every run, which is
// what a throwaway product wants.
func (c *Cache) Product(src, registry string, flags []string) (string, error) {
	return c.buildProduct(src, registry, flags)
}

// GoProduct compiles one registry-declared generator, reusing the
// content-addressed cache.
func (c *Cache) GoProduct(src, registry string) (string, error) {
	return c.buildGo(src, registry)
}

// LaunchFloor returns the path of the empty program a timing row subtracts,
// compiling it on first use.
func (c *Cache) LaunchFloor() (string, error) { return c.launchFloor() }

// Close removes the launch-floor directory. It is safe to call when nothing was
// compiled.
func (c *Cache) Close() { c.closeLaunchFloor() }

// BinaryHasContent reports whether path is a file with bytes in it. Gate 2 is
// this question asked of a binary the compile step claimed to produce.
func BinaryHasContent(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir() && info.Size() > 0
}

// binaryFor returns the product path for a source: the source path with its
// extension replaced by the platform's executable suffix.
func binaryFor(src string) string {
	return strings.TrimSuffix(src, filepath.Ext(src)) + execSuffix()
}

// lockPath returns the lock file beside a registry: 2267/bench.toml is paired
// with 2267/bench.lock.
func lockPath(registryPath string) string {
	dir := filepath.Dir(registryPath)
	base := strings.TrimSuffix(filepath.Base(registryPath), filepath.Ext(registryPath))
	return filepath.Join(dir, base+".lock")
}

// productHash is the digest of everything a compiled binary depends on: the
// source bytes and the flags it was compiled with. Changing either must change
// the hash, or a rebuilt binary would be adopted as if it were still current.
//
// Line endings are normalised to LF before hashing. A corpus checked out with
// autocrlf converts every file, so the same source would otherwise hash
// differently on two machines and each would rebuild what the other just built.
// The compiler does not care about the difference, so neither should the key.
func productHash(src string, flags []string) (string, error) {
	body, err := os.ReadFile(src)
	if err != nil {
		return "", err
	}
	digest := sha256.New()
	digest.Write(normaliseNewlines(body))
	digest.Write([]byte("\x00" + strings.Join(flags, "\x00")))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// normaliseNewlines strips the CR of a CRLF pair, leaving LF. A CR that is not
// followed by LF is content and is kept.
func normaliseNewlines(body []byte) []byte {
	if !bytes.ContainsRune(body, '\r') {
		return body
	}
	out := make([]byte, 0, len(body))
	for i := range body {
		if body[i] == '\r' && i+1 < len(body) && body[i+1] == '\n' {
			continue
		}
		out = append(out, body[i])
	}
	return out
}

// buildProduct compiles a source into the product beside it, reusing the
// existing binary when the recorded hash still matches.
func (c *Cache) buildProduct(src, registryPath string, flags []string) (string, error) {
	product := binaryFor(src)
	hash, err := productHash(src, flags)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", src, err)
	}

	lock := ""
	if registryPath != "" {
		lock = lockPath(registryPath)
	}

	unlock := buildLock(product)
	defer unlock()

	// Re-check under the lock: another worker may have built exactly this product
	// while this one was reading the source and its hash.
	if reused, ok := c.productIntact(product, lock, hash); ok {
		return reused, nil
	}
	if _, err := c.compileTo(src, product, flags); err != nil {
		return "", err
	}
	if lock != "" {
		// The binary is valid; only the record is missing, so the next run
		// rebuilds once. That is not a build failure, but it is worth saying:
		// a lock that cannot be updated means every run recompiles.
		if err := recordProductHash(lock, c.relativeToRoot(product), hash); err != nil {
			c.logf("bench.py: cannot record the build hash in %s: %v\n",
				filepath.Base(lock), err)
		}
	}
	return product, nil
}

// relativeToRoot renders a path relative to the corpus root, in the same form
// the registry uses for src, so a hash key reads like the src it belongs to.
func (c *Cache) relativeToRoot(path string) string {
	rel, err := filepath.Rel(c.Root, path)
	if err != nil {
		return filepath.ToSlash(path)
	}
	return filepath.ToSlash(rel)
}

// productIntact reports whether the product beside a source still matches the
// hash the lock recorded.
//
// Three things must hold: the binary exists and is not empty, the lock records
// a hash for it, and the recorded hash is the one this source and these flags
// produce now. A mismatch means the source or the flags changed, so the binary
// is stale and must be rebuilt.
func (c *Cache) productIntact(product, lock, want string) (string, bool) {
	info, err := os.Stat(product)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", false
	}
	if lock == "" {
		return "", false
	}
	got, ok := readProductHash(lock, c.relativeToRoot(product))
	if !ok || got != want {
		return "", false
	}
	return product, true
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
