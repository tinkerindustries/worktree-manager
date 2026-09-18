package coord

// p5_test.go exercises the phase-5 coordinator surface: the materialise
// verb (init's step 3) and the rm verb (reap + teardown + drop). Each
// test is named for the exit criterion or rail it proves; the reaper's
// live-signal tests live in reap_unix_test.go (unix only), and the live
// docker half lives in the acceptance-tagged files.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/driver"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
	"github.com/tinkerindustries/worktree-manager/internal/store"
)

// applyStub is an apply-capable stub driver whose apply fails on demand.
type applyStub struct {
	failOn      string // resource name whose apply fails
	applied     []string
	teardowns   []string
	teardownErr error
}

func (d *applyStub) Type() string          { return "state-path" }
func (d *applyStub) HasApply() bool        { return true }
func (d *applyStub) HasTeardown() bool     { return true }
func (d *applyStub) GatesAllocation() bool { return false }

func (d *applyStub) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	return table[r.Name].Value, nil
}
func (d *applyStub) Probe(*spec.Resource, any, driver.Env) driver.ProbeResult {
	return driver.ProbeFree
}
func (d *applyStub) Apply(r *spec.Resource, value any, env driver.Env) (driver.ApplyResult, error) {
	d.applied = append(d.applied, r.Name)
	if d.failOn == r.Name {
		return driver.ApplyResult{}, fmt.Errorf("apply of %s failed on purpose", r.Name)
	}
	return driver.ApplyResult{}, nil
}
func (d *applyStub) Teardown(r *spec.Resource, value any, env driver.Env) error {
	d.teardowns = append(d.teardowns, r.Name)
	return d.teardownErr
}
func (d *applyStub) Verify(*spec.Resource, any, driver.Env) ([]driver.Finding, error) {
	return nil, nil
}
func (d *applyStub) BlastRadius(*spec.Resource, *spec.Spec) string { return "prose" }

// materialiseSpec has two state-path resources in dependency order (db
// references state), so apply order and rollback order are observable.
func materialiseSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "compose-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "state-path", Name: "state",
				Template: strPtr("{home}/.wt-test/{slug}-{slot}/state")},
			{Type: "state-path", Name: "db",
				Template: strPtr("{state}/db.sqlite")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("materialise spec does not validate: %v", err)
	}
	return s
}

// allocMaterialise allocates one entry against the spec with the given
// driver registry installed.
func allocMaterialise(t *testing.T, reg driver.Registry) (*Harness, *Session, *spec.Spec, api.AllocateResult) {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	h.H.InstallDrivers(reg)
	sp := materialiseSpec(t)
	res, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	return h, sess, sp, res
}

// TestCoordMaterialiseAppliesInDependencyOrder: the materialise verb runs
// apply in dependency order (db after state) and leaves the entry
// reserving for the client to activate.
func TestCoordMaterialiseAppliesInDependencyOrder(t *testing.T) {
	stub := &applyStub{}
	h, sess, sp, res := allocMaterialise(t, driver.NewRegistry(stub))
	ctx := context.Background()

	resp := h.Request(ctx, sess, verbMaterialise, &api.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres api.MaterialiseResult
	decodeResult(t, resp, &mres)
	if mres.Failed != "" {
		t.Fatalf("materialise failed: %s: %s", mres.Failed, mres.Err)
	}
	if len(stub.applied) != 2 || stub.applied[0] != "state" || stub.applied[1] != "db" {
		t.Errorf("apply order = %v, want [state db]", stub.applied)
	}
	if mres.State != store.StateReserving {
		t.Errorf("state = %q, want reserving (the client activates)", mres.State)
	}
}

// TestCoordMaterialiseFailureCleanRollbackLeavesEntryReleasable is exit
// criterion 3's coordinator half: an apply failure part-way through rolls
// the applied resources back in reverse, the entry stays reserving, and
// the client can drop it with release — the tree is left exactly as it was
// found.
func TestCoordMaterialiseFailureCleanRollbackLeavesEntryReleasable(t *testing.T) {
	stub := &applyStub{failOn: "db"}
	h, sess, sp, res := allocMaterialise(t, driver.NewRegistry(stub))
	ctx := context.Background()

	resp := h.Request(ctx, sess, verbMaterialise, &api.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres api.MaterialiseResult
	decodeResult(t, resp, &mres)
	if mres.Failed != "db" {
		t.Fatalf("failed = %q, want db", mres.Failed)
	}
	if mres.State != store.StateReserving {
		t.Errorf("state = %q, want reserving after a clean rollback", mres.State)
	}
	if len(stub.teardowns) != 1 || stub.teardowns[0] != "state" {
		t.Errorf("rollback teardowns = %v, want [state] in reverse order", stub.teardowns)
	}

	// The client's rollback: release the entry — the slot frees and nothing
	// survives.
	resp = h.Request(ctx, sess, verbRelease, &api.EntryRef{App: res.App, Slug: res.Slug})
	if resp.Error != nil {
		t.Fatalf("release refused: %+v", resp.Error)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if registryEntry(reg, res.App, res.Slug) != nil {
		t.Error("the entry survived the release; the slot is not freed")
	}
}

// TestCoordMaterialiseFailureWithFailedRollbackMovesToTearingDown: an
// apply failure whose rollback teardown also fails leaves resources out
// there, so the entry moves to tearing-down with the note and the slot
// stays held — the client must not release what survived (B2.3).
func TestCoordMaterialiseFailureWithFailedRollbackMovesToTearingDown(t *testing.T) {
	stub := &applyStub{failOn: "db", teardownErr: fmt.Errorf("rollback teardown failed on purpose")}
	h, sess, sp, res := allocMaterialise(t, driver.NewRegistry(stub))
	ctx := context.Background()

	resp := h.Request(ctx, sess, verbMaterialise, &api.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres api.MaterialiseResult
	decodeResult(t, resp, &mres)
	if mres.State != store.StateTearingDown {
		t.Fatalf("state = %q, want tearing-down (the rollback left resources behind)", mres.State)
	}
	if mres.RollbackErr == "" {
		t.Error("rollback_err must name the failed teardown")
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	e := registryEntry(reg, res.App, res.Slug)
	if e == nil {
		t.Fatal("the entry is gone; the slot was freed with resources still out there")
	}
	if e.State != store.StateTearingDown || e.TeardownNote == "" {
		t.Errorf("entry = %+v, want tearing-down with a note naming what survived", e)
	}
}

// TestCoordMaterialiseRequiresOwnership: materialising another client's
// entry is refused like every mutating call.
func TestCoordMaterialiseRequiresOwnership(t *testing.T) {
	stub := &applyStub{}
	h, _, sp, res := allocMaterialise(t, driver.NewRegistry(stub))
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}
	resp := h.Request(context.Background(), other, verbMaterialise, &api.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("foreign materialise = %+v, want exit 3", resp.Error)
	}
	if len(stub.applied) != 0 {
		t.Errorf("the foreign client applied %v; nothing may be applied on its behalf", stub.applied)
	}
}

// TestAllocateRebuildsFromSlotHint is attach outcome 4's coordinator half:
// a descriptor's slot is sent as the hint and the entry is rebuilt at that
// slot — the descriptor beats the registry (ARCHITECTURE.md §8.6 rule 1).
func TestAllocateRebuildsFromSlotHint(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(api.KindHost, "")
	sp := materialiseSpec(t)
	// No band is needed: the spec has no port resources.

	resp := h.Request(context.Background(), sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: "wt-1", Path: "/tmp/wt/wt-1", SlotHint: 3,
	})
	if resp.Error != nil {
		t.Fatalf("rebuild allocation refused: %+v", resp.Error)
	}
	var res api.AllocateResult
	decodeResult(t, resp, &res)
	if res.Slot != 3 {
		t.Errorf("slot = %d, want the descriptor's 3", res.Slot)
	}
	if res.Existed {
		t.Error("Existed = true, want false for a fresh rebuild")
	}
}

// TestAllocateSlotHintHeldSlotRefused: a descriptor cannot claim a slot
// another entry holds.
func TestAllocateSlotHintHeldSlotRefused(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(api.KindHost, "")
	sp := materialiseSpec(t)
	if _, perr := allocate(t, h, sess, sp, "wt-1"); perr != nil {
		t.Fatalf("first allocation refused: %+v", perr)
	}
	resp := h.Request(context.Background(), sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: "wt-2", Path: "/tmp/wt/wt-2", SlotHint: 1,
	})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("slot-hint collision = %+v, want exit 3", resp.Error)
	}
	if !strings.Contains(resp.Error.Msg, "held") {
		t.Errorf("the refusal must name the collision: %q", resp.Error.Msg)
	}
}

// TestAllocateResultCarriesExistedPathAndShared: re-running allocate for
// an existing slug returns the entry (Existed, Path) and the shared block,
// never a reallocation — rule 5.
func TestAllocateResultCarriesExistedPathAndShared(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(api.KindHost, "")
	sp := materialiseSpec(t)
	sp.Shared = []spec.Shared{{Name: "{home}/.wt-test/db.sqlite", Impact: "shared writes"}}
	first, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("first allocation refused: %+v", perr)
	}
	second, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("re-allocation refused: %+v", perr)
	}
	if !second.Existed {
		t.Error("Existed = false on a re-run, want true")
	}
	if want := filepath.Join("/tmp/wt", "wt-1"); second.Path != want {
		t.Errorf("Path = %q, want the entry's recorded path (%q)", second.Path, want)
	}
	if second.Slot != first.Slot {
		t.Errorf("slot = %d, want the authoritative %d", second.Slot, first.Slot)
	}
	if len(second.Shared) != 1 || second.Shared[0].Name == "" || second.Shared[0].Impact == "" {
		t.Errorf("shared = %+v, want the resolved name-and-impact row", second.Shared)
	}
}

// --- the rm verb ---------------------------------------------------------

// TestRmVerbEntryNotFoundIsDataOutcome: rm of a slug with no entry is not
// an error — the client's no-entry paths decide (04-lifecycle.md §7.3).
func TestRmVerbEntryNotFoundIsDataOutcome(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(api.KindHost, "")
	sp := materialiseSpec(t)
	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: sp.App, Slug: "never-allocated", Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm of a missing entry = %+v, want a data outcome", resp.Error)
	}
	var res api.RmResult
	decodeResult(t, resp, &res)
	if res.EntryFound {
		t.Error("EntryFound = true for a slug with no entry")
	}
}

// TestRmVerbDryRunPreviewsWithoutTeardown: rm --dry-run returns the entry,
// the reap preview and the resources, and changes nothing.
func TestRmVerbDryRunPreviewsWithoutTeardown(t *testing.T) {
	stub := &stubDriver{}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))
	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp, DryRun: true,
	})
	if resp.Error != nil {
		t.Fatalf("rm --dry-run refused: %+v", resp.Error)
	}
	var res api.RmResult
	decodeResult(t, resp, &res)
	if !res.EntryFound || res.Removed {
		t.Errorf("result = %+v, want entry found and nothing removed", res)
	}
	if len(stub.tornDown) != 0 {
		t.Errorf("teardowns = %v under --dry-run; nothing may be torn down", stub.tornDown)
	}
	if len(res.Resources) != 1 || res.Resources[0] != "compose" {
		t.Errorf("resources = %v, want the entry's teardown list", res.Resources)
	}
}

// TestRmVerbSequencesReapThenTeardownAndDropsEntry: the real rm reaps,
// tears down, and drops the entry; a clean teardown frees the slot.
func TestRmVerbSequencesReapThenTeardownAndDropsEntry(t *testing.T) {
	stub := &stubDriver{}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))
	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm refused: %+v", resp.Error)
	}
	var res api.RmResult
	decodeResult(t, resp, &res)
	if !res.EntryFound || !res.Removed {
		t.Fatalf("result = %+v, want entry found and removed", res)
	}
	if len(stub.tornDown) != 1 {
		t.Errorf("teardowns = %v, want the entry's namespace torn down", stub.tornDown)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if registryEntry(reg, ref.App, ref.Slug) != nil {
		t.Error("the entry survived the rm; the slot is not freed")
	}
}

// TestRmVerbForeignEntryRefused: rm of another client's entry is refused,
// like every mutating call.
func TestRmVerbForeignEntryRefused(t *testing.T) {
	stub := &stubDriver{}
	h, _, sp, ref := setupTeardown(t, driver.NewRegistry(stub))
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}
	resp := h.Request(context.Background(), other, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp,
	})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("foreign rm = %+v, want exit 3", resp.Error)
	}
}

// TestRmVerbSurvivorsHoldTheSlot: a teardown that leaves resources behind
// keeps the slot held and reports the survivors (B2.3).
func TestRmVerbSurvivorsHoldTheSlot(t *testing.T) {
	stub := &stubDriver{teardownErr: &driver.TeardownError{Resource: "compose", Survivors: []driver.Survivor{
		{Kind: "container", Name: "c1", Resource: "compose", Reason: "still there"},
	}}}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))
	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp,
	})
	if resp.Error == nil || resp.Error.Code != 1 {
		t.Fatalf("rm with survivors = %+v, want exit 1", resp.Error)
	}
	if !strings.Contains(resp.Error.Msg, "survived") {
		t.Errorf("the error must name what survived: %q", resp.Error.Msg)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil || e.State != store.StateTearingDown {
		t.Errorf("entry = %+v, want tearing-down with the slot held", e)
	}
}

// TestRmVerbAbandonDropsTheEntryWithoutTearingDown is item 5b's core claim:
// --abandon recovers an entry no ordinary teardown can free (here, a driver
// that always reports a survivor — standing in for the resource-removed-
// from-the-spec case sequence.go's own survivor message is about) by
// dropping the registry row directly. The driver's Teardown must never be
// called: --abandon runs no driver at all.
func TestRmVerbAbandonDropsTheEntryWithoutTearingDown(t *testing.T) {
	stub := &stubDriver{teardownErr: &driver.TeardownError{Resource: "compose", Survivors: []driver.Survivor{
		{Kind: "container", Name: "c1", Resource: "compose", Reason: "still there"},
	}}}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))

	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp, Abandon: true,
	})
	if resp.Error != nil {
		t.Fatalf("rm --abandon refused: %+v", resp.Error)
	}
	var res api.RmResult
	decodeResult(t, resp, &res)
	if !res.EntryFound || !res.Removed {
		t.Fatalf("result = %+v, want entry found and removed", res)
	}
	if len(stub.tornDown) != 0 {
		t.Errorf("--abandon must run no driver's teardown, got %v", stub.tornDown)
	}
	if len(res.Notes) == 0 || !strings.Contains(res.Notes[0], "compose") {
		t.Errorf("the note must name what was abandoned: %v", res.Notes)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if registryEntry(reg, ref.App, ref.Slug) != nil {
		t.Error("the entry survived --abandon; the slot is not freed")
	}
}

// TestRmVerbAbandonDryRunPreviewsWithoutDropping: --abandon --dry-run names
// what would be abandoned and changes nothing, exactly like the ordinary
// preview.
func TestRmVerbAbandonDryRunPreviewsWithoutDropping(t *testing.T) {
	stub := &stubDriver{}
	h, sess, sp, ref := setupTeardown(t, driver.NewRegistry(stub))

	resp := h.Request(context.Background(), sess, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp, Abandon: true, DryRun: true,
	})
	if resp.Error != nil {
		t.Fatalf("rm --abandon --dry-run refused: %+v", resp.Error)
	}
	var res api.RmResult
	decodeResult(t, resp, &res)
	if !res.EntryFound || res.Removed {
		t.Errorf("result = %+v, want entry found and nothing removed", res)
	}
	if len(stub.tornDown) != 0 {
		t.Errorf("--abandon --dry-run must run no driver, got %v", stub.tornDown)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if registryEntry(reg, ref.App, ref.Slug) == nil {
		t.Error("the entry must survive a dry run")
	}
}

// TestRmVerbAbandonRequiresOwnership: --abandon is still a mutating call on
// the entry, so a foreign client is refused exactly like an ordinary rm.
func TestRmVerbAbandonRequiresOwnership(t *testing.T) {
	stub := &stubDriver{}
	h, _, sp, ref := setupTeardown(t, driver.NewRegistry(stub))
	other, err := h.ConnectPeer(5000, api.KindHost, "")
	if err != nil {
		t.Fatalf("second hello refused: %+v", err)
	}
	resp := h.Request(context.Background(), other, verbRm, &api.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp, Abandon: true,
	})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("foreign --abandon = %+v, want exit 3", resp.Error)
	}
}

// decodeResult decodes a response's result into v.
func decodeResult(t *testing.T, resp *api.Response, v any) {
	t.Helper()
	if resp.Error != nil {
		t.Fatalf("unexpected error: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, v); err != nil {
		t.Fatalf("decoding the result: %v", err)
	}
}

// freeCoordPort finds a port nothing is bound to right now.
func freeCoordPort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("finding a free port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	l.Close()
	return port
}
