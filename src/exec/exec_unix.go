//go:build !windows

package exec

import (
	"os"
	"os/exec"
	"syscall"
)

// jobState is the process group a child leads. Unix kills a group by addressing
// its leader's negative pid, so there is nothing to create up front and nothing
// to release.
type jobState struct{}

func (j *jobState) begin(*os.Process) {}

func (j *jobState) end() {}

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(command *exec.Cmd, _ *jobState) {
	if command.Process == nil {
		return
	}
	// A negative pid addresses the whole group, so descendants go with it.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}

// openProcess hands observers the pid. Unix accounting calls take a pid rather
// than a handle, and the pid stays addressable until the child is reaped -- which
// happens only after the observers have run, because they run after the wait but
// before this process' own exit.
//
// There is nothing to release: the kernel owns the pid.
func openProcess(pid int) Process {
	return Process{PID: pid}
}
