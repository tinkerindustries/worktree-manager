//go:build !linux

package platform

import (
	"errors"
	"net"
)

// The systemd machinery is linux-only; off Linux these are the stubs the
// shared daemon.go dispatch compiles against. None is reachable: the
// callers switch on runtime.GOOS before calling them.

// systemdUserDir is unreachable off linux.
func systemdUserDir(prefix string) (string, error) {
	return "", errors.New("no systemd user directory on this platform")
}

// systemdRunning reports false off linux.
func systemdRunning(prefix string) (bool, error) { return false, nil }

// installSystemdUnits refuses off linux.
func installSystemdUnits(prefix, wtdPath, addr, containerToken string) (InstallSupervisorResult, error) {
	return InstallSupervisorResult{}, errors.New("no systemd on this platform")
}

// lingeringCaveat is empty off Linux: only the systemd user unit has the
// logout problem, and the launchd agent on macOS belongs to the GUI
// session by construction.
func lingeringCaveat(prefix string) string { return "" }

// ActivatedListener reports no activated socket off Linux: launchd socket
// activation is C-API-only and deliberately unused (daemon.go), and the
// Windows coordinator owns its pipe.
func ActivatedListener() (net.Listener, error) { return nil, nil }
