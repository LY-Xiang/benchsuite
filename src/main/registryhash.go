package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// The hash of a compiled product is recorded in the registry next to the source
// it came from:
//
//	[A]
//	maxgen = "letters %S 25"
//	hash = { "2267/A.cpp" = "3f2a...", "2267/refs/2267A.cpp" = "9c81..." }
//
// The key is the source path relative to the corpus root, so the solution and
// the reference of one table are told apart and neither shadows the other.
//
// The registry is written textually rather than re-encoded. A TOML encoder
// would drop every comment in the file, and these registries carry comments
// that explain the input shape -- the most valuable part of them. Editing the
// lines in place keeps them.

// hashKey is the registry key under which the recorded hashes live.
const hashKey = "hash"

// readProductHash returns the hash recorded for a source path in a registry.
func readProductHash(registryPath, sourcePath string) (string, bool) {
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return "", false
	}
	for _, line := range strings.Split(string(raw), "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, hashKey+" ") && !strings.HasPrefix(trimmed, hashKey+"=") {
			continue
		}
		entry, ok := parseHashEntry(trimmed)
		if !ok {
			continue
		}
		if hash, ok := entry[sourcePath]; ok {
			return hash, true
		}
	}
	return "", false
}

// parseHashEntry reads one `hash = { ... }` line into its pairs. A malformed
// line yields no pairs rather than an error: a registry that cannot be read
// this way simply has no recorded hashes, and every product is rebuilt.
func parseHashEntry(line string) (map[string]string, bool) {
	rest, ok := strings.CutPrefix(line, hashKey)
	if !ok {
		return nil, false
	}
	rest = strings.TrimSpace(rest)
	rest, ok = strings.CutPrefix(rest, "=")
	if !ok {
		return nil, false
	}
	rest = strings.TrimSpace(rest)
	rest, ok = strings.CutPrefix(rest, "{")
	if !ok {
		return nil, false
	}
	rest, ok = strings.CutSuffix(strings.TrimSpace(rest), "}")
	if !ok {
		return nil, false
	}
	out := map[string]string{}
	for _, part := range strings.Split(rest, ",") {
		key, value, ok := strings.Cut(strings.TrimSpace(part), "=")
		if !ok {
			continue
		}
		key = strings.Trim(strings.TrimSpace(key), `"`)
		value = strings.Trim(strings.TrimSpace(value), `"`)
		if key != "" && value != "" {
			out[key] = value
		}
	}
	return out, true
}

// recordProductHash writes the hash of a compiled source into a registry.
//
// The file is rewritten atomically: the new content goes to a temporary file in
// the same directory and is renamed over the old one, so a concurrent reader
// sees either the whole old file or the whole new one, never a partial write.
func recordProductHash(registryPath, sourcePath, hash string) error {
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return err
	}
	table, ok := tableNameFor(registryPath, sourcePath)
	if !ok {
		return fmt.Errorf("cannot find the table for %s in %s", sourcePath, registryPath)
	}
	updated, err := setHashInTable(string(raw), table, sourcePath, hash)
	if err != nil {
		return err
	}
	if updated == string(raw) {
		return nil
	}
	return writeFileAtomic(registryPath, []byte(updated))
}

// setHashInTable returns the registry text with the hash recorded for one
// source in the named table.
func setHashInTable(text, table, sourcePath, hash string) (string, error) {
	lines := strings.Split(text, "\n")
	start, end := tableRange(lines, table)
	if start < 0 {
		return "", fmt.Errorf("no table %s", table)
	}

	entries := map[string]string{}
	hashLine := -1
	for i := start; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		if !strings.HasPrefix(trimmed, hashKey+" ") && !strings.HasPrefix(trimmed, hashKey+"=") {
			continue
		}
		if parsed, ok := parseHashEntry(trimmed); ok {
			for k, v := range parsed {
				entries[k] = v
			}
		}
		hashLine = i
		break
	}
	entries[sourcePath] = hash

	rendered := renderHashEntry(entries)
	if hashLine >= 0 {
		lines[hashLine] = rendered
	} else {
		// Insert after the last key of the table, so the hash sits with the
		// other declarations rather than before the table header.
		insert := start + 1
		for i := start + 1; i < end; i++ {
			trimmed := strings.TrimSpace(lines[i])
			if trimmed == "" || strings.HasPrefix(trimmed, "#") {
				continue
			}
			insert = i + 1
		}
		lines = append(lines[:insert], append([]string{rendered}, lines[insert:]...)...)
	}
	return strings.Join(lines, "\n"), nil
}

// renderHashEntry writes the pairs back as one inline table, sorted by key so
// the file is stable across runs.
func renderHashEntry(entries map[string]string) string {
	keys := make([]string, 0, len(entries))
	for key := range entries {
		keys = append(keys, key)
	}
	sortStrings(keys)
	var b strings.Builder
	b.WriteString(hashKey + " = { ")
	for i, key := range keys {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%q = %q", key, entries[key])
	}
	b.WriteString(" }")
	return b.String()
}

// tableNameFor finds the registry table that declares a source path.
//
// The solution of a problem is the source its table names, and the reference is
// the file refs/<id>.cpp, whose id carries the table name. Both are matched
// against the tables the file declares.
//
// The path may name either a source or the product compiled from it, since the
// hash key is written in the form of the product.
func tableNameFor(registryPath, path string) (string, bool) {
	raw, err := os.ReadFile(registryPath)
	if err != nil {
		return "", false
	}
	tables := declaredTables(string(raw))
	if len(tables) == 0 {
		return "", false
	}
	stem := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	// A source or product named after the table: 2267/A.cpp or 2267/A.exe.
	for _, name := range tables {
		if stem == name {
			return name, true
		}
	}
	// A source named by a table's own src key: 717343/E-usr.exe under the table
	// EUsr, whose id cannot carry the hyphen.
	for _, name := range tables {
		if src := tableSrc(string(raw), name); src != "" {
			srcStem := strings.TrimSuffix(filepath.Base(src), filepath.Ext(src))
			if srcStem == stem {
				return name, true
			}
		}
	}
	// A reference lives in refs/ and is named after the problem id, which is the
	// contest followed by the table name.
	if strings.Contains(path, "/refs/") {
		for _, name := range tables {
			if strings.HasSuffix(stem, name) {
				return name, true
			}
		}
		return tables[0], true
	}
	return "", false
}

// tableSrc reads the src key of one table, or "" when it declares none.
func tableSrc(text, table string) string {
	lines := strings.Split(text, "\n")
	start, end := tableRange(lines, table)
	if start < 0 {
		return ""
	}
	for i := start + 1; i < end; i++ {
		trimmed := strings.TrimSpace(lines[i])
		value, ok := strings.CutPrefix(trimmed, "src")
		if !ok {
			continue
		}
		value, ok = strings.CutPrefix(strings.TrimSpace(value), "=")
		if !ok {
			continue
		}
		return strings.Trim(strings.TrimSpace(value), `"`)
	}
	return ""
}

// declaredTables lists the table names a registry declares, in file order.
func declaredTables(text string) []string {
	var out []string
	for _, line := range strings.Split(text, "\n") {
		trimmed := strings.TrimSpace(line)
		if !strings.HasPrefix(trimmed, "[") || strings.HasPrefix(trimmed, "[[") {
			continue
		}
		name, ok := strings.CutPrefix(trimmed, "[")
		if !ok {
			continue
		}
		name, ok = strings.CutSuffix(name, "]")
		if !ok || name == "" {
			continue
		}
		out = append(out, name)
	}
	return out
}

// tableRange returns the line range of a table's body: the header line and the
// lines up to the next header.
func tableRange(lines []string, table string) (int, int) {
	start := -1
	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "["+table+"]" {
			start = i
			break
		}
	}
	if start < 0 {
		return -1, -1
	}
	end := len(lines)
	for i := start + 1; i < len(lines); i++ {
		trimmed := strings.TrimSpace(lines[i])
		if strings.HasPrefix(trimmed, "[") {
			end = i
			break
		}
	}
	return start, end
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

// sortStrings sorts in place; it is here so this file does not need the sort
// import for one call.
func sortStrings(values []string) {
	for i := 1; i < len(values); i++ {
		for j := i; j > 0 && values[j] < values[j-1]; j-- {
			values[j], values[j-1] = values[j-1], values[j]
		}
	}
}
