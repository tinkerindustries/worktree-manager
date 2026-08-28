//go:build windows

package platform

// machine_windows.go is the Windows half of the VM runner seam: WSL2
// distros, each running its own dockerd inside (03-drivers.md §4.5). The
// live path needs an interactive Windows desktop with WSL installed and
// cannot be exercised on the Linux implementation machine; the command
// shapes are the documented ones, the parser is defensive, and the
// driver's logic is proved against a fake runner (driver/machine_test.go).

import (
	"fmt"
	"os"
	"strings"
)

// Machine returns the platform's VM runner: WSL2 on Windows.
func Machine() MachineRunner { return wslRunner{} }

// wslRunner is the wsl.exe shell-out. The binary is looked up per
// operation, so an absent WSL reports unavailable rather than panicking at
// startup. The lookup goes through HelperCommand like every other helper:
// wsl.exe lives in System32 and resolves on any PATH, but a helper that is
// resolved one way and run another is the shape the colima PATH bug had.
type wslRunner struct{}

// Binary names the helper.
func (wslRunner) Binary() string { return "wsl" }

// List reports every distro with its running state, from two simple
// listings: `wsl --list --quiet` for the names and `wsl --list --running`
// for the running ones. The verbose table is not parsed — its columns
// depend on console width and codepage — which is why the two quiet
// listings are used instead.
func (wslRunner) List() ([]MachineInstance, error) {
	list, err := HelperCommand("wsl", "--list", "--quiet")
	if err != nil {
		return nil, wslUnavailable()
	}
	all, err := list.Output()
	if err != nil {
		return nil, HelperError("wsl --list --quiet", err)
	}
	listRunning, err := HelperCommand("wsl", "--list", "--running")
	if err != nil {
		return nil, wslUnavailable()
	}
	runningOut, err := listRunning.Output()
	if err != nil {
		return nil, HelperError("wsl --list --running", err)
	}
	running := map[string]bool{}
	for _, line := range parseWSLNames(string(runningOut)) {
		running[line] = true
	}
	var instances []MachineInstance
	for _, line := range parseWSLNames(string(all)) {
		instances = append(instances, MachineInstance{Name: line, Running: running[line]})
	}
	return instances, nil
}

// Start boots the distro by running a trivial command inside it, in the
// background: the VM warm-up must not block init (B4.4). Creating a distro
// is `wsl --import <name> <dir> <rootfs>` — a person's job, stated in the
// reference doc rather than attempted here; the driver starts distros the
// onboarding skill or a developer already created.
func (wslRunner) Start(name string) error {
	cmd, err := HelperCommand("wsl", "--distribution", name, "--exec", "/bin/true")
	if err != nil {
		return wslUnavailable()
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// Delete runs the documented destroy command.
func (wslRunner) Delete(name string) error {
	cmd, err := HelperCommand("wsl", "--unregister", name)
	if err != nil {
		return wslUnavailable()
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("wsl --unregister %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// wslUnavailable is the one answer for an absent wsl.exe, naming the
// install command.
func wslUnavailable() error {
	return machineUnavailable("wsl is not installed or not on PATH; install Windows Subsystem for Linux (wsl --install), then re-run")
}

// DeleteCommand is the documented bypass (B4.5).
func (wslRunner) DeleteCommand(name string) string {
	return fmt.Sprintf("wsl --unregister %s", name)
}
