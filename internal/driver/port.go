package driver

// port.go is the port driver: an integer derived from the band ledger's base
// for this resource, probed by binding on loopback, never applied (a port is
// not a thing that exists) and never torn down. The two rails of
// 03-drivers.md §4.1 bind it: the stride form caps slots at 99, which spec
// validate enforces and this driver must not contradict; and a held port is
// never remediated — the allocator skips the slot and says which one,
// because whatever holds the port is probably the developer's own running
// instance (B1.6, ARCHITECTURE.md §12.1).

import (
	"fmt"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// Port is the port driver.
type Port struct{}

// Type reports the resource type.
func (*Port) Type() string { return "port" }

// HasApply reports that a port is not created.
func (*Port) HasApply() bool { return false }

// HasTeardown reports that a port is not destroyed.
func (*Port) HasTeardown() bool { return false }

// GatesAllocation reports that a held port skips a candidate slot.
func (*Port) GatesAllocation() bool { return true }

// Derive returns the port for this resource's band base, in the two forms
// ARCHITECTURE.md §8.4 fixes: stride is base + slot, group is
// base + slot×size + offset. spec.Resolve implements both; the driver calls
// it rather than deriving a second time, so it cannot contradict the
// stride-form ceiling of 99 slots that spec validate enforces (M2 §6.1).
func (*Port) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	v, ok := table[r.Name]
	if !ok {
		return nil, fmt.Errorf("port driver: no resolved value for resource %q", r.Name)
	}
	return v.Value, nil
}

// Probe binds the port on loopback and releases it immediately. An
// address-in-use error means the port is held by something outside the
// registry; a bind that cannot even be attempted is unavailable, which does
// not block allocation — the registry remains the only check performed and
// the caller says so (03-drivers.md §4.1).
func (*Port) Probe(r *spec.Resource, value any, env Env) ProbeResult {
	port, ok := value.(int)
	if !ok {
		return ProbeUnavailable
	}
	if err := platform.ProbeBind(port); err != nil {
		if platform.IsAddrInUse(err) {
			return ProbeHeld
		}
		return ProbeUnavailable
	}
	return ProbeFree
}

// Apply is the optional half's absent side: a port is not created.
func (*Port) Apply(*spec.Resource, any, Env) (ApplyResult, error) { return ApplyResult{}, nil }

// Teardown is the optional half's absent side: a port is not a thing that
// exists (03-drivers.md §4.1).
func (*Port) Teardown(*spec.Resource, any, Env) error { return nil }

// Verify reports whether the port is bound. Naming the holder is listener
// discovery, which the phase-5 reaper owns (plan.md phase 4: no lsof this
// phase), so a bound port is reported with the holder unidentified rather
// than silently passed.
func (*Port) Verify(r *spec.Resource, value any, env Env) ([]Finding, error) {
	port, ok := value.(int)
	if !ok {
		return nil, fmt.Errorf("port driver: verify of resource %q: value is %T, want int", r.Name, value)
	}
	switch p := (&Port{}).Probe(r, value, env); p {
	case ProbeFree:
		return []Finding{{Resource: r.Name, Kind: "port-free", Level: LevelInfo, Message: fmt.Sprintf("port %d is free", port)}}, nil
	case ProbeHeld:
		return []Finding{{Resource: r.Name, Kind: "port-bound", Level: LevelWarning,
			Message: fmt.Sprintf("port %d is bound, and this check does not identify the holder", port)}}, nil
	default:
		return []Finding{{Resource: r.Name, Kind: "port-unprobeable", Level: LevelWarning,
			Message: fmt.Sprintf("port %d could not be probed (the bind could not be attempted); treat it as unverified", port)}}, nil
	}
}

// BlastRadius is the shared-block prose for a port resource. When a port is
// shared, every worktree derives the same number and the second stack to
// bind fails to start — nothing is silently wrong, which is why the port
// collision is the requirement's baseline rather than its worst case.
func (*Port) BlastRadius(r *spec.Resource, s *spec.Spec) string {
	return "ports are not isolated: every worktree derives the same port and the second stack to bind fails to start"
}
