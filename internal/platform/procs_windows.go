//go:build windows

package platform

import (
	"os/exec"
	"strconv"
)

// startInOwnGroup is a no-op on Windows: there is no process-group kill in
// the portable surface, and taskkill's /T tree form (killGroup below) is
// the documented Windows path (08-platform.md §3).
func startInOwnGroup(cmd *exec.Cmd) {}

// killGroup terminates the command's tree with taskkill /T — the Windows
// analogue of the unix group kill, and the reason the graceful-step
// weakness documented in 08-platform.md §3 is confined to the TERM step.
func killGroup(cmd *exec.Cmd) error {
	if cmd.Process == nil {
		return nil
	}
	return exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(cmd.Process.Pid)).Run()
}
