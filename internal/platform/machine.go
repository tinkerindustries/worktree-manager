package platform

// machine.go is the platform's VM runner seam, the machine driver's
// platform half (03-drivers.md §4.5, M8): Colima profiles on macOS, WSL2
// distros on Windows, and no runner anywhere else. The driver selects by
// platform through this package and accepts an override (the schema's
// driver field, which only accepts "auto" today); the interface here is
// the one seam both the driver and its fake-runner tests see, exactly like
// the docker seam.
//
// The capacity constraint is on the machine, not on the registry
// (03-drivers.md §8's open question, answered in phase 8: count the
// daemon's own instances), so List reports every instance that exists —
// created by hand or by the tool — and the driver counts the running ones.

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
)

// MachineInstance is one existing VM or daemon as the platform's runner
// reports it.
type MachineInstance struct {
	// Name is the instance's name: a Colima profile or a WSL2 distro.
	Name string
	// Running reports whether the instance's daemon is up right now. The
	// capacity guard counts running instances, because it is running
	// dockerds that carve bridge subnets from one address pool
	// (03-drivers.md §4.5, B4.2).
	Running bool
}

// MachineRunner is the platform's per-worktree VM runner. Every operation
// that needs the helper binary reports its absence as an error the driver
// maps to unavailable; the caller never branches on GOOS.
type MachineRunner interface {
	// Binary is the helper's base name: "colima" or "wsl".
	Binary() string
	// List reports every instance on the machine, running or not.
	List() ([]MachineInstance, error)
	// Start creates and starts the named instance without waiting for it
	// to be ready. Warm-up is measured in minutes and runs in the
	// background (B4.4): the caller's apply returns immediately and the
	// entry stays reserving until materialisation finishes.
	Start(name string) error
	// Delete destroys the named instance and its data.
	Delete(name string) error
	// DeleteCommand is the documented manual bypass for the platform —
	// the exact command the driver runs and the generated reference doc
	// names, so the doc cannot drift from the implementation (B4.5,
	// 03-drivers.md §4.5).
	DeleteCommand(name string) string
}

// ErrMachineUnavailable is what every runner operation returns when the
// helper cannot run at all: the binary is absent, or the platform has no
// runner. The driver maps it to its unavailable result, which does not
// block allocation and does block freeing the slot on teardown. The
// sentinel's own text is the generic marker; the concrete reason is
// appended by machineUnavailable, so the message is never doubled.
var ErrMachineUnavailable = errors.New("no VM runner available on this platform")

// parseColimaList parses `colima list --json` — one object per profile
// with its status. The empty document "[]" is valid and means no
// profiles; anything else that does not parse is unavailable. A profile
// in any state other than Running does not count against the capacity
// guard.
func parseColimaList(out []byte) ([]MachineInstance, error) {
	if strings.TrimSpace(string(out)) == "[]" {
		return nil, nil
	}
	var profiles []struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &profiles); err != nil {
		return nil, err
	}
	instances := make([]MachineInstance, 0, len(profiles))
	for _, p := range profiles {
		if p.Name == "" {
			continue
		}
		instances = append(instances, MachineInstance{Name: p.Name, Running: p.Status == "Running"})
	}
	return instances, nil
}

// parseWSLNames splits a `wsl --list --quiet` (or --running) listing into
// distro names, dropping the marker column ("*"), blank lines and anything
// that is not a name. Output encoding from wsl.exe can vary by codepage; a
// line that does not survive is skipped rather than failing the whole
// count.
func parseWSLNames(out string) []string {
	var names []string
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		line = strings.TrimPrefix(line, "*")
		line = strings.TrimSpace(line)
		if line == "" || strings.ContainsAny(line, " \t") {
			continue
		}
		names = append(names, line)
	}
	return names
}

// machineUnavailable wraps a reason in ErrMachineUnavailable, the marker
// the driver maps to unavailable (does not block allocation; does block
// freeing the slot on teardown).
func machineUnavailable(reason string) error {
	return fmt.Errorf("%w: %s", ErrMachineUnavailable, reason)
}
