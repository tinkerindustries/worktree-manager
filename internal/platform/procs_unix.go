//go:build darwin || linux

package platform

import (
	"os/exec"
	"syscall"
)

// startInOwnGroup makes the command a process-group leader.
func startInOwnGroup(cmd *exec.Cmd) {
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
}

// killGroup kills the whole process group: the negative pid form targets
// the group the command leads. ESRCH means the group is already gone.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
}
