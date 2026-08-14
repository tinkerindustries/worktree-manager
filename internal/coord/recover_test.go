package coord

// recover_test.go proves phase 9's R1 exit criterion: upgrading the
// coordinator while an entry is reserving leaves that entry recoverable.
// The restart is simulated the way the real one works — a new Handler over
// the same store path, the same way cmd/wtd's startup runs
// RecoverInterrupted before serving — and every recovery outcome is
// asserted: a materialised half is torn down by handle and the entry
// drops, a never-materialised entry drops without touching anything, an
// unavailable teardown moves the entry to tearing-down with the slot held,
// and an entry whose spec cannot be found moves to tearing-down with the
// note naming the fix. Active and tearing-down entries are never touched
// by the pass.

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// restartHarness builds a fresh coordinator over the same store path — the
// restart — with the same fake runner carrying over the machine's state.
func restartHarness(t *testing.T, root string, m *coordFakeMachine) *Harness {
	t.Helper()
	h := NewHarness(t, root)
	if m != nil {
		h.H.Machine = m
	}
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{},
		&driver.CIDR{}, &driver.Machine{}))
	return h
}

// vmName reads the entry's resolved machine name.
func vmName(t *testing.T, h *Harness, app, slug string) string {
	t.Helper()
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	for i := range reg.Entries {
		if reg.Entries[i].App == app && reg.Entries[i].Slug == slug {
			v, ok := reg.Entries[i].Resources["vm"]
			if !ok {
				t.Fatalf("entry %s has no vm resource: %+v", slug, reg.Entries[i].Resources)
			}
			name, ok := v.Value.(string)
			if !ok || name == "" {
				t.Fatalf("vm value = %#v, want a name", v.Value)
			}
			return name
		}
	}
	t.Fatalf("no entry for %s/%s", app, slug)
	return ""
}

// TestRecoverInterruptedOnRestartTearsDownAndDrops is the R1 exit
// criterion: an entry mid-materialisation when the coordinator is
// upgraded. The VM the machine driver started exists; the new coordinator
// tears it down by handle and drops the entry, so the slot is free and the
// next init allocates fresh — recoverable rather than wedged.
func TestRecoverInterruptedOnRestartTearsDownAndDrops(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := restartHarness(t, root, nil)
	m := &coordFakeMachine{}
	h1.H.Machine = m
	sess, err := h1.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	sp := machineSpec(t, "")
	allocateMachine(t, h1, sess, sp, "wt-1")
	resp := h1.Request(context.Background(), sess, verbMaterialise, &api.MaterialiseArgs{
		App: sp.App, Slug: "wt-1", Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise: %v", resp.Error)
	}
	// The machine driver starts in the background (B4.4): by the time the
	// process dies the VM exists.
	name := vmName(t, h1, sp.App, "wt-1")
	m.instances = []platform.MachineInstance{{Name: name, Running: true}}

	// The restart: a new coordinator on the same store, its own runner
	// seeing the same machine.
	m2 := &coordFakeMachine{instances: m.instances}
	h2 := restartHarness(t, root, m2)

	n, rerr := h2.H.RecoverInterrupted()
	if rerr != nil {
		t.Fatalf("RecoverInterrupted: %v", rerr)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}
	if len(m2.deleted) != 1 || m2.deleted[0] != name {
		t.Errorf("the VM must be torn down by handle: deleted = %v, want [%s]", m2.deleted, name)
	}
	reg, rerr := h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 0 {
		t.Fatalf("registry after recovery = %+v, want empty", reg.Entries)
	}

	// The client's next init allocates fresh — the same slot, since nothing
	// holds it.
	sess2, err2 := h2.Connect(api.KindHost, "")
	if err2 != nil {
		t.Fatalf("second hello refused: %+v", err2)
	}
	slot := allocateMachine(t, h2, sess2, sp, "wt-1")
	if slot != 1 {
		t.Errorf("re-allocated slot = %d, want 1 (the slot was freed)", slot)
	}
}

// TestRecoverInterruptedNeverMaterialisedDropsCleanly: the client died
// before materialise ever ran — the common reserving case. Recovery drops
// the entry without touching any driver, and the slot frees.
func TestRecoverInterruptedNeverMaterialisedDropsCleanly(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := restartHarness(t, root, nil)
	m := &coordFakeMachine{}
	h1.H.Machine = m
	sess, err := h1.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	sp := machineSpec(t, "")
	allocateMachine(t, h1, sess, sp, "wt-1")

	h2 := restartHarness(t, root, &coordFakeMachine{})
	n, rerr := h2.H.RecoverInterrupted()
	if rerr != nil {
		t.Fatalf("RecoverInterrupted: %v", rerr)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}
	reg, rerr := h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 0 {
		t.Fatalf("registry after recovery = %+v, want empty", reg.Entries)
	}
}

// TestRecoverInterruptedTeardownUnavailableMovesToTearingDown: the
// teardown cannot run at startup (the runner cannot answer), so the entry
// moves to tearing-down with the note and the slot stays held — the
// resting state doctor, rm and reconcile repair. The owner's blind
// rollback release is refused: releasing would orphan the survivors.
func TestRecoverInterruptedTeardownUnavailableMovesToTearingDown(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := restartHarness(t, root, nil)
	m := &coordFakeMachine{instances: []platform.MachineInstance{{Name: "vm-app-wt-1-1", Running: true}}}
	h1.H.Machine = m
	sess, err := h1.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	sp := machineSpec(t, "")
	allocateMachine(t, h1, sess, sp, "wt-1")

	// The restarted coordinator's runner cannot answer: teardown is
	// unavailable, which blocks freeing the slot (plan.md §3).
	m2 := &coordFakeMachine{instances: []platform.MachineInstance{{Name: "vm-app-wt-1-1", Running: true}}}
	h2 := restartHarness(t, root, m2)
	m2.listErr = platform.ErrMachineUnavailable

	n, rerr := h2.H.RecoverInterrupted()
	if rerr != nil {
		t.Fatalf("RecoverInterrupted: %v", rerr)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1 (the entry stopped being reserving)", n)
	}
	reg, rerr := h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 1 {
		t.Fatalf("registry = %+v, want the entry held", reg.Entries)
	}
	e := reg.Entries[0]
	if e.State != store.StateTearingDown {
		t.Errorf("state = %q, want tearing-down", e.State)
	}
	if e.TeardownNote == "" {
		t.Error("the entry must carry a note naming what survived")
	}

	// The owning client's rollback release must not orphan the survivors.
	owner, err2 := h2.Connect(api.KindHost, "")
	if err2 != nil {
		t.Fatalf("hello refused: %+v", err2)
	}
	resp := h2.Request(context.Background(), owner, verbRelease, &api.EntryRef{App: sp.App, Slug: "wt-1"})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("release of a tearing-down entry = %+v, want a refusal", resp.Error)
	}
	if !strings.Contains(resp.Error.Remedy, "wt rm") {
		t.Errorf("the refusal must name the teardown re-run: %s", resp.Error.Remedy)
	}
	reg, rerr = h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].State != store.StateTearingDown {
		t.Errorf("the refused release must leave the entry held: %+v", reg.Entries)
	}
}

// TestRecoverInterruptedWithoutSpecMovesToTearingDown: no spec can be
// found (the path is not visible and nothing is cached), so a teardown
// would be a partial honour of a destructive operation. The entry moves to
// tearing-down with the note naming the fix — never dropped blind, never
// left to the ageing timer, which drops without tearing down.
func TestRecoverInterruptedWithoutSpecMovesToTearingDown(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := restartHarness(t, root, nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fleetEntry(t, h1, &store.Entry{
		App: "no-spec-app", Slug: "wt-1", Slot: 1,
		Owner: "4242", OwnerKind: api.KindHost,
		Path: filepath.Join("/container", "worktrees", "wt-1"), PathVisible: false,
		State: store.StateReserving, Resources: map[string]spec.Resolved{},
		CreatedAt: now, LastSeen: now,
	})

	h2 := restartHarness(t, root, nil)
	n, rerr := h2.H.RecoverInterrupted()
	if rerr != nil {
		t.Fatalf("RecoverInterrupted: %v", rerr)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1", n)
	}
	reg, rerr := h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].State != store.StateTearingDown {
		t.Fatalf("registry after recovery = %+v, want the entry tearing-down", reg.Entries)
	}
	if !strings.Contains(reg.Entries[0].TeardownNote, "no spec") {
		t.Errorf("the note must name the missing spec: %s", reg.Entries[0].TeardownNote)
	}
	// The slot stays held: no other entry may take it.
	if e := registrySlotEntry(reg, "no-spec-app", 1); e == nil {
		t.Error("slot 1 must stay held")
	}
}

// TestRecoverInterruptedLeavesActiveAndTearingDownAlone: the pass touches
// reserving entries only — active and tearing-down entries are resting
// states that survive a restart by design.
func TestRecoverInterruptedLeavesActiveAndTearingDownAlone(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := restartHarness(t, root, nil)
	now := time.Now().UTC().Format(time.RFC3339Nano)
	mk := func(slug, state string) *store.Entry {
		e := baseFleetEntry(tempRoot(t), true)
		e.Slug = slug
		e.State = state
		e.CreatedAt, e.LastSeen = now, now
		return e
	}
	fleetEntry(t, h1, mk("wt-active", store.StateActive))
	fleetEntry(t, h1, mk("wt-tearing", store.StateTearingDown))
	fleetEntry(t, h1, mk("wt-reserving", store.StateReserving))

	h2 := restartHarness(t, root, nil)
	n, rerr := h2.H.RecoverInterrupted()
	if rerr != nil {
		t.Fatalf("RecoverInterrupted: %v", rerr)
	}
	if n != 1 {
		t.Fatalf("recovered = %d, want 1 (the reserving entry only)", n)
	}
	reg, rerr := h2.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	states := map[string]string{}
	for _, e := range reg.Entries {
		states[e.Slug] = e.State
	}
	if states["wt-active"] != store.StateActive || states["wt-tearing"] != store.StateTearingDown {
		t.Errorf("the pass touched resting states: %v", states)
	}
	// The reserving entry stops being reserving (the fixture has no spec
	// anywhere, so it moves to tearing-down with the note; the outcome that
	// matters here is that the pass resolved it and touched nothing else).
	if states["wt-reserving"] == store.StateReserving {
		t.Errorf("the reserving entry must be resolved: %v", states)
	}
}

// TestReleaseRefusesTearingDownEntry is the release rail on its own: the
// rollback verb drops reserving entries and refuses tearing-down ones,
// because releasing would orphan what survived (B2.3).
func TestReleaseRefusesTearingDownEntry(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	now := time.Now().UTC().Format(time.RFC3339Nano)
	e := baseFleetEntry(tempRoot(t), true)
	e.State = store.StateTearingDown
	e.TeardownNote = "teardown left a container behind"
	e.CreatedAt, e.LastSeen = now, now
	fleetEntry(t, h, e)

	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello refused: %+v", err)
	}
	resp := h.Request(context.Background(), sess, verbRelease, &api.EntryRef{App: e.App, Slug: e.Slug})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("release of a tearing-down entry = %+v, want a refusal", resp.Error)
	}
	reg, rerr := h.Store.ReadRegistry()
	if rerr != nil {
		t.Fatalf("reading the registry: %v", rerr)
	}
	if len(reg.Entries) != 1 {
		t.Errorf("the refused release must leave the entry: %+v", reg.Entries)
	}
}
