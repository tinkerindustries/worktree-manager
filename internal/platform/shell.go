package platform

// shell.go is the hook shell (08-platform.md §3, "hook execution"): the one
// place that decides what runs a `run:` command from wt.yaml.
//
// Hook commands are POSIX shell — the fixture's own start hook is `nohup
// ... >log 2>&1 &` — so the shell is `sh` on every platform, including
// Windows, where it comes from Git for Windows. Running them through
// cmd.exe would misread the same spec that works everywhere else. A Windows
// host without that shell is refused by name, with the remedy.

import (
	"context"
	"os/exec"
)

// ShellCommand builds the command that runs one hook line. The context
// carries the hook's declared timeout; pass context.Background() for a hook
// without one.
//
// It returns an error when the platform has no POSIX shell, so the caller
// reports a missing prerequisite rather than "exec: sh: executable file not
// found".
func ShellCommand(ctx context.Context, cmdline string) (*exec.Cmd, error) {
	sh, err := shellPath()
	if err != nil {
		return nil, err
	}
	return exec.CommandContext(ctx, sh, "-c", cmdline), nil
}
