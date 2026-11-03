//go:build !windows && !linux

package peakmem

// Peak memory on platforms with neither VmHWM nor psapi. The run still happens and
// still counts; only the memory column reports that it was not measured, which is
// why this is a supported outcome rather than a refusal.

func Begin(int) Handle { return unsupported{} }
