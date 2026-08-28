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
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"strings"
	"time"
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
	// Start creates and starts the named instance. Warm-up is measured in
	// minutes and must not be waited on end to end (B4.4), so Start
	// detaches: once the child has run for machineStartGrace without
	// exiting, Start returns nil and leaves it running in the background —
	// the caller's apply returns immediately and the entry stays reserving
	// until materialisation finishes. A child that exits inside that
	// window is never a real boot finishing early; it is an instant
	// failure — a missing sibling binary, a corrupt profile, an invalid
	// name — and Start reports it as an error carrying the child's
	// captured output, rather than the false success a bare cmd.Start()
	// would have reported. Before this grace period existed, `colima
	// start` dying on a missing limactl looked identical to one that was
	// booting, and the driver recorded the resource as applied either way.
	//
	// output receives everything the child writes to stdout and stderr,
	// for as long as it runs — the coordinator wires its own per-instance
	// log file here, because a background process's output going to wtd's
	// own stderr is nowhere a person looks under a supervisor. output may
	// be nil, in which case the captured text is used only to build the
	// early-exit error and is then discarded.
	Start(name string, output io.Writer) error
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

// machineStartGrace is how long awaitMachineStart waits after a runner's
// start command has been spawned before declaring it successfully
// detached. A VM boot is measured in minutes, so this window is far too
// short to observe one finishing — that is deliberate, since waiting on
// the boot is exactly what Start must not do (B4.4). It exists only to
// give an instant failure (a missing sibling binary, a corrupt profile, an
// invalid instance name) time to surface: those exit in milliseconds, well
// inside the window, and a real boot is never still going to exit this
// early. A var rather than a const so a test can shrink it and observe
// both outcomes without waiting two seconds.
var machineStartGrace = 2 * time.Second

// MachineStartGrace reports the grace period awaitMachineStart waits out,
// so a caller composing a message about a start that survived it (the
// machine driver's apply note) names the same duration this package
// actually waits, rather than a hard-coded guess that could drift from it.
func MachineStartGrace() time.Duration { return machineStartGrace }

// awaitMachineStart runs an already-started command for machineStartGrace
// and reports what it saw. cmd must have had Start called on it already,
// with its Stdout and Stderr both routed through a buffer (out) so a
// failure's error can quote what the child wrote even when the caller
// supplied no destination writer of its own.
//
// A nil return means the child was still running when the window closed —
// exactly the outcome an unwatched cmd.Start() would have reported, and
// the caller leaves it running, detached, for the same reason a real boot
// takes minutes. A non-nil return means the child exited before the
// window closed. That is never a boot finishing early — every runner this
// package supports takes far longer than machineStartGrace to become
// ready — so any exit inside the window is reported as a failure, quoting
// whatever the child managed to write before it went.
func awaitMachineStart(cmd *exec.Cmd, out *bytes.Buffer) error {
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case werr := <-done:
		text := strings.TrimSpace(out.String())
		if werr == nil {
			werr = errors.New("exited before its warm-up could plausibly have finished")
		}
		if text == "" {
			return fmt.Errorf("exited during startup: %w", werr)
		}
		return fmt.Errorf("exited during startup: %w: %s", werr, text)
	case <-time.After(machineStartGrace):
		return nil
	}
}

// colimaProfile is one profile as `colima list --json` reports it.
type colimaProfile struct {
	Name   string `json:"name"`
	Status string `json:"status"`
}

// parseColimaList parses `colima list --json`. Colima writes one JSON
// object per line — a JSON stream, not a JSON array — so the array form
// the first implementation assumed never parsed and the machine driver
// could not list profiles at all on any machine with a profile on it.
// Both shapes are accepted here: the stream Colima emits today, and the
// array a document starting with "[" would be, because the two cost one
// branch and only one of them has ever been verified against a real
// Colima.
//
// Empty output means no profiles. A profile in any state other than
// Running does not count against the capacity guard.
func parseColimaList(out []byte) ([]MachineInstance, error) {
	trimmed := strings.TrimSpace(string(out))
	if trimmed == "" || trimmed == "[]" {
		return nil, nil
	}
	var profiles []colimaProfile
	if strings.HasPrefix(trimmed, "[") {
		if err := json.Unmarshal([]byte(trimmed), &profiles); err != nil {
			return nil, err
		}
	} else {
		dec := json.NewDecoder(strings.NewReader(trimmed))
		for {
			var p colimaProfile
			if err := dec.Decode(&p); err != nil {
				if errors.Is(err, io.EOF) {
					break
				}
				return nil, err
			}
			profiles = append(profiles, p)
		}
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
