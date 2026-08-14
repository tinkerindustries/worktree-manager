//go:build !windows

package platform

import (
	"errors"
)

// The Task Scheduler machinery is windows-only; off Windows these are the
// stubs the shared daemon.go dispatch compiles against. None is
// reachable: the callers switch on runtime.GOOS before calling them.

// windowsTaskDir is unreachable off windows.
func windowsTaskDir(prefix string) (string, error) {
	return "", errors.New("no Task Scheduler directory on this platform")
}

// taskSchedulerRunning reports false off windows.
func taskSchedulerRunning(prefix string) (bool, error) { return false, nil }

// installWindowsTask refuses off windows.
func installWindowsTask(prefix, wtdPath, addr, containerToken string) (InstallSupervisorResult, error) {
	return InstallSupervisorResult{}, errors.New("no Task Scheduler on this platform")
}
