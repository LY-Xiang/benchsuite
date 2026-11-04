//go:build darwin

package mem

// ru_maxrss is in bytes on Darwin.
const maxrssScale = 1
