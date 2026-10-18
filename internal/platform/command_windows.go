//go:build windows

package platform

import (
	"os/exec"
	"syscall"
)

// createNoWindow is CREATE_NO_WINDOW: the child gets a console with no
// window. DETACHED_PROCESS is deliberately not used — a detached child
// has no console at all, and its own console children would be given a
// visible one again, which is the bug this exists to prevent.
const createNoWindow = 0x08000000

var getConsoleWindow = syscall.NewLazyDLL("kernel32.dll").NewProc("GetConsoleWindow")

// hideConsole applies Command's rule to cmd for this process.
func hideConsole(cmd *exec.Cmd) {
	hideConsoleUnless(cmd, hasConsole())
}

// hasConsole reports whether this process is attached to a console.
// GetConsoleWindow answers NULL exactly when it is not: wtd started by
// the logon task, never a terminal it was run from, whose pseudo-console
// has a window handle of its own.
func hasConsole() bool {
	hwnd, _, _ := getConsoleWindow.Call()
	return hwnd != 0
}

// hideConsoleUnless is hideConsole's testable core. A parent with a
// console leaves cmd alone, so the child shares it. Otherwise the flag
// and SW_HIDE are merged into whatever SysProcAttr the caller already
// set, never replacing it.
func hideConsoleUnless(cmd *exec.Cmd, parentHasConsole bool) {
	if parentHasConsole {
		return
	}
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{}
	}
	cmd.SysProcAttr.HideWindow = true
	cmd.SysProcAttr.CreationFlags |= createNoWindow
}
