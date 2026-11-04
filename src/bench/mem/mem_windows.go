//go:build windows

package mem

// Memory and CPU on Windows, read from the handle exec opened.
//
// psapi's GetProcessMemoryInfo fills a PROCESS_MEMORY_COUNTERS_EX whose
// PeakWorkingSetSize is the high-water mark since the process started, so one read
// after the child exits gives the real peak. The same call answers whether the
// process is still addressable at all.
//
// The counters struct is declared for a 64-bit word so its layout matches the
// native one. On a 32-bit word the uintptr fields would be 4 bytes and the offset
// of PeakWorkingSetSize would be wrong, so the build tag keeps this to amd64/arm64.

import (
	"syscall"
	"time"
	"unsafe"

	execpkg "github.com/LY-Xiang/benchsuite/src/exec"
)

var (
	psapi                    = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procGetProcessTimes      = kernel32.NewProc("GetProcessTimes")
)

// processMemoryCountersEx mirrors the native structure. Only the fields through
// PeakWorkingSetSize are read; the tail must still be present, since the size
// passed to the call is what selects the EX variant.
type processMemoryCountersEx struct {
	Cb                         uint32
	PageFaultCount             uint32
	PeakWorkingSetSize         uintptr
	WorkingSetSize             uintptr
	QuotaPeakPagedPoolUsage    uintptr
	QuotaPagedPoolUsage        uintptr
	QuotaPeakNonPagedPoolUsage uintptr
	QuotaNonPagedPoolUsage     uintptr
	PagefileUsage              uintptr
	PeakPagefileUsage          uintptr
	PrivateUsage               uintptr
}

func peakOf(proc execpkg.Process) (int64, bool) {
	if proc.Handle == 0 {
		return 0, false
	}
	var counters processMemoryCountersEx
	counters.Cb = uint32(unsafe.Sizeof(counters))
	ok, _, _ := procGetProcessMemoryInfo.Call(
		proc.Handle, uintptr(unsafe.Pointer(&counters)), unsafe.Sizeof(counters))
	if ok == 0 {
		return 0, false
	}
	return int64(counters.PeakWorkingSetSize), true
}

// cpuOf takes the process' own CPU time from the accounting the OS kept for it.
//
// It reads GetProcessTimes rather than dividing a cycle count by a clock: a core's
// frequency moves during a run, so a cycle count converted at the frequency
// observed at exit is not a duration -- a boosted core would make the child appear
// to have used less time than the wall clock allows. The accounting is quantised to
// the scheduler's tick, which costs accuracy only for runs shorter than that.
func cpuOf(proc execpkg.Process) (time.Duration, bool) {
	if proc.Handle == 0 {
		return 0, false
	}
	var created, exited, kernel, user syscall.Filetime
	ok, _, _ := procGetProcessTimes.Call(proc.Handle,
		uintptr(unsafe.Pointer(&created)), uintptr(unsafe.Pointer(&exited)),
		uintptr(unsafe.Pointer(&kernel)), uintptr(unsafe.Pointer(&user)))
	if ok == 0 {
		return 0, false
	}
	return filetime(kernel) + filetime(user), true
}

// filetime converts a FILETIME, which counts 100-nanosecond intervals.
func filetime(ft syscall.Filetime) time.Duration {
	return time.Duration(int64(ft.HighDateTime)<<32+int64(ft.LowDateTime)) * 100
}
