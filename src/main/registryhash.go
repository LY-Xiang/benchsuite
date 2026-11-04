package main

import (
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
)

// The hash of a compiled product is recorded in bench.lock, beside the registry:
//
//	2267/A.exe a3f2...
//	2267/refs/2267A.exe 9c81...
//
// One product per line, its corpus-relative path followed by the digest of the
// source bytes and the flags it was built from. The lock is state the harness
// owns: it is regenerated whenever the sources change, it is not version
// controlled, and no human reads it. That is why it is a lock and not a section
// of the registry -- the registry is a declaration, the lock is a record.
//
// A corrupted or missing lock is not an error. It costs one full rebuild, the
// same as a first run, and the lock is rewritten correctly afterwards.

// recordProductHash writes the hash of a compiled source into the lock,
// preserving the entries already there.
//
// The file is rewritten atomically: the new content goes to a temporary file in
// the same directory and is renamed over the old one, so a concurrent reader
// sees either the whole old file or the whole new one, never a partial write.
//
// The read-modify-write is serialised in-process: cases build in parallel, and
// two products of one contest finishing at once would otherwise each rewrite the
// lock from a snapshot taken before the other's entry landed, losing one record.
// A single mutex is enough -- a lock write costs one small file, and a build
// dwarfs it.
var lockMu sync.Mutex

func recordProductHash(lockPath, productPath, hash string) error {
	lockMu.Lock()
	defer lockMu.Unlock()

	entries := readLock(lockPath)
	entries[productPath] = hash

	var out strings.Builder
	for _, key := range sortedKeys(entries) {
		out.WriteString(key)
		out.WriteByte(' ')
		out.WriteString(entries[key])
		out.WriteByte('\n')
	}
	return writeFileAtomic(lockPath, []byte(out.String()))
}

// readProductHash returns the hash recorded for a product path in the lock.
func readProductHash(lockPath, productPath string) (string, bool) {
	entries := readLock(lockPath)
	hash, ok := entries[productPath]
	return hash, ok
}

// readLock parses the lock into product path -> hash. A missing or unreadable
// lock is an empty map: every product then misses and recompiles, exactly like
// a first run.
func readLock(path string) map[string]string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return map[string]string{}
	}
	entries := make(map[string]string)
	for _, line := range strings.Split(string(raw), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		key, value, ok := strings.Cut(line, " ")
		if !ok || key == "" || value == "" {
			continue
		}
		entries[key] = value
	}
	return entries
}

// sortedKeys returns the map keys in stable order, so the lock file does not
// churn between runs when only one entry changed.
func sortedKeys(entries map[string]string) []string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	return keys
}

// writeFileAtomic replaces a file's contents through a temporary file in the
// same directory, so a reader never sees a partial write.
func writeFileAtomic(path string, content []byte) error {
	dir := filepath.Dir(path)
	temp, err := os.CreateTemp(dir, filepath.Base(path)+".tmp-*")
	if err != nil {
		return err
	}
	tempPath := temp.Name()
	defer func() { _ = os.Remove(tempPath) }()
	if _, err := temp.Write(content); err != nil {
		_ = temp.Close()
		return err
	}
	if err := temp.Close(); err != nil {
		return err
	}
	return os.Rename(tempPath, path)
}
