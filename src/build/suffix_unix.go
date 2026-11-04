//go:build !windows

package build

// Platform difference as a file, not a runtime branch: each file provides the
// executable suffix this platform's products carry, and exactly one is compiled.

// execSuffix is the extension every compiled product gets on this platform.
func execSuffix() string { return "" }
