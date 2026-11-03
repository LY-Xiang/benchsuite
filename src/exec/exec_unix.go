//go:build !windows

package exec

import (
	"os/exec"
	"syscall"
)

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

func killProcessTree(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	// A negative pid addresses the whole group, so descendants go with it.
	_ = syscall.Kill(-command.Process.Pid, syscall.SIGKILL)
}
