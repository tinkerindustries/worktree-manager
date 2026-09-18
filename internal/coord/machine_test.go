package coord

// machine_test.go exercises the phase-8 machine wiring through the
// coordinator: the capacity guard's refusal surfacing as exit 3 on
// materialise (the refusal names what is running and how to tear one
// down), and the keep flag leaving the VM up through the rm verb with the
// note naming the documented bypass.

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/driver"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// machineSpec is the vm-app-shaped machine resource: one VM per worktree,
// capped at 4 concurrent instances, with the keep flag.
func machineSpec(t *testing.T, keepFlag string) *spec.Spec {
	t.Helper()
	s := &spec.Spec{
		Version: 1, App: "vm-app",
		Slots: spec.Slots{Max: intPtr(8)},
		Resources: []spec.Resource{{
			Type: "machine", Name: "vm", Driver: strPtr("auto"),
			Template: strPtr("{app}-{slug}-{slot}"), MaxConcurrent: intPtr(4),
		}},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if keepFlag != "" {
		s.Resources[0].KeepFlag = strPtr(keepFlag)
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("machine spec does not validate: %v", err)
	}
	return s
}

// coordFakeMachine is the coordinator tests' runner seam.
type coordFakeMachine struct {
	instances []platform.MachineInstance
	listErr   error
	started   []string
	deleted   []string
}

func (f *coordFakeMachine) Binary() string { return "colima" }
func (f *coordFakeMachine) List() ([]platform.MachineInstance, error) {
	if f.listErr != nil {
		return nil, f.listErr
	}
	return f.instances, nil
}
func (f *coordFakeMachine) Start(name string, output io.Writer) error {
	f.started = append(f.started, name)
	return nil
}
func (f *coordFakeMachine) Delete(name string) error {
	f.deleted = append(f.deleted, name)
	return nil
}
func (f *coordFakeMachine) DockerEndpoint(name string) (string, error) {
	return "unix:///fake/" + name + "/docker.sock", nil
}
func (f *coordFakeMachine) DeleteCommand(name string) string {
	return "colima delete " + name + " --data --force"
}

// setupMachineHarness builds the harness with the real driver registry
// and the fake runner installed.
func setupMachineHarness(t *testing.T, m *coordFakeMachine) (*Harness, *Session) {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	if m != nil {
		h.H.Machine = m
	}
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{},
		&driver.CIDR{}, &driver.Machine{}))
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	return h, sess
}

// allocateMachine allocates one entry and returns its slot.
func allocateMachine(t *testing.T, h *Harness, sess *Session, sp *spec.Spec, slug string) int {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: slug, Path: filepath.Join(t.TempDir(), slug),
		Description: "a vm worktree",
	})
	if resp.Error != nil {
		t.Fatalf("allocating %s: %v", slug, resp.Error)
	}
	var res api.AllocateResult
	mustUnmarshal(t, resp.Result, &res)
	return res.Slot
}

// TestCoordMaterialiseCapacityRefusalExits3 is the capacity guard's wire
// half: four instances are running and the entry's own is not among them,
// so the materialisation of a new instance is refused with exit 3, naming
// what is running and how to tear one down (B4.2; capacity reached is exit
// 3).
func TestCoordMaterialiseCapacityRefusalExits3(t *testing.T) {
	m := &coordFakeMachine{instances: []platform.MachineInstance{
		{Name: "vm-app-wt-a-1", Running: true},
		{Name: "vm-app-wt-b-2", Running: true},
		{Name: "vm-app-wt-c-3", Running: true},
		{Name: "vm-app-wt-d-4", Running: true},
	}}
	h, sess := setupMachineHarness(t, m)
	sp := machineSpec(t, "")
	slug := "wt-5"
	allocateMachine(t, h, sess, sp, slug)

	resp := h.Request(context.Background(), sess, verbMaterialise, &api.MaterialiseArgs{
		App: sp.App, Slug: slug, Spec: *sp,
	})
	if resp.Error == nil {
		t.Fatal("materialising a fifth instance past max_concurrent must be refused")
	}
	if resp.Error.Code != 3 {
		t.Fatalf("the capacity refusal must be exit 3, got %d (%s)", resp.Error.Code, resp.Error.Msg)
	}
	for _, want := range []string{"machine capacity reached", "vm-app-wt-a-1", "colima delete", "address pool"} {
		if !strings.Contains(resp.Error.Msg, want) {
			t.Errorf("the refusal must name %q: %s", want, resp.Error.Msg)
		}
	}
	if len(m.started) != 0 {
		t.Errorf("nothing may be started past the guard: %v", m.started)
	}
}

// TestCoordMaterialiseCapacityReRunAllowed: the entry's own instance is
// among the running ones — a re-run stays allowed at the limit, exactly as
// the design says (re-running an already-running profile is not a new
// instance).
func TestCoordMaterialiseCapacityReRunAllowed(t *testing.T) {
	m := &coordFakeMachine{instances: []platform.MachineInstance{
		// The entry's own instance — slug wt-5 lands on slot 1, so the
		// derived name is vm-app-wt-5-1 — is already running: a re-run.
		{Name: "vm-app-wt-5-1", Running: true},
		{Name: "vm-app-wt-b-2", Running: true},
		{Name: "vm-app-wt-c-3", Running: true},
		{Name: "vm-app-wt-d-4", Running: true},
	}}
	h, sess := setupMachineHarness(t, m)
	sp := machineSpec(t, "")
	slug := "wt-5"
	allocateMachine(t, h, sess, sp, slug)

	resp := h.Request(context.Background(), sess, verbMaterialise, &api.MaterialiseArgs{
		App: sp.App, Slug: slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("a re-run of a running instance must be allowed, got %v", resp.Error)
	}
	if len(m.started) != 0 {
		t.Errorf("a re-run must start nothing: %v", m.started)
	}
}

// TestCoordRmKeepVMLeavesTheInstanceUp is the B4.3 rail through the rm
// verb: with --keep-vm the teardown deletes nothing, the entry drops, and
// the note names the documented bypass so the deliberate survivor is never
// silent.
func TestCoordRmKeepVMLeavesTheInstanceUp(t *testing.T) {
	m := &coordFakeMachine{}
	h, sess := setupMachineHarness(t, m)
	sp := machineSpec(t, "--keep-vm")
	slug := "wt-1"
	allocateMachine(t, h, sess, sp, slug)

	// Materialise first, so the instance is started, then tear down with
	// the keep flag.
	if resp := h.Request(context.Background(), sess, verbMaterialise, &api.MaterialiseArgs{
		App: sp.App, Slug: slug, Spec: *sp,
	}); resp.Error != nil {
		t.Fatalf("materialise: %v", resp.Error)
	}
	if len(m.started) != 1 {
		t.Fatalf("started = %v, want the instance", m.started)
	}

	resp := h.Request(context.Background(), sess, "rm", &api.RmArgs{
		App: sp.App, Slug: slug, Spec: *sp, KeepFlags: []string{"--keep-vm"},
	})
	if resp.Error != nil {
		t.Fatalf("rm: %v", resp.Error)
	}
	var res api.RmResult
	mustUnmarshal(t, resp.Result, &res)
	if !res.Removed {
		t.Fatal("the entry must drop even though the VM is kept")
	}
	if len(m.deleted) != 0 {
		t.Errorf("the keep flag must leave the VM up: %v", m.deleted)
	}
	if len(res.Notes) != 1 {
		t.Fatalf("the keep must be stated: %v", res.Notes)
	}
	if !strings.Contains(res.Notes[0], "vm-app-wt-1-1") || !strings.Contains(res.Notes[0], "colima delete vm-app-wt-1-1 --data --force") {
		t.Errorf("the note must name the instance and the documented bypass: %s", res.Notes[0])
	}

	// Without the keep flag the instance is destroyed.
	resp = h.Request(context.Background(), sess, "rm", &api.RmArgs{
		App: sp.App, Slug: "wt-1", Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm of a missing entry is a data outcome, got %v", resp.Error)
	}
	var second api.RmResult
	mustUnmarshal(t, resp.Result, &second)
	if second.EntryFound {
		t.Fatal("the first rm dropped the entry; the second must find nothing")
	}
}

// poisonDocker fails or refuses every call, letting a test prove a code
// path never reaches the docker seam at all — the assertion items 3 and 4
// exist to make true in exactly this scenario (a namespace bound to a
// machine that no longer exists must never touch docker to discover that).
type poisonDocker struct{}

func (poisonDocker) poisoned() error                             { return errors.New("poisonDocker: this call must never happen") }
func (d poisonDocker) Version() error                            { return d.poisoned() }
func (d poisonDocker) ListContainers(string) ([]string, error)   { return nil, d.poisoned() }
func (d poisonDocker) ListNetworks(string) ([]string, error)     { return nil, d.poisoned() }
func (d poisonDocker) ListVolumes(string) ([]string, error)      { return nil, d.poisoned() }
func (d poisonDocker) ListNetworksAll() ([]string, error)        { return nil, d.poisoned() }
func (d poisonDocker) NetworkSubnet(string) (string, error)      { return "", d.poisoned() }
func (d poisonDocker) RemoveContainers([]string) error           { return d.poisoned() }
func (d poisonDocker) RemoveNetworks([]string) error             { return d.poisoned() }
func (d poisonDocker) RemoveVolumes([]string) error              { return d.poisoned() }
func (d poisonDocker) NetworkEndpoints(string) ([]string, error) { return nil, d.poisoned() }
func (d poisonDocker) DisconnectContainer(string, string) error  { return d.poisoned() }
func (d poisonDocker) WithHost(string) driver.Docker             { return d }

// machineNamespaceSpec binds a compose namespace to a machine resource —
// the shape items 3 and 4 exist for: a per-worktree VM and the compose
// project that lives inside it.
func machineNamespaceSpec(t *testing.T) *spec.Spec {
	t.Helper()
	s := &spec.Spec{
		Version: 1, App: "vm-compose-app",
		Slots: spec.Slots{Max: intPtr(8)},
		Resources: []spec.Resource{
			{Type: "machine", Name: "vm", Driver: strPtr("auto"), Template: strPtr("{app}-{slug}-{slot}")},
			{Type: "namespace", Name: "compose", Kind: strPtr("compose"),
				Template: strPtr("{app}-{slug}-{slot}"), Machine: strPtr("vm")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("machine+namespace spec does not validate: %v", err)
	}
	return s
}

// TestCoordRmVacuousWhenBoundMachineGoneEndToEnd proves items 3 and 4 wired
// together through the real coordinator, not just the driver in isolation:
// a namespace bound to a machine that was never started (or was deleted by
// hand) tears down without ever touching docker, and the entry drops
// cleanly. This is the fix for the wedge TeardownOrder's machine-last
// ordering otherwise produces — the namespace's own teardown would
// otherwise fail against a daemon that the next step deletes anyway, and
// no later `wt rm` could ever reach it.
func TestCoordRmVacuousWhenBoundMachineGoneEndToEnd(t *testing.T) {
	m := &coordFakeMachine{} // no instances: the bound machine does not exist
	h, sess := setupMachineHarness(t, m)
	h.H.Docker = poisonDocker{}
	sp := machineNamespaceSpec(t)
	slug := "wt-1"
	allocateMachine(t, h, sess, sp, slug)

	resp := h.Request(context.Background(), sess, "rm", &api.RmArgs{
		App: sp.App, Slug: slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm: %v", resp.Error)
	}
	var res api.RmResult
	mustUnmarshal(t, resp.Result, &res)
	if !res.Removed {
		t.Fatalf("a namespace whose bound machine is gone must be torn down vacuously and the entry must drop, got %+v", res)
	}
}

// TestDoctorMachineCapacityFinding is the deferred row of 06-fleet.md §4:
// "max_concurrent machines approaching" — the machine driver exists now,
// so doctor produces the finding, naming what is running and how to tear
// one down, and notes the bound when the runner cannot answer.
func TestDoctorMachineCapacityFinding(t *testing.T) {
	t.Run("approaching the limit is a finding", func(t *testing.T) {
		m := &coordFakeMachine{instances: []platform.MachineInstance{
			{Name: "vm-app-wt-a-1", Running: true},
			{Name: "vm-app-wt-b-2", Running: true},
			{Name: "vm-app-wt-c-3", Running: true},
		}}
		h, sess := setupMachineHarness(t, m)
		sp := machineSpec(t, "")
		// The entry's path must be stattable for doctor to reach the
		// spec-driven checks.
		dir := t.TempDir()
		allocateWithPath(t, h, sess, sp, "wt-1", filepath.Join(dir, "wt-1"))

		resp := h.Request(context.Background(), sess, verbDoctor, nil)
		if resp.Error != nil {
			t.Fatalf("doctor: %v", resp.Error)
		}
		var res api.DoctorResult
		mustUnmarshal(t, resp.Result, &res)
		matched := false
		for _, f := range res.Findings {
			if f.Level != "warning" || !strings.Contains(f.Message, "approaching the machine capacity") {
				continue
			}
			matched = true
			for _, want := range []string{"3 of 4", "vm-app-wt-a-1", "colima delete", "capacity guard"} {
				if !strings.Contains(f.Message, want) {
					t.Errorf("the finding must name %q: %s", want, f.Message)
				}
			}
			if f.Remedy == "" {
				t.Errorf("the finding must name its fix: %+v", f)
			}
		}
		if !matched {
			t.Errorf("no capacity finding:\n%s", docFindingsT(res.Findings))
		}
	})

	t.Run("well under the limit has no finding", func(t *testing.T) {
		m := &coordFakeMachine{instances: []platform.MachineInstance{
			{Name: "vm-app-wt-a-1", Running: true},
		}}
		h, sess := setupMachineHarness(t, m)
		sp := machineSpec(t, "")
		dir := t.TempDir()
		allocateWithPath(t, h, sess, sp, "wt-1", filepath.Join(dir, "wt-1"))

		resp := h.Request(context.Background(), sess, verbDoctor, nil)
		var res api.DoctorResult
		mustUnmarshal(t, resp.Result, &res)
		for _, f := range res.Findings {
			if strings.Contains(f.Message, "machine capacity") {
				t.Errorf("1 of 4 running must not be a capacity finding: %+v", f)
			}
		}
	})

	t.Run("an unanswerable runner states the bound", func(t *testing.T) {
		// The runner is a fake that cannot answer, not the platform's
		// real one. Passing nil left the check on whatever the host had:
		// on a machine with colima installed the note appeared only
		// because parseColimaList could not read what colima writes, so
		// fixing that parser turned this into a pass on one machine and a
		// failure on the next.
		h, sess := setupMachineHarness(t, &coordFakeMachine{
			listErr: errors.New("the colima daemon cannot answer"),
		})
		// The entry's path must sit inside a repo with the committed
		// spec, or doctor cannot reach the spec-driven checks.
		_, wt := sweepRepo(t, machineSpec(t, ""))
		sp := machineSpec(t, "")
		allocateWithPath(t, h, sess, sp, "wt-1", wt)

		resp := h.Request(context.Background(), sess, verbDoctor, nil)
		var res api.DoctorResult
		mustUnmarshal(t, resp.Result, &res)
		noted := false
		for _, n := range res.Notes {
			if strings.Contains(n, "machine capacity check was skipped") {
				noted = true
			}
		}
		if !noted {
			t.Errorf("an unanswerable runner must be stated in the notes: %v", res.Notes)
		}
	})
}

// allocateWithPath allocates one entry at an explicit path.
func allocateWithPath(t *testing.T, h *Harness, sess *Session, sp *spec.Spec, slug, path string) {
	t.Helper()
	if err := osMkdirAll(path); err != nil {
		t.Fatal(err)
	}
	resp := h.Request(context.Background(), sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: slug, Path: path,
		DescriptorPath: filepath.Join(path, "wt-env.yaml"), Description: "a doctor worktree",
	})
	if resp.Error != nil {
		t.Fatalf("allocating %s: %v", slug, resp.Error)
	}
}

// docFindingsT renders findings for an assertion message.
func docFindingsT(findings []api.DoctorFinding) string {
	var b strings.Builder
	for _, f := range findings {
		fmt.Fprintf(&b, "%s: %s\n", f.Level, f.Message)
	}
	return b.String()
}

// osMkdirAll is a thin wrapper so the helper reads naturally.
func osMkdirAll(path string) error { return os.MkdirAll(path, 0o700) }
