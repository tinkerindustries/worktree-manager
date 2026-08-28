//go:build darwin

package platform

// machine_darwin.go is the macOS half of the VM runner seam: Colima
// profiles, each running its own dockerd (03-drivers.md §4.5). The live
// path cannot be exercised on the Linux implementation machine; the
// command shapes are the documented ones, and the driver's logic is proved
// against a fake runner (driver/machine_test.go).

import (
	"fmt"
	"os"
	"strings"
)

// Machine returns the platform's VM runner: Colima on macOS.
func Machine() MachineRunner { return colimaRunner{} }

// colimaRunner is the Colima shell-out. The helper binary is looked up per
// operation, so an absent Colima reports unavailable rather than panicking
// at startup.
type colimaRunner struct{}

// Binary names the helper.
func (colimaRunner) Binary() string { return "colima" }

// List parses `colima list --json` — one object per profile with its
// status. A missing binary or a daemon that cannot answer is
// ErrMachineUnavailable; a profile in any state other than Running does not
// count against the capacity guard.
func (colimaRunner) List() ([]MachineInstance, error) {
	cmd, err := HelperCommand("colima", "list", "--json")
	if err != nil {
		return nil, colimaUnavailable(err)
	}
	out, err := cmd.Output()
	if err != nil {
		return nil, HelperError("colima list", err)
	}
	instances, err := parseColimaList(out)
	if err != nil {
		return nil, fmt.Errorf("parsing colima list --json: %w", err)
	}
	return instances, nil
}

// Start launches `colima start <name>` in the background and returns
// immediately: warm-up is measured in minutes and must not block init
// (B4.4). The profile is created if it does not exist — colima start is
// the create-and-start command. The child's output follows the
// coordinator's stderr, which is where wtd's own log goes.
func (colimaRunner) Start(name string) error {
	cmd, err := HelperCommand("colima", "start", name)
	if err != nil {
		return colimaUnavailable(err)
	}
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Start()
}

// Delete runs the documented destroy command.
func (colimaRunner) Delete(name string) error {
	cmd, err := HelperCommand("colima", "delete", name, "--data", "--force")
	if err != nil {
		return colimaUnavailable(err)
	}
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("colima delete %s: %s", name, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteCommand is the documented bypass (B4.5).
func (colimaRunner) DeleteCommand(name string) string {
	return fmt.Sprintf("colima delete %s --data --force", name)
}

// colimaUnavailable turns a failed lookup into ErrMachineUnavailable,
// naming the locations searched. Homebrew installs colima into
// /opt/homebrew/bin, which is not on the PATH launchd gives the
// coordinator, so "not on PATH" alone was never the useful half.
func colimaUnavailable(err error) error {
	return machineUnavailable(err.Error() +
		"; install it with 'brew install colima docker' if it is absent, " +
		"or set " + HelperDirsEnv + " to the directory holding it")
}
