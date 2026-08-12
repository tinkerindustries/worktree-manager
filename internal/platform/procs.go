package platform

// procs.go is the process-group half of the hook runner (M8b): a hook is
// `sh -c <command>`, and a timeout that killed only the shell would orphan
// the command's children (a build, a compose up). The platform packages
// the "start in its own group, kill the whole group" pair so the client's
// hook runner never branches on GOOS (08-platform.md §2).

import (
	"os/exec"
)

// StartInOwnGroup prepares cmd to run in its own process group: on unix a
// group leader, so KillGroup can reach the command and its children
// together; on Windows a no-op (the Windows kill path is taskkill's tree
// form, 08-platform.md §3). The caller starts the command with Run/Start
// after calling this.
func StartInOwnGroup(cmd *exec.Cmd) { startInOwnGroup(cmd) }

// KillGroup terminates the command's process group — the command and every
// child it spawned. This is the "tracked by pid and stopped by pid" rail
// (B7.2): the group id is the command's own pid, never a name match.
func KillGroup(cmd *exec.Cmd) error { return killGroup(cmd) }
