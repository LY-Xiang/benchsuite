package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
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

// binaryFor returns the product path for a source: the source path with its
// extension replaced by the platform's executable suffix.
func binaryFor(src string) string {
	return strings.TrimSuffix(src, filepath.Ext(src)) + binarySuffix()
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
	for i := 0; i < len(body); i++ {
		if body[i] == '\r' && i+1 < len(body) && body[i+1] == '\n' {
			continue
		}
		out = append(out, body[i])
	}
	return out
}

// buildProduct compiles a source into the product beside it, reusing the
// existing binary when the recorded hash still matches.
//
// registryPath locates the lock: the lock sits beside the registry. When it is
// empty the hash is not recorded anywhere and the binary is rebuilt on every
// run, which is what a throwaway product wants.
func (e *Env) buildProduct(src, registryPath string, flags []string) (string, error) {
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
	if reused, ok := e.productIntact(product, lock, hash); ok {
		return reused, nil
	}
	if _, err := e.compileTo(src, product, flags); err != nil {
		return "", err
	}
	if lock != "" {
		// The binary is valid; only the record is missing, so the next run
		// rebuilds once. That is not a build failure, but it is worth saying:
		// a lock that cannot be updated means every run recompiles.
		if err := recordProductHash(lock, e.relativeToRoot(product), hash); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "bench.py: cannot record the build hash in %s: %v\n",
				filepath.Base(lock), err)
		}
	}
	return product, nil
}

// relativeToRoot renders a path relative to the corpus root, in the same form
// the registry uses for src, so a hash key reads like the src it belongs to.
func (e *Env) relativeToRoot(path string) string {
	rel, err := filepath.Rel(e.Root, path)
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
func (e *Env) productIntact(product, lock, want string) (string, bool) {
	info, err := os.Stat(product)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", false
	}
	if lock == "" {
		return "", false
	}
	got, ok := readProductHash(lock, e.relativeToRoot(product))
	if !ok || got != want {
		return "", false
	}
	return product, true
}
