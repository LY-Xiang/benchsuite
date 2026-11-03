// Package peakmem measures a child process's peak resident set.
//
// A measurement is taken by opening a handle or descriptor on the child as soon
// as it starts and reading the high-water mark after it exits. Both supported
// platforms keep that mark in the kernel, so the value does not depend on how
// often the caller looks: a process that allocates 200 MiB for 1 ms and frees it
// still reports 200 MiB. That is why this is one read and not a polling loop --
// polling would miss exactly the spikes the number is meant to catch.
//
// The handle must be opened while the child is still alive, which is why Begin is
// called immediately after the process starts rather than after it is waited for.
//
// A process cannot report the peak of a process that has already exited, so a
// failed open is reported as unmeasured rather than as a zero: zero would claim
// the child used no memory, which is a stronger and false statement.
package peakmem

// Unknown marks a measurement that was not made. Every other value is a real
// byte count, so a caller can compare against it directly.
const Unknown int64 = -1

// Handle is one live measurement. A build with no platform implementation still
// returns a Handle, so callers never branch on the platform.
type Handle interface {
	// Read reports the child's peak resident set in bytes. The bool is false when
	// no sample succeeded, and the caller should report Unknown.
	Read() (int64, bool)
	// Release frees whatever the handle holds. It is safe to call twice.
	Release()
}

// unsupported is the handle a build without a platform implementation gets.
type unsupported struct{}

func (unsupported) Read() (int64, bool) { return 0, false }
func (unsupported) Release()            {}

// Unsupported returns the handle for a build with no platform implementation.
// Callers that always want a Handle start from it and overwrite on success.
func Unsupported() Handle { return unsupported{} }
