//go:build !windows

package platform

import "os/exec"

// hideConsole is a no-op off Windows: a child there has no console
// window to raise.
func hideConsole(cmd *exec.Cmd) {}
