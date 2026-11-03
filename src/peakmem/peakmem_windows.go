//go:build windows

package peakmem

// Peak working set on Windows.
//
// psapi's GetProcessMemoryInfo fills a PROCESS_MEMORY_COUNTERS_EX whose
// PeakWorkingSetSize is the high-water mark since the process started, so one read
// after the child exits gives the real peak. The handle is opened with
// PROCESS_QUERY_LIMITED_INFORMATION, the least privilege that can answer the
// question, and it must be opened while the child is alive: a handle acquired
// after exit would address a dead process and fail.
//
// The counters struct is declared for a 64-bit word so its layout matches the
// native one. On a 32-bit word the uintptr fields would be 4 bytes and the offset
// of PeakWorkingSetSize would be wrong, so the build tag keeps this to amd64/arm64.

import (
	"syscall"
	"unsafe"
)

var (
	psapi                    = syscall.NewLazyDLL("psapi.dll")
	procGetProcessMemoryInfo = psapi.NewProc("GetProcessMemoryInfo")
	kernel32                 = syscall.NewLazyDLL("kernel32.dll")
	procOpenProcess          = kernel32.NewProc("OpenProcess")
	procCloseHandle          = kernel32.NewProc("CloseHandle")
)

// processQueryLimitedInformation is enough to read memory counters and less than
// PROCESS_VM_READ, which would let the harness read a child's memory itself.
const processQueryLimitedInformation = 0x1000

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

type windows struct {
	handle uintptr
}

// Begin opens the handle a later read needs. A failure is not fatal:
// the run proceeds and reports the measurement as unknown.
func Begin(pid int) Handle {
	handle, _, _ := procOpenProcess.Call(processQueryLimitedInformation, 0, uintptr(pid))
	if handle == 0 {
		return unsupported{}
	}
	return &windows{handle: handle}
}

func (h *windows) Read() (int64, bool) {
	if h.handle == 0 {
		return 0, false
	}
	var counters processMemoryCountersEx
	counters.Cb = uint32(unsafe.Sizeof(counters))
	ok, _, _ := procGetProcessMemoryInfo.Call(
		h.handle, uintptr(unsafe.Pointer(&counters)), unsafe.Sizeof(counters))
	if ok == 0 {
		return 0, false
	}
	return int64(counters.PeakWorkingSetSize), true
}

func (h *windows) Release() {
	if h.handle != 0 {
		// A failed close leaks one handle for the life of the process; there is no
		// recovery, so the result is deliberately discarded.
		_, _, _ = procCloseHandle.Call(h.handle)
		h.handle = 0
	}
}
