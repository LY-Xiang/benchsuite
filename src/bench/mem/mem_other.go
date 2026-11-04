//go:build !linux && !darwin && !windows

package mem

// Platforms the harness can build for but cannot measure: the run still happens
// and still counts, only the memory and CPU columns report that they were not
// measured. That is a supported outcome rather than a refusal.

import (
	"time"

	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
)

func peakOf(execpkg.Process) (int64, bool) { return 0, false }

func cpuOf(execpkg.Process) (time.Duration, bool) { return 0, false }
