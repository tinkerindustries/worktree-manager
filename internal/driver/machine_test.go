package driver

// machine_test.go proves the machine driver's rails against a fake
// platform.MachineRunner: the capacity guard (refuse a NEW instance past
// max_concurrent, naming what is running and how to tear one down;
// re-running an already-running profile stays allowed), the background
// warm-up not blocking apply, the keep flag leaving the VM up, and the
// bypass command the driver supplies. The live path — real Colima on
// macOS, real WSL2 on Windows — cannot run on this Linux implementation
// machine and is reported not_run; the seam is the platform.MachineRunner
// interface, with the real shell-outs on one side (internal/platform) and
// this fake on the other.

import (
	"errors"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// fakeMachine is the runner seam's test side: a fixed set of instances,
// a log of what was started and deleted, and optional failures.
type fakeMachine struct {
	instances []platform.MachineInstance
	listErr   error
	startErr  error
	deleteErr error
	started   []string
	deleted   []string
	binary    string
}

func newFakeMachine() *fakeMachine {
	return &fakeMachine{binary: "colima"}
}

func (f *fakeMachine) Binary() string { return f.binary }
func (f *fakeMachine) List() ([]platform.MachineInstance, error) {
	return f.instances, f.listErr
}
func (f *fakeMachine) Start(name string) error {
	if f.startErr != nil {
		return f.startErr
	}
	f.started = append(f.started, name)
	return nil
}
func (f *fakeMachine) Delete(name string) error {
	if f.deleteErr != nil {
		return f.deleteErr
	}
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *fakeMachine) DeleteCommand(name string) string {
	return f.binary + " delete " + name + " --data --force"
}

// machineFixture builds the vm-app-shaped machine resource: one VM per
// worktree, capped at 4 concurrent instances (B4.2's guard).
func machineFixture(t *testing.T, m platform.MachineRunner, keepFlag bool) (*spec.Spec, Env, *spec.Resource) {
	t.Helper()
	max := 100
	resources := []spec.Resource{{
		Type: "machine", Name: "vm", Driver: strPtr("auto"),
		Template: strPtr("{app}-{slug}-{slot}"), MaxConcurrent: intPtr(4),
	}}
	if keepFlag {
		resources[0].KeepFlag = strPtr("--keep-vm")
	}
	s := &spec.Spec{
		Version: 1, App: "vm-app",
		Slots:     spec.Slots{Max: &max},
		Resources: resources,
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("fixture spec does not validate: %v", err)
	}
	ctx := spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the fixture: %v", err)
	}
	env := Env{
		Spec: s, App: "vm-app", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
		Resolved: table, Machine: m,
	}
	return s, env, resourceByName(s, "vm")
}

func machineValue(t *testing.T, s *spec.Spec) string {
	t.Helper()
	table, err := spec.Resolve(s, spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	})
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	return table["vm"].Value.(string)
}

// TestMachineApplyStartsInBackground is the apply contract: create and
// start without blocking — the fake's Start returns immediately, as the
// real runner's backgrounded colima start does — and the note says warm-up
// runs in the background and init does not wait (B4.4).
func TestMachineApplyStartsInBackground(t *testing.T) {
	m := newFakeMachine()
	_, env, res := machineFixture(t, m, false)
	name := machineValue(t, env.Spec)

	ar, err := (&Machine{}).Apply(res, name, env)
	if err != nil {
		t.Fatalf("Apply: %v", err)
	}
	if len(m.started) != 1 || m.started[0] != name {
		t.Fatalf("started = %v, want %s", m.started, name)
	}
	if len(ar.Notes) != 1 {
		t.Fatalf("the apply must state the background warm-up: %v", ar.Notes)
	}
	if !strings.Contains(ar.Notes[0], "background") || !strings.Contains(ar.Notes[0], "does not wait") {
		t.Errorf("the note must state the background warm-up: %s", ar.Notes[0])
	}
}

// TestMachineCapacityGuardRefusesNewInstance is the capacity rail (B4.2):
// past max_concurrent a NEW instance is refused, naming what is currently
// running and how to tear one down, and carrying the reason into the
// message — multiple dockerds carve bridge subnets from one address pool.
func TestMachineCapacityGuardRefusesNewInstance(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{
		{Name: "vm-app-wt-5-5", Running: true},
		{Name: "vm-app-wt-6-6", Running: true},
		{Name: "vm-app-wt-7-7", Running: true},
		{Name: "vm-app-wt-8-8", Running: true},
		// A fifth, made by hand — the count is the daemon's own instances,
		// not the registry's entries (03-drivers.md §8's open question,
		// answered).
		{Name: "hand-made-vm", Running: true},
	}
	_, env, res := machineFixture(t, m, false)
	name := machineValue(t, env.Spec)

	_, err := (&Machine{}).Apply(res, name, env)
	if !isRefusal(err) {
		t.Fatalf("a new instance past max_concurrent must be refused, got %v", err)
	}
	for _, want := range []string{
		"machine capacity reached", "5 of 4", name,
		"tear down with", "hand-made-vm", "address pool", "fully subnetted",
	} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the refusal must name %q: %v", want, err)
		}
	}
	if len(m.started) != 0 {
		t.Errorf("nothing may be started past the guard: %v", m.started)
	}
}

// TestMachineCapacityGuardNamesEachRunningInstance: every running instance
// gets its teardown command in the refusal, so the remedy is actionable
// for each one.
func TestMachineCapacityGuardNamesEachRunningInstance(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{
		{Name: "vm-app-wt-5-5", Running: true},
		{Name: "vm-app-wt-6-6", Running: true},
		{Name: "vm-app-wt-7-7", Running: true},
		{Name: "vm-app-wt-8-8", Running: true},
	}
	_, env, res := machineFixture(t, m, false)
	_, err := (&Machine{}).Apply(res, machineValue(t, env.Spec), env)
	if !isRefusal(err) {
		t.Fatalf("expected a refusal, got %v", err)
	}
	for _, n := range []string{"vm-app-wt-5-5", "vm-app-wt-6-6", "vm-app-wt-7-7", "vm-app-wt-8-8"} {
		if !strings.Contains(err.Error(), m.DeleteCommand(n)) {
			t.Errorf("the refusal must name the teardown command for %s: %v", n, err)
		}
	}
}

// TestMachineReRunOfRunningProfileAllowed is the other half of the guard:
// re-running an already-running profile stays allowed, because it is not a
// new instance — even at the limit, an init re-run on a live worktree
// succeeds and starts nothing.
func TestMachineReRunOfRunningProfileAllowed(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{
		{Name: "vm-app-wt-1-1", Running: true},
		{Name: "vm-app-wt-2-2", Running: true},
		{Name: "vm-app-wt-3-3", Running: true},
		{Name: "vm-app-wt-4-4", Running: true},
	}
	_, env, res := machineFixture(t, m, false)

	// The entry being re-run is wt-1's own instance, already running.
	spec2 := *env.Spec
	spec2.Resources[0].Template = strPtr("{app}-wt-1-1")
	table, err := spec.Resolve(&spec2, spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	})
	if err != nil {
		t.Fatalf("resolving: %v", err)
	}
	env.Spec = &spec2
	env.Resolved = table
	name := table["vm"].Value.(string)
	if name != "vm-app-wt-1-1" {
		t.Fatalf("fixture sanity: %s", name)
	}

	ar, err := (&Machine{}).Apply(res, name, env)
	if err != nil {
		t.Fatalf("a re-run of a running profile must be allowed, got %v", err)
	}
	if len(m.started) != 0 {
		t.Errorf("a re-run must not start anything: %v", m.started)
	}
	if len(ar.Notes) != 1 || !strings.Contains(ar.Notes[0], "already running") {
		t.Errorf("the re-run must say the instance is already running: %v", ar.Notes)
	}
}

// TestMachineCapacityCountsDaemonInstancesNotRegistryEntries: instances
// with no registry entry — created by hand, or by a view that is gone —
// count against the limit, because the constraint is on the machine
// (03-drivers.md §8's open question: counting the daemon's instances is
// more truthful and more expensive, and it is the right trade).
func TestMachineCapacityCountsDaemonInstancesNotRegistryEntries(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{
		{Name: "vm-app-wt-5-5", Running: true},
		{Name: "vm-app-wt-6-6", Running: true},
		{Name: "vm-app-wt-7-7", Running: true},
		{Name: "vm-app-wt-8-8", Running: true},
		// Four entries' worth of registry would say 4; the daemon says 5.
		{Name: "vm-app-old-view-9", Running: true},
	}
	_, env, res := machineFixture(t, m, false)
	_, err := (&Machine{}).Apply(res, machineValue(t, env.Spec), env)
	if !isRefusal(err) {
		t.Fatalf("the daemon's own count must refuse, got %v", err)
	}
	if !strings.Contains(err.Error(), "vm-app-old-view-9") {
		t.Errorf("the refusal must name the hand-made instance: %v", err)
	}
}

// TestMachineCapacityGuardDoesNotCountStoppedInstances: a stopped profile
// is not carving a subnet, so it neither fills the limit nor is named.
func TestMachineCapacityGuardDoesNotCountStoppedInstances(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{
		{Name: "vm-app-wt-1-1", Running: true},
		{Name: "vm-app-wt-2-2", Running: true},
		{Name: "vm-app-wt-3-3", Running: true},
		{Name: "vm-app-wt-4-4", Running: false},
	}
	_, env, res := machineFixture(t, m, false)
	ar, err := (&Machine{}).Apply(res, machineValue(t, env.Spec), env)
	if err != nil {
		t.Fatalf("a stopped profile must not fill the limit: %v", err)
	}
	if len(ar.Notes) == 0 {
		t.Error("the apply must carry its note")
	}
}

// TestMachineApplyUnavailableWithoutRunner: no runner installed (this
// Linux machine) is unavailable — the materialisation names the platforms
// that have one rather than pretending a machine exists.
func TestMachineApplyUnavailableWithoutRunner(t *testing.T) {
	_, env, res := machineFixture(t, nil, false)
	_, err := (&Machine{}).Apply(res, machineValue(t, env.Spec), env)
	if !isErrUnavailable(err) {
		t.Fatalf("Apply without a runner = %v, want unavailable", err)
	}
	if !strings.Contains(err.Error(), "Colima") || !strings.Contains(err.Error(), "WSL2") {
		t.Errorf("the unavailable reason must name the platforms: %v", err)
	}
}

// TestMachineApplyUnavailableWhenRunnerCannotList: the runner cannot
// answer (colima absent, daemon down) — the guard cannot count, so apply
// is unavailable, never a blind start.
func TestMachineApplyUnavailableWhenRunnerCannotList(t *testing.T) {
	m := newFakeMachine()
	m.listErr = platform.ErrMachineUnavailable
	_, env, res := machineFixture(t, m, false)
	_, err := (&Machine{}).Apply(res, machineValue(t, env.Spec), env)
	if !isErrUnavailable(err) {
		t.Fatalf("Apply with an unanswerable runner = %v, want unavailable", err)
	}
	if len(m.started) != 0 {
		t.Errorf("nothing may be started when the count cannot be taken: %v", m.started)
	}
}

// TestMachineTeardownDeletesUnlessKeepFlag is the B4.3 rail: teardown
// destroys the instance, unless the keep flag is given — which drops the
// containers and the entry but leaves the expensive VM up.
func TestMachineTeardownDeletesUnlessKeepFlag(t *testing.T) {
	t.Run("teardown deletes", func(t *testing.T) {
		m := newFakeMachine()
		_, env, res := machineFixture(t, m, false)
		name := machineValue(t, env.Spec)
		if err := (&Machine{}).Teardown(res, name, env); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if len(m.deleted) != 1 || m.deleted[0] != name {
			t.Fatalf("deleted = %v, want %s", m.deleted, name)
		}
	})

	t.Run("the keep flag leaves the VM up", func(t *testing.T) {
		m := newFakeMachine()
		_, env, res := machineFixture(t, m, true)
		name := machineValue(t, env.Spec)
		env.KeepFlags = []string{"--keep-vm"}
		if err := (&Machine{}).Teardown(res, name, env); err != nil {
			t.Fatalf("Teardown with the keep flag: %v", err)
		}
		if len(m.deleted) != 0 {
			t.Errorf("the keep flag must leave the VM up: %v", m.deleted)
		}
	})

	t.Run("without the flag name the VM still goes", func(t *testing.T) {
		m := newFakeMachine()
		_, env, res := machineFixture(t, m, true)
		name := machineValue(t, env.Spec)
		env.KeepFlags = []string{"--some-other-flag"}
		if err := (&Machine{}).Teardown(res, name, env); err != nil {
			t.Fatalf("Teardown: %v", err)
		}
		if len(m.deleted) != 1 {
			t.Errorf("a non-matching keep flag must not protect the VM: %v", m.deleted)
		}
	})
}

// TestMachineTeardownUnavailableBlocksTheSlot: an unavailable teardown
// (no runner) blocks freeing the slot, like every teardown unavailable
// (plan.md §3).
func TestMachineTeardownUnavailableBlocksTheSlot(t *testing.T) {
	_, env, res := machineFixture(t, nil, false)
	err := (&Machine{}).Teardown(res, machineValue(t, env.Spec), env)
	if !isErrUnavailable(err) {
		t.Fatalf("Teardown without a runner = %v, want unavailable", err)
	}
}

// TestMachineTeardownFailureReportsSurvivorAndBypass: a delete that ran
// and failed is a survivor, and the reason names the documented bypass —
// the exact command a person runs when the helper cannot.
func TestMachineTeardownFailureReportsSurvivorAndBypass(t *testing.T) {
	m := newFakeMachine()
	m.deleteErr = errors.New("profile is in use")
	_, env, res := machineFixture(t, m, false)
	name := machineValue(t, env.Spec)
	err := (&Machine{}).Teardown(res, name, env)
	if !isTeardownError(err) {
		t.Fatalf("Teardown = %v, want a TeardownError", err)
	}
	te := err.(*TeardownError)
	if len(te.Survivors) != 1 || te.Survivors[0].Name != name || te.Survivors[0].Kind != "machine" {
		t.Fatalf("survivors = %+v, want the machine", te.Survivors)
	}
	if !strings.Contains(te.Survivors[0].Reason, m.DeleteCommand(name)) {
		t.Errorf("the survivor reason must name the documented bypass: %+v", te.Survivors[0])
	}
}

// TestMachineBypassCommandSuppliedByDriver is the B4.5 rail: the driver
// supplies the exact command for its platform — the reference doc reads it
// from here, so the doc cannot drift from the implementation. On this
// Linux machine the platform has no runner, and the driver says so rather
// than inventing one.
func TestMachineBypassCommandSuppliedByDriver(t *testing.T) {
	cmd := BypassCommand("vm-app-wt-1-1")
	if cmd == "" {
		return // linux: no runner; the doc names the platforms instead
	}
	if !strings.Contains(cmd, "vm-app-wt-1-1") {
		t.Errorf("the bypass command must name the instance: %q", cmd)
	}
}

// TestMachineOrderingForcedFirstIs the 03-drivers.md §5 forcing rule: a VM
// has to exist before anything expects its daemon, so machine applies
// first among its dependents — even when nothing template-references it —
// and tears down last.
func TestMachineOrderingForcedFirst(t *testing.T) {
	s := &spec.Spec{
		Version: 1, App: "vm-app",
		Slots: spec.Slots{Max: intPtr(4)},
		Resources: []spec.Resource{
			// Declared first, but the machine must still apply before it:
			// the cidr's network lives inside the VM's docker.
			{Type: "cidr", Name: "egress", Pool: strPtr("172.30.0.0/16"), Size: intPtr(22)},
			{Type: "machine", Name: "vm", Driver: strPtr("auto"),
				Template: strPtr("{app}-{slug}-{slot}"), MaxConcurrent: intPtr(4)},
			{Type: "state-path", Name: "db", Template: strPtr("{home}/{slug}/db.sqlite")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("spec: %v", err)
	}
	reg := NewRegistry(&Machine{}, &StatePath{})

	order := reg.ApplyOrder(s)
	// machine first among the apply-capable resources, whatever the
	// declaration order.
	applyIdx := -1
	for i, name := range order {
		if name == "vm" {
			applyIdx = i
			break
		}
	}
	if applyIdx != 0 {
		t.Fatalf("ApplyOrder = %v, want machine first (a VM must exist before anything expects its daemon)", order)
	}

	teardown := reg.TeardownOrder(s)
	if len(teardown) != 2 || teardown[0] != "db" || teardown[1] != "vm" {
		t.Fatalf("TeardownOrder = %v, want [db vm] — the VM is destroyed last", teardown)
	}
}

// TestMachineProbeAndVerify: the probe reports an existing instance, and
// verify reports the VM-missing drift (the "VM missing" row of
// 06-fleet.md §4).
func TestMachineProbeAndVerify(t *testing.T) {
	m := newFakeMachine()
	m.instances = []platform.MachineInstance{{Name: "vm-app-wt-1-1", Running: true}}
	_, env, res := machineFixture(t, m, false)
	name := machineValue(t, env.Spec)

	t.Run("an existing instance probes held", func(t *testing.T) {
		if got := (&Machine{}).Probe(res, name, env); got != ProbeHeld {
			t.Errorf("Probe = %v, want held", got)
		}
	})
	t.Run("a fresh name probes free", func(t *testing.T) {
		if got := (&Machine{}).Probe(res, "vm-app-wt-9-9", env); got != ProbeFree {
			t.Errorf("Probe = %v, want free", got)
		}
	})
	t.Run("no runner is unavailable", func(t *testing.T) {
		env.Machine = nil
		if got := (&Machine{}).Probe(res, name, env); got != ProbeUnavailable {
			t.Errorf("Probe = %v, want unavailable", got)
		}
		env.Machine = m
	})
	t.Run("verify reports the missing VM", func(t *testing.T) {
		findings, err := (&Machine{}).Verify(res, "vm-app-wt-9-9", env)
		if err != nil {
			t.Fatalf("Verify: %v", err)
		}
		if len(findings) != 1 || findings[0].Level != LevelWarning ||
			!strings.Contains(findings[0].Message, "does not exist") {
			t.Fatalf("findings = %+v, want the VM-missing warning", findings)
		}
	})
	t.Run("verify is quiet for a present instance", func(t *testing.T) {
		findings, err := (&Machine{}).Verify(res, name, env)
		if err != nil || len(findings) != 0 {
			t.Errorf("a present instance must verify clean, got %v %v", findings, err)
		}
	})
	t.Run("verify states the cannot-check bound", func(t *testing.T) {
		m.listErr = platform.ErrMachineUnavailable
		findings, err := (&Machine{}).Verify(res, name, env)
		if err != nil || len(findings) != 1 || findings[0].Level != LevelInfo {
			t.Errorf("an unanswerable runner must be stated, got %v %v", findings, err)
		}
	})
}
