//go:build darwin

package platform

// machine_darwin.go is the macOS half of the VM runner seam: Colima
// profiles, each running its own dockerd (03-drivers.md §4.5). The live
// path cannot be exercised on the Linux implementation machine; the
// command shapes are the documented ones, and the driver's logic is proved
// against a fake runner (driver/machine_test.go).

import (
	"bytes"
	"fmt"
	"io"
	"os"
	"path/filepath"
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

// Start launches `colima start <name>`, waits out the grace period
// (awaitMachineStart, in machine.go) and then returns, leaving the process
// running detached: warm-up is measured in minutes and must not block init
// (B4.4). The profile is created if it does not exist — colima start is
// the create-and-start command. output receives the child's stdout and
// stderr for as long as it runs; the coordinator wires its own
// per-instance log file here, which is where a person actually looks —
// before this grace period existed, the child's output went to wtd's own
// stderr, nowhere anyone looks under launchd, and a `colima start` that
// died on a missing limactl looked identical to one that was booting.
func (colimaRunner) Start(name string, output io.Writer) error {
	cmd, err := HelperCommand("colima", "start", name)
	if err != nil {
		return colimaUnavailable(err)
	}
	var buf bytes.Buffer
	w := io.Writer(&buf)
	if output != nil {
		w = io.MultiWriter(output, &buf)
	}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("colima start %s: %w", name, err)
	}
	if err := awaitMachineStart(cmd, &buf); err != nil {
		return fmt.Errorf("colima start %s %w", name, err)
	}
	return nil
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

// DockerEndpoint returns the profile's docker socket, from Colima's fixed
// layout: unix://$HOME/.colima/<profile>/docker.sock. Verified on this
// machine: `colima list --json` does not carry the socket at all, and
// `colima status <profile> --json` fails outright once the profile is
// stopped — the one time a namespace teardown needs the endpoint most,
// because the machine driver tears the VM down after the namespace inside
// it. The layout path works whether the instance is running or not, which
// is the reason it is the one used here over the alternative this package
// could have read instead: Colima also registers a docker context named
// colima-<profile> whose endpoint matches this same socket, but reading it
// means shelling out to `docker context inspect` — another process, on the
// critical path of every teardown, for a value this format string already
// gives for free. The two answers can be treated as a cross-check if this
// ever needs corroborating, but only one is worth paying for on every
// call.
func (colimaRunner) DockerEndpoint(name string) (string, error) {
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", fmt.Errorf("resolving the docker endpoint for colima profile %s: cannot determine the home directory: %w", name, err)
	}
	return "unix://" + filepath.Join(home, ".colima", name, "docker.sock"), nil
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
