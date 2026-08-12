package driver

// machine.go is the machine driver: a per-worktree VM or daemon — a
// Colima profile on macOS, a WSL2 distro on Windows, each running its own
// dockerd (03-drivers.md §4.5, B4.1). The driver selects by platform
// through internal/platform (the only GOOS-branching package) and accepts
// the schema's driver override, which only accepts "auto" today.
//
// The rails of §4.5:
//
//   - Apply creates and starts, with warm-up in the background: it is
//     measured in minutes, so the driver launches the runner's start and
//     returns immediately — init does not block on it, and the entry
//     stays reserving until materialisation finishes (B4.4).
//   - The capacity guard refuses a NEW instance past max_concurrent,
//     naming what is currently running and how to tear one down. The
//     count is the daemon's own instances — everything on the machine,
//     including instances made by hand and instances no registry entry
//     describes — because the constraint is on the machine (03-drivers.md
//     §8's open question, answered: counting the daemon's instances is
//     more truthful and more expensive, and it is the right trade). The
//     reason is carried into the message: multiple dockerds carve bridge
//     subnets from one address pool, and past roughly four the pool
//     exhausts and network creation fails as `all predefined address
//     pools have been fully subnetted`, which surfaces as something
//     entirely unrelated (ARCHITECTURE.md §2).
//   - Re-running an already-running profile stays allowed: it is not a
//     new instance.
//   - Teardown destroys the instance, unless the keep flag is given,
//     which drops the containers and the registry entry but leaves the
//     expensive VM up (B4.3).
//   - The documented bypass is supplied by this driver — the exact
//     command the driver's own teardown runs — so the generated
//     reference doc cannot drift from the implementation (B4.5).
//
// The live path cannot be exercised on this implementation machine (no
// Colima, no WSL2): the driver is built against the platform.MachineRunner
// seam, and the rails above are proved against a fake runner in
// machine_test.go. The live criterion is reported not_run.

import (
	"errors"
	"fmt"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// Machine is the machine driver.
type Machine struct{}

// Type reports the resource type.
func (*Machine) Type() string { return "machine" }

// HasApply reports that the driver creates and starts the instance.
func (*Machine) HasApply() bool { return true }

// HasTeardown reports that the driver destroys the instance.
func (*Machine) HasTeardown() bool { return true }

// GatesAllocation reports that a machine probe does not skip a candidate
// slot: allocation is not the capacity guard — the guard refuses a new
// instance at apply, where creation happens (03-drivers.md §7's
// failure-mode table).
func (*Machine) GatesAllocation() bool { return false }

// Derive returns the resolved instance name, exactly as spec.Resolve
// computes it.
func (*Machine) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	v, ok := table[r.Name]
	if !ok {
		return nil, fmt.Errorf("machine driver: no resolved value for resource %q", r.Name)
	}
	return v.Value, nil
}

// Probe reports whether an instance with the derived name already exists.
// A hit means a leftover from an incomplete teardown (or a re-run of a
// live one), not that the slot is taken — the driver does not gate
// allocation, so this is informational. Without a runner the probe is
// unavailable, which does not block allocation.
func (*Machine) Probe(r *spec.Resource, value any, env Env) ProbeResult {
	name, ok := value.(string)
	if !ok || name == "" {
		return ProbeUnavailable
	}
	instances, err := machineList(env)
	if err != nil {
		return ProbeUnavailable
	}
	for _, in := range instances {
		if in.Name == name {
			return ProbeHeld
		}
	}
	return ProbeFree
}

// Apply creates and starts the instance: the capacity guard first, then
// the background warm-up. The guard refuses a NEW instance past
// max_concurrent, naming what is currently running and how to tear one
// down; re-running an already-running profile stays allowed, because it is
// not a new instance (03-drivers.md §4.5, B4.2).
func (*Machine) Apply(r *spec.Resource, value any, env Env) (ApplyResult, error) {
	name, ok := value.(string)
	if !ok || name == "" {
		return ApplyResult{}, fmt.Errorf("machine driver: apply of resource %q: value %#v is not an instance name", r.Name, value)
	}
	if env.Machine == nil {
		return ApplyResult{}, &ErrUnavailable{Reason: "no VM runner is installed on this coordinator; the machine driver runs Colima on macOS and WSL2 on Windows"}
	}
	instances, err := env.Machine.List()
	if err != nil {
		if errors.Is(err, platform.ErrMachineUnavailable) {
			return ApplyResult{}, &ErrUnavailable{Reason: err.Error()}
		}
		return ApplyResult{}, fmt.Errorf("machine driver: listing the instances on this machine: %w", err)
	}

	max := spec.DefaultMachineMaxConcurrent
	if r.MaxConcurrent != nil && *r.MaxConcurrent >= 1 {
		max = *r.MaxConcurrent
	}
	running := make([]string, 0)
	alreadyRunning := false
	for _, in := range instances {
		if !in.Running {
			continue
		}
		if in.Name == name {
			alreadyRunning = true
		}
		running = append(running, in.Name)
	}
	sort.Strings(running)

	if len(running) >= max && !alreadyRunning {
		// The capacity guard: refuse the new instance, name what is
		// running and how to tear one down, and carry the reason into the
		// message — multiple dockerds carve bridge subnets from one
		// address pool, and past roughly four the pool exhausts and
		// network creation fails as `all predefined address pools have
		// been fully subnetted`, which surfaces as something entirely
		// unrelated (03-drivers.md §4.5, ARCHITECTURE.md §2).
		var parts []string
		for _, n := range running {
			parts = append(parts, fmt.Sprintf("%s (tear down with %q)", n, env.Machine.DeleteCommand(n)))
		}
		return ApplyResult{}, &RefusalError{Reason: fmt.Sprintf(
			"machine capacity reached: %d of %d instances are running (%s), and %q would be a new instance — multiple dockerds carve bridge subnets from one address pool, and past roughly four the pool exhausts (network creation fails as \"all predefined address pools have been fully subnetted\", which surfaces as something entirely unrelated); tear one down and re-run",
			len(running), max, strings.Join(parts, ", "), name)}
	}

	if alreadyRunning {
		return ApplyResult{Notes: []string{fmt.Sprintf("%s: the instance is already running; nothing to start", name)}}, nil
	}
	if err := env.Machine.Start(name); err != nil {
		if errors.Is(err, platform.ErrMachineUnavailable) {
			return ApplyResult{}, &ErrUnavailable{Reason: err.Error()}
		}
		return ApplyResult{}, fmt.Errorf("machine driver: starting %s: %w", name, err)
	}
	return ApplyResult{Notes: []string{fmt.Sprintf(
		"%s: %s start %s launched in the background — warm-up is measured in minutes, and init does not wait for it (B4.4)", name, env.Machine.Binary(), name)}}, nil
}

// Teardown destroys the instance, unless the keep flag is given — which
// drops the containers and the registry entry but leaves the expensive VM
// up (B4.3). The handle is the resolved instance name from the registry,
// never anything read from the worktree (03-drivers.md §2.2). An
// unavailable runner blocks freeing the slot, like every teardown
// unavailable (plan.md §3).
//
// The destroy is discover-before-destroy (03-drivers.md §2.1's rule
// generalised to the machine: a driver that does not create must
// discover): the runner is asked for its instances, and an instance that
// does not exist is a clean no-op. This is phase 9's R1 half — the
// coordinator's restart recovery tears down reserving entries whose
// materialisation may never have started, and a delete of a never-created
// profile (a wedge: nothing a person can delete) must not move the entry
// to tearing-down forever. It also fixes the same wedge for `rm` of a
// machine entry whose VM was destroyed by hand.
func (*Machine) Teardown(r *spec.Resource, value any, env Env) error {
	name, ok := value.(string)
	if !ok || name == "" {
		return fmt.Errorf("machine driver: teardown of resource %q: value %#v is not an instance name", r.Name, value)
	}
	if machineKept(r, env) {
		// --keep-vm: nothing to destroy here. The entry drops and the VM
		// stays up; the coordinator's note names the manual teardown
		// command (the documented bypass), so the leftover is never
		// silent.
		return nil
	}
	if env.Machine == nil {
		return &ErrUnavailable{Reason: "no VM runner is installed on this coordinator; the machine driver runs Colima on macOS and WSL2 on Windows"}
	}
	instances, err := machineList(env)
	if err != nil {
		if errors.Is(err, platform.ErrMachineUnavailable) {
			return &ErrUnavailable{Reason: err.Error()}
		}
		return &TeardownError{Resource: r.Name, Survivors: []Survivor{{
			Kind: "machine", Name: name, Resource: r.Name,
			Reason: fmt.Sprintf("checking whether it exists failed: %v; the documented bypass is %q", err, env.Machine.DeleteCommand(name)),
		}}}
	}
	exists := false
	for _, in := range instances {
		if in.Name == name {
			exists = true
			break
		}
	}
	if !exists {
		// Nothing to destroy: the instance was never created (the entry is
		// reserving and materialisation never ran, or the VM was deleted by
		// hand). A clean no-op, exactly like a namespace with no objects.
		return nil
	}
	if err := env.Machine.Delete(name); err != nil {
		if errors.Is(err, platform.ErrMachineUnavailable) {
			return &ErrUnavailable{Reason: err.Error()}
		}
		return &TeardownError{Resource: r.Name, Survivors: []Survivor{{
			Kind: "machine", Name: name, Resource: r.Name,
			Reason: fmt.Sprintf("deleting it failed: %v; the documented bypass is %q", err, env.Machine.DeleteCommand(name)),
		}}}
	}
	return nil
}

// Verify reports the drift a machine can carry: the instance the entry
// records does not exist on the machine, or cannot be checked from here —
// stated, never silent (the "VM missing" row of 06-fleet.md §4). Verify
// changes nothing.
func (*Machine) Verify(r *spec.Resource, value any, env Env) ([]Finding, error) {
	name, ok := value.(string)
	if !ok || name == "" {
		return nil, nil
	}
	instances, err := machineList(env)
	if err != nil {
		return []Finding{{Resource: r.Name, Level: LevelInfo,
			Message: fmt.Sprintf("the machine %s cannot be checked: %s", name, err)}}, nil
	}
	for _, in := range instances {
		if in.Name == name {
			return nil, nil // the instance exists, running or not
		}
	}
	return []Finding{{Resource: r.Name, Level: LevelWarning,
		Message: fmt.Sprintf("the machine %s does not exist on this machine — the VM is missing", name)}}, nil
}

// machineList asks the runner for every instance, mapping a missing runner
// to the unavailable error string.
func machineList(env Env) ([]platform.MachineInstance, error) {
	if env.Machine == nil {
		return nil, fmt.Errorf("no VM runner is installed on this coordinator; the machine driver runs Colima on macOS and WSL2 on Windows")
	}
	instances, err := env.Machine.List()
	if err != nil {
		if errors.Is(err, platform.ErrMachineUnavailable) {
			return nil, err
		}
		return nil, fmt.Errorf("listing the instances on this machine: %w", err)
	}
	return instances, nil
}

// machineKept reports whether the teardown's keep flag was passed for this
// resource: the spec declares the flag's name (keep_flag), and the CLI
// passes the names it accepted (env.KeepFlags), exactly like the
// state-path driver's purge flags.
func machineKept(r *spec.Resource, env Env) bool {
	if r.KeepFlag == nil {
		return false
	}
	for _, f := range env.KeepFlags {
		if f == *r.KeepFlag {
			return true
		}
	}
	return false
}

// BypassCommand is the documented manual teardown for one machine
// instance — the exact command the driver's own teardown runs, supplied by
// the driver so the generated reference doc cannot drift from the
// implementation (B4.5, 03-drivers.md §4.5). The artefact renderer reads
// it from here; the template hard-codes nothing. An empty string means the
// platform has no runner (the doc then names the platforms that do).
func BypassCommand(instance string) string {
	return platform.Machine().DeleteCommand(instance)
}

// BlastRadius is the shared-block prose for a machine resource. A machine
// cannot declare default: shared (the schema refuses the field), so this
// prose exists for the contract; it states the capacity rail.
func (*Machine) BlastRadius(r *spec.Resource, s *spec.Spec) string {
	return "each worktree's VM runs its own docker daemon, and the machine's address pool is shared: past the spec's max_concurrent the next new instance is refused until one is torn down"
}
