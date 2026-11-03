//go:build windows

package exec

import (
	"context"
	"os/exec"
	"strconv"
	"syscall"
	"time"
)

// createNewProcessGroup puts the child in its own group, so a timeout can take
// down the whole tree.
const createNewProcessGroup = 0x00000200

// taskkillBudget bounds the tree kill, so a wedged taskkill cannot turn a timeout
// into a hang.
const taskkillBudget = 10 * time.Second

func configureProcessGroup(command *exec.Cmd) {
	command.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNewProcessGroup}
}

func killProcessTree(command *exec.Cmd) {
	if command.Process == nil {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), taskkillBudget)
	defer cancel()
	_ = exec.CommandContext(ctx, "taskkill", "/F", "/T", "/PID",
		strconv.Itoa(command.Process.Pid)).Run()
	// taskkill may fail on an exited or refusing process; killing the parent is
	// the fallback.
	_ = command.Process.Kill()
}
