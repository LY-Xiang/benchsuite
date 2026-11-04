//go:build linux

package mem

// ru_maxrss is in kilobytes on Linux.
const maxrssScale = 1024
