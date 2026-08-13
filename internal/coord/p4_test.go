package coord

// p4_test.go exercises the coordinator's teardown core: the entry lifecycle
// phase 5's rm sequences into the release verb. Each test is named for the
// exit criterion it proves.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// stubDriver is the teardown-path stub: a teardown-capable driver whose
// teardown fails on demand, recording the handles it was given — the
// registry's denormalised values, never anything read from the tree.
type stubDriver struct {
	teardownErr error
	tornDown    []string
}

func (d *stubDriver) Type() string          { return "namespace" }
func (d *stubDriver) HasApply() bool        { return false }
func (d *stubDriver) HasTeardown() bool     { return true }
func (d *stubDriver) GatesAllocation() bool { return false }

func (d *stubDriver) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	return table[r.Name].Value, nil
}

func (d *stubDriver) Probe(*spec.Resource, any, driver.Env) driver.ProbeResult {
	return driver.ProbeFree
}
func (d *stubDriver) Apply(*spec.Resource, any, driver.Env) (driver.ApplyResult, error) {
	return driver.ApplyResult{}, nil
}
func (d *stubDriver) Teardown(r *spec.Resource, value any, env driver.Env) error {
	d.tornDown = append(d.tornDown, r.Name)
	return d.teardownErr
}
func (d *stubDriver) Verify(*spec.Resource, any, driver.Env) ([]driver.Finding, error) {
	return nil, nil
}
func (d *stubDriver) BlastRadius(*spec.Resource, *spec.Spec) string { return "prose" }

// teardownSpec is the allocation spec the teardown tests run against: one
// compose namespace, no ports, so no band registration is needed.
func teardownSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "compose-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "namespace", Name: "compose", Kind: strPtr("compose"),
				Template: strPtr("{app}-{slug}-{slot}")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("teardown spec does not validate: %v", err)
	}
	return s
}

// setupTeardown allocates an entry and installs the given driver registry,
// returning the harness, session, spec and entry ref.
func setupTeardown(t *testing.T, reg driver.Registry) (*Harness, *Session, *spec.Spec, protocol.EntryRef) {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	h.H.InstallDrivers(reg)
	sp := teardownSpec(t)
	res, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	return h, sess, sp, protocol.EntryRef{App: res.App, Slug: res.Slug}
}

// TestCoordTeardownLeavesTearingDownAndHoldsTheSlot is exit criterion 3: a
// teardown that leaves resources behind moves the entry to tearing-down,
// the note lists exactly what survived, and the slot is not freed.
func TestCoordTeardownLeavesTearingDownAndHoldsTheSlot(t *testing.T) {
	stub := &stubDriver{teardownErr: &driver.TeardownError{Resource: "compose", Survivors: []driver.Survivor{
		{Kind: "container", Name: "c1", Resource: "compose", Reason: "removing it failed"},
	}}}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))

	resp := h.H.Teardown(sess, ref, sp, nil)
	if resp.Error == nil {
		t.Fatal("teardown with survivors must fail")
	}
	if resp.Error.Code != 1 {
		t.Errorf("exit code = %d, want 1 (partial failure)", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Msg, "c1") {
		t.Errorf("the error must name what survived: %q", resp.Error.Msg)
	}
	if resp.Error.Remedy == "" {
		t.Error("the error must carry a remedy")
	}

	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil {
		t.Fatal("the entry must survive the failed teardown")
	}
	if e.State != store.StateTearingDown {
		t.Errorf("state = %q, want tearing-down", e.State)
	}
	if !strings.Contains(e.TeardownNote, "c1") {
		t.Errorf("the entry's note must list what survived: %q", e.TeardownNote)
	}

	// The slot is not freed: the next allocation must not take slot 1.
	res2, perr := allocate(t, h, sess, sp, "wt-2")
	if perr != nil {
		t.Fatalf("second allocation refused: %+v", perr)
	}
	if res2.Slot != 2 {
		t.Errorf("second allocation took slot %d, want 2 (slot 1 must stay held)", res2.Slot)
	}

	// A re-run that now succeeds frees the entry: fix the cause, re-run.
	stub.teardownErr = nil
	resp = h.H.Teardown(sess, ref, sp, nil)
	if resp.Error != nil {
		t.Fatalf("re-run teardown refused: %+v", resp.Error)
	}
	reg, err = h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if registryEntry(reg, ref.App, ref.Slug) != nil {
		t.Error("the entry must drop once nothing survives")
	}
	if len(stub.tornDown) != 2 {
		t.Errorf("teardown ran %d times, want 2", len(stub.tornDown))
	}
}

// TestCoordTeardownUnavailableDoesNotFreeTheSlot: an unreachable daemon is
// exit code 4, the entry moves to tearing-down, and the slot stays held —
// an unavailable teardown blocks freeing the slot (plan.md §3).
func TestCoordTeardownUnavailableDoesNotFreeTheSlot(t *testing.T) {
	stub := &stubDriver{teardownErr: &driver.ErrUnavailable{Reason: "the docker daemon is unreachable"}}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))

	resp := h.H.Teardown(sess, ref, sp, nil)
	if resp.Error == nil || resp.Error.Code != 4 {
		t.Fatalf("teardown = %+v, want exit code 4 (unavailable)", resp.Error)
	}
	if !strings.Contains(resp.Error.Msg, "unreachable") {
		t.Errorf("the error must name the cause: %q", resp.Error.Msg)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if e := registryEntry(reg, ref.App, ref.Slug); e == nil || e.State != store.StateTearingDown {
		t.Fatalf("entry = %+v, want tearing-down", e)
	}
	res2, perr := allocate(t, h, sess, sp, "wt-2")
	if perr != nil || res2.Slot != 2 {
		t.Fatalf("the slot must stay held: second allocation = slot %d / %+v, want 2", res2.Slot, perr)
	}
}

// TestCoordTeardownReservedNamespaceRefused is exit criterion 4 at the
// coordinator: a namespace resolving to a host-globally reserved name is
// refused, naming the reservation, and the slot stays held.
func TestCoordTeardownReservedNamespaceRefused(t *testing.T) {
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(&driver.Namespace{}))

	// A person declares the co-resident production stack's project name once
	// per machine, in the ledger.
	resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
		Host: true, Names: []string{"compose-app-wt-1-1"},
		Note: "compose-app production stack (compose.prod.yaml)",
	})
	if resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}

	td := h.H.Teardown(sess, ref, sp, nil)
	if td.Error == nil {
		t.Fatal("teardown of a reserved namespace must be refused")
	}
	if td.Error.Code != 3 {
		t.Errorf("exit code = %d, want 3 (refused by a safety check)", td.Error.Code)
	}
	if !strings.Contains(td.Error.Msg, "compose-app production stack") {
		t.Errorf("the refusal must name the reservation: %q", td.Error.Msg)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if e := registryEntry(reg, ref.App, ref.Slug); e == nil || e.State != store.StateTearingDown {
		t.Fatalf("entry = %+v, want tearing-down", e)
	}
	if res2, perr := allocate(t, h, sess, sp, "wt-2"); perr != nil || res2.Slot != 2 {
		t.Fatalf("the slot must stay held: second allocation = slot %d / %+v, want 2", res2.Slot, perr)
	}
}

// TestCoordTeardownCleanDropsTheEntry: nothing survives → the entry drops
// and the slot frees.
func TestCoordTeardownCleanDropsTheEntry(t *testing.T) {
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(&stubDriver{}))

	resp := h.H.Teardown(sess, ref, sp, nil)
	if resp.Error != nil {
		t.Fatalf("clean teardown refused: %+v", resp.Error)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if registryEntry(reg, ref.App, ref.Slug) != nil {
		t.Error("the entry must drop after a clean teardown")
	}
	// The freed slot is allocatable again.
	res2, perr := allocate(t, h, sess, sp, "wt-2")
	if perr != nil || res2.Slot != 1 {
		t.Fatalf("the freed slot must be allocatable: slot %d / %+v, want 1", res2.Slot, perr)
	}
}

// TestCoordTeardownRequiresSpecAndOwnership: a teardown without the spec is
// refused (the dependent projects and the purge refusal cannot be computed),
// and a foreign client cannot tear down an entry it does not own.
func TestCoordTeardownRequiresSpecAndOwnership(t *testing.T) {
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(&stubDriver{}))

	t.Run("spec required", func(t *testing.T) {
		resp := h.H.Teardown(sess, ref, nil, nil)
		if resp.Error == nil || resp.Error.Code != 3 {
			t.Fatalf("spec-less teardown = %+v, want a refusal", resp.Error)
		}
	})

	t.Run("foreign client refused", func(t *testing.T) {
		other, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
		if reply.Error != nil {
			t.Fatalf("hello refused: %+v", reply.Error)
		}
		resp := h.H.Teardown(other, ref, sp, nil)
		if resp.Error == nil || resp.Error.Code != 3 {
			t.Fatalf("foreign teardown = %+v, want an ownership refusal", resp.Error)
		}
		if !strings.Contains(resp.Error.Msg, "owned by") {
			t.Errorf("the refusal must name the owner: %q", resp.Error.Msg)
		}
	})
}

// TestAgeingOutTearsDownByHandle: a client that dies after materialise and
// before activate leaves real objects whose only handle is the entry, so
// the ageing timer tears them down rather than deleting the entry from
// under them. A teardown that cannot finish leaves the entry tearing-down
// with its note, which is the state doctor, rm and reconcile repair.
func TestAgeingOutTearsDownByHandle(t *testing.T) {
	stub := &stubDriver{teardownErr: &driver.TeardownError{Resource: "compose", Survivors: []driver.Survivor{
		{Kind: "container", Name: "c1", Resource: "compose", Reason: "removing it failed"},
	}}}
	h, _, _, ref := setupTeardown(t, driver.NewRegistry(stub))

	// The entry is still reserving — the client never activated it.
	aged, err := h.H.AgeReserving(time.Now().UTC().Add(11*time.Minute), ReservingTimeout)
	if err != nil {
		t.Fatalf("AgeReserving: %v", err)
	}
	if aged != 1 {
		t.Fatalf("aged = %d, want 1", aged)
	}
	if len(stub.tornDown) != 1 || stub.tornDown[0] != "compose" {
		t.Errorf("tornDown = %v, want the entry's own resource torn down by handle", stub.tornDown)
	}

	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil {
		t.Fatal("the entry was dropped while a container survived; its handle is gone with it")
	}
	if e.State != store.StateTearingDown {
		t.Errorf("state = %q, want tearing-down", e.State)
	}
	if !strings.Contains(e.TeardownNote, "c1") {
		t.Errorf("the entry's note must list what survived: %q", e.TeardownNote)
	}
}

// TestAgeingOutDropsACleanTeardown: nothing survived, so the entry is
// dropped and the slot frees — the outcome the timer had before, now
// reached by tearing down first.
func TestAgeingOutDropsACleanTeardown(t *testing.T) {
	stub := &stubDriver{}
	h, _, _, ref := setupTeardown(t, driver.NewRegistry(stub))

	aged, err := h.H.AgeReserving(time.Now().UTC().Add(11*time.Minute), ReservingTimeout)
	if err != nil || aged != 1 {
		t.Fatalf("AgeReserving = %d, %v; want 1 aged out", aged, err)
	}
	if len(stub.tornDown) != 1 {
		t.Errorf("tornDown = %v, want the resource torn down before the entry was dropped", stub.tornDown)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if registryEntry(reg, ref.App, ref.Slug) != nil {
		t.Error("the entry survived a clean teardown; the slot is not freed")
	}
}

// strPtr points at a string literal for a pointer field.
func strPtr(s string) *string { return &s }
