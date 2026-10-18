//go:build windows

package platform

import (
	"os/exec"
	"syscall"
	"testing"
)

// TestHideConsoleWithoutParentConsole is wtd's case: a process with no
// console of its own starts its child with CREATE_NO_WINDOW and SW_HIDE,
// merged into what the caller set rather than replacing it.
func TestHideConsoleWithoutParentConsole(t *testing.T) {
	cmd := exec.Command("git", "--version")
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: syscall.CREATE_NEW_PROCESS_GROUP}
	hideConsoleUnless(cmd, false)
	if !cmd.SysProcAttr.HideWindow {
		t.Error("HideWindow not set")
	}
	if cmd.SysProcAttr.CreationFlags&createNoWindow == 0 {
		t.Errorf("CreationFlags = %#x, want CREATE_NO_WINDOW", cmd.SysProcAttr.CreationFlags)
	}
	if cmd.SysProcAttr.CreationFlags&syscall.CREATE_NEW_PROCESS_GROUP == 0 {
		t.Errorf("CreationFlags = %#x: the caller's CREATE_NEW_PROCESS_GROUP was dropped", cmd.SysProcAttr.CreationFlags)
	}

	bare := exec.Command("git", "--version")
	hideConsoleUnless(bare, false)
	if bare.SysProcAttr == nil || bare.SysProcAttr.CreationFlags != createNoWindow || !bare.SysProcAttr.HideWindow {
		t.Errorf("SysProcAttr = %+v, want CREATE_NO_WINDOW and HideWindow", bare.SysProcAttr)
	}
}

// TestHideConsoleWithParentConsole is wt's case, and wtd's when it is run
// from a terminal: the child shares the parent's console, untouched.
func TestHideConsoleWithParentConsole(t *testing.T) {
	cmd := exec.Command("git", "--version")
	hideConsoleUnless(cmd, true)
	if cmd.SysProcAttr != nil {
		t.Errorf("SysProcAttr = %+v, want nil", cmd.SysProcAttr)
	}
}
