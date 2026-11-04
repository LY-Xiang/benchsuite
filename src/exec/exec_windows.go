//go:build windows

package exec

import (
	"os"
	"os/exec"
	"sync/atomic"
	"syscall"
	"unsafe"
)

// createNewProcessGroup puts the child in its own group, so a console Ctrl+C
// aimed at the harness does not reach it.
const createNewProcessGroup = 0x00000200

// Job-object wiring. A job is the kernel's own container for a process tree:
// assigning the child means every descendant it spawns later is in the job too,
// and one TerminateJobObject takes the whole set down. The previous approach ran
// an external `taskkill /T`, which costs a process spawn on every timeout and
// depends on a program that may be absent, wedged or refused.
//
// JOB_OBJECT_LIMIT_KILL_ON_JOB_CLOSE closes the remaining hole: if the harness
// itself dies, the job handle is closed by the kernel and the child's tree goes
// with it instead of leaking.
var (
	kernel32                     = syscall.NewLazyDLL("kernel32.dll")
	procCreateJobObjectW         = kernel32.NewProc("CreateJobObjectW")
	procSetInformationJobObject  = kernel32.NewProc("SetInformationJobObject")
	procAssignProcessToJobObject = kernel32.NewProc("AssignProcessToJobObject")
	procTerminateJobObject       = kernel32.NewProc("TerminateJobObject")
	procOpenProcess              = kernel32.NewProc("OpenProcess")
	procCloseHandle              = kernel32.NewProc("CloseHandle")
)

const (
	jobObjectExtendedLimitInformationClass = 9
	jobObjectLimitKillOnJobClose           = 0x00002000

	// The least rights AssignProcessToJobObject accepts, so the harness never
	// takes an ability to read the child's memory it does not need.
	processSetQuota     = 0x0100
	processTerminate    = 0x0001
	processAssignRights = processSetQuota | processTerminate
)

// ioCounters mirrors IO_COUNTERS: six 64-bit totals.
type ioCounters struct {
	ReadOperationCount  uint64
	WriteOperationCount uint64
	OtherOperationCount uint64
	ReadTransferCount   uint64
	WriteTransferCount  uint64
	OtherTransferCount  uint64
}

// basicLimitInformation mirrors JOBOBJECT_BASIC_LIMIT_INFORMATION. The uintptr
// fields are SIZE_T, so the struct is sized for a 64-bit word.
type basicLimitInformation struct {
	PerProcessUserTimeLimit int64
	PerJobUserTimeLimit     int64
	LimitFlags              uint32
	MinimumWorkingSetSize   uintptr
	MaximumWorkingSetSize   uintptr
	ActiveProcessLimit      uint32
	Affinity                uintptr
	PriorityClassSection    uint32
	SchedulingClass         uint32
}

// extendedLimitInformation mirrors JOBOBJECT_EXTENDED_LIMIT_INFORMATION, the
// structure jobObjectExtendedLimitInformationClass takes.
type extendedLimitInformation struct {
	BasicLimitInformation basicLimitInformation
	IoInfo                ioCounters
	ProcessMemoryLimit    uintptr
	JobMemoryLimit        uintptr
	PeakProcessMemoryUsed uintptr
	PeakJobMemoryUsed     uintptr
}

// jobState is the job one child was assigned to.
//
// Tagging happens right after Start, after the watchdog goroutine may already be
// running, so the handle is published atomically: a kill that lands before the
// assignment falls back to the process handle, which is correct if narrower.
type jobState struct {
	handle atomic.Uintptr
}

// begin creates the job and puts the child in it. A failure leaves the state
// empty: the run proceeds and a timeout kills only the child itself, which is
// the fallback the previous implementation also had.
func (j *jobState) begin(process *os.Process) {
	if process == nil {
		return
	}
	job, _, _ := procCreateJobObjectW.Call(0, 0)
	if job == 0 {
		return
	}
	var info extendedLimitInformation
	info.BasicLimitInformation.LimitFlags = jobObjectLimitKillOnJobClose
	if ok, _, _ := procSetInformationJobObject.Call(job,
		jobObjectExtendedLimitInformationClass,
		uintptr(unsafe.Pointer(&info)), unsafe.Sizeof(info)); ok == 0 {
		_, _, _ = procCloseHandle.Call(job)
		return
	}
	// os.Process keeps its handle private, so the assignment opens its own with
	// the two rights AssignProcessToJobObject requires.
	handle, _, _ := procOpenProcess.Call(processAssignRights, 0, uintptr(process.Pid))
	if handle == 0 {
		_, _, _ = procCloseHandle.Call(job)
		return
	}
	ok, _, _ := procAssignProcessToJobObject.Call(job, handle)
	_, _, _ = procCloseHandle.Call(handle)
	if ok == 0 {
		// A nested job the OS refuses is not fatal: kill falls back to the handle.
		_, _, _ = procCloseHandle.Call(job)
		return
	}
	j.handle.Store(job)
}

// end closes the job handle, which takes down any descendant still running. The
// child itself has already exited or been killed by this point.
func (j *jobState) end() {
	if job := j.handle.Swap(0); job != 0 {
		_, _, _ = procCloseHandle.Call(job)
	}
}

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

// killProcessTree terminates the whole job, or the child alone when the job could
// not be created.
func killProcessTree(command *exec.Cmd, job *jobState) {
	if handle := job.handle.Load(); handle != 0 {
		_, _, _ = procTerminateJobObject.Call(handle, 1)
	}
	if command.Process != nil {
		_ = command.Process.Kill()
	}
}
