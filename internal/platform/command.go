package platform

// command.go is the one constructor every child process goes through.
//
// On Windows wtd is linked -H=windowsgui (dist/build.sh), so it has no
// console, and Windows gives every console-subsystem child of a process
// without one a console of its own — a visible window, and on a machine
// whose default terminal is Windows Terminal, a Terminal window — for as
// long as the child runs. git, gh, netstat, tasklist and schtasks each
// flashed one. The child is started with CREATE_NO_WINDOW instead: it
// still gets a console, so its pipes and its own children behave as
// before, and the console has no window. A process that has a console of
// its own (wt, or wtd run from a terminal) is left alone, so its children
// keep sharing that console: the terminal's Ctrl+C still reaches them and
// a prompt still reaches the person at it.

import (
	"context"
	"os/exec"
)

// Command is exec.Command for a child that must not raise a console
// window. Every call site outside this file uses it, or CommandContext,
// in place of the os/exec constructors; a test holds the line.
func Command(name string, args ...string) *exec.Cmd {
	cmd := exec.Command(name, args...)
	hideConsole(cmd)
	return cmd
}

// CommandContext is exec.CommandContext with Command's console rule.
func CommandContext(ctx context.Context, name string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, name, args...)
	hideConsole(cmd)
	return cmd
}
