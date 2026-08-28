//go:build !darwin && !windows

package platform

// machine_other.go is the runner seam on the platforms the design does not
// give a VM runner: Linux and anything else. Every operation is
// unavailable, so a spec declaring a machine resource reports the gap and
// names the platform that has a runner — a silent degrade reads as success
// (plan.md §3), so the driver never pretends a machine exists here.

import "io"

// Machine returns no runner on this platform.
func Machine() MachineRunner { return unsupportedMachine{} }

// unsupportedMachine is the runner seam's absent side.
type unsupportedMachine struct{}

// Binary names no helper.
func (unsupportedMachine) Binary() string { return "" }

// List cannot run.
func (unsupportedMachine) List() ([]MachineInstance, error) {
	return nil, machineUnavailable("the machine driver runs Colima on macOS and WSL2 on Windows")
}

// Start cannot run.
func (unsupportedMachine) Start(name string, output io.Writer) error {
	return machineUnavailable("the machine driver runs Colima on macOS and WSL2 on Windows")
}

// Delete cannot run.
func (unsupportedMachine) Delete(name string) error {
	return machineUnavailable("the machine driver runs Colima on macOS and WSL2 on Windows")
}

// DeleteCommand names no bypass: there is nothing to tear down by hand.
func (unsupportedMachine) DeleteCommand(name string) string { return "" }

// DockerEndpoint has no answer here either: this platform runs no
// per-worktree VM at all, so there is no separate daemon to name — the
// same "unsupported" sentinel WSL2 returns, and for the same reason the
// namespace driver treats them alike, falling back to the ambient docker
// seam it always used on this platform.
func (unsupportedMachine) DockerEndpoint(name string) (string, error) {
	return "", ErrDockerEndpointUnsupported
}
