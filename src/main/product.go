package main

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

// A build product sits beside the source it was compiled from: a solution at
// 2267/A.cpp compiles to 2267/A.exe, and the name follows the source path
// rather than a content hash. A reader looking for the binary of a problem finds
// it under the problem.
//
// The hash of what was compiled is recorded in the registry next to it, so the
// next run can tell whether the binary still matches the source and the flags.
// That record is what makes the binary a cache: without it, a stale binary would
// be indistinguishable from a fresh one.
//
// This replaces a single shared directory of content-addressed entries. The
// entries are still content-addressed -- the hash in the registry is that
// address -- but they live where their source is, so a human can find them.

// binaryFor returns the product path for a source: the source path with its
// extension replaced by the platform's executable suffix.
func binaryFor(src string) string {
	return strings.TrimSuffix(src, filepath.Ext(src)) + binarySuffix()
}

// productHash is the digest of everything a compiled binary depends on: the
// source bytes and the flags it was compiled with. Changing either must change
// the hash, or a rebuilt binary would be adopted as if it were still current.
func productHash(src string, flags []string) (string, error) {
	digest := sha256.New()
	if err := digestFile(digest, src); err != nil {
		return "", err
	}
	digest.Write([]byte("\x00" + strings.Join(flags, "\x00")))
	return hex.EncodeToString(digest.Sum(nil)), nil
}

// digestFile streams a file into a digest rather than reading it whole: a source
// may be large, and hashing it must not load it into memory.
func digestFile(digest io.Writer, path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer func() { _ = file.Close() }()
	_, err = io.Copy(digest, file)
	return err
}

// buildProduct compiles a source into the product beside it, reusing the
// existing binary when the recorded hash still matches.
//
// registryPath is the registry file that records the hash; when it is empty the
// hash is not recorded anywhere and the binary is rebuilt on every run, which is
// what a throwaway product wants.
//
// The binary is written through a temporary file in the same directory and
// renamed into place, so a concurrent run never executes a half-written binary.
func (e *Env) buildProduct(src, registryPath string, flags []string) (string, error) {
	product := binaryFor(src)
	hash, err := productHash(src, flags)
	if err != nil {
		return "", fmt.Errorf("cannot read %s: %w", src, err)
	}

	if reused, ok := e.productIntact(product, registryPath, hash); ok {
		return reused, nil
	}
	if _, err := e.compileTo(src, product, flags); err != nil {
		return "", err
	}
	if registryPath != "" {
		// The binary is valid; only the record is missing, so the next run
		// rebuilds once. That is not a build failure, but it is worth saying:
		// a registry that cannot be updated means every run recompiles.
		if err := recordProductHash(registryPath, e.relativeToRoot(product), hash); err != nil {
			_, _ = fmt.Fprintf(os.Stderr, "bench.py: cannot record the build hash in %s: %v\n",
				filepath.Base(registryPath), err)
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
// hash the registry recorded.
//
// Three things must hold: the binary exists and is not empty, the registry
// records a hash for it, and the recorded hash is the one this source and these
// flags produce now. A mismatch means the source or the flags changed, so the
// binary is stale and must be rebuilt.
func (e *Env) productIntact(product, registryPath, want string) (string, bool) {
	info, err := os.Stat(product)
	if err != nil || info.IsDir() || info.Size() == 0 {
		return "", false
	}
	if registryPath == "" {
		return "", false
	}
	got, ok := readProductHash(registryPath, e.relativeToRoot(product))
	if !ok || got != want {
		return "", false
	}
	return product, true
}
