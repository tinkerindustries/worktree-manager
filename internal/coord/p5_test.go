package coord

// p5_test.go exercises the phase-5 coordinator surface: the materialise
// verb (init's step 3), the rm verb (reap + teardown + drop) and the
// reaper's rails. Each test is named for the exit criterion or rail it
// proves; the live docker half lives in the acceptance-tagged files.

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
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
func allocMaterialise(t *testing.T, reg driver.Registry) (*Harness, *Session, *spec.Spec, protocol.AllocateResult) {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
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

	resp := h.Request(ctx, sess, verbMaterialise, &protocol.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres protocol.MaterialiseResult
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

	resp := h.Request(ctx, sess, verbMaterialise, &protocol.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres protocol.MaterialiseResult
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
	resp = h.Request(ctx, sess, verbRelease, &protocol.EntryRef{App: res.App, Slug: res.Slug})
	if resp.Error != nil {
		t.Fatalf("release refused: %+v", resp.Error)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
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

	resp := h.Request(ctx, sess, verbMaterialise, &protocol.MaterialiseArgs{
		App: res.App, Slug: res.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("materialise refused: %+v", resp.Error)
	}
	var mres protocol.MaterialiseResult
	decodeResult(t, resp, &mres)
	if mres.State != store.StateTearingDown {
		t.Fatalf("state = %q, want tearing-down (the rollback left resources behind)", mres.State)
	}
	if mres.RollbackErr == "" {
		t.Error("rollback_err must name the failed teardown")
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
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
	other, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("second hello refused: %+v", reply.Error)
	}
	resp := h.Request(context.Background(), other, verbMaterialise, &protocol.MaterialiseArgs{
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
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := materialiseSpec(t)
	// No band is needed: the spec has no port resources.

	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "wt-1", Path: "/tmp/wt/wt-1", SlotHint: 3,
	})
	if resp.Error != nil {
		t.Fatalf("rebuild allocation refused: %+v", resp.Error)
	}
	var res protocol.AllocateResult
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
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := materialiseSpec(t)
	if _, perr := allocate(t, h, sess, sp, "wt-1"); perr != nil {
		t.Fatalf("first allocation refused: %+v", perr)
	}
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
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
	sess, _ := h.Connect(protocol.KindHost, "")
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
	if second.Path != "/tmp/wt/wt-1" {
		t.Errorf("Path = %q, want the entry's recorded path", second.Path)
	}
	if second.Slot != first.Slot {
		t.Errorf("slot = %d, want the authoritative %d", second.Slot, first.Slot)
	}
	if len(second.Shared) != 1 || second.Shared[0].Name == "" || second.Shared[0].Impact == "" {
		t.Errorf("shared = %+v, want the resolved name-and-impact row", second.Shared)
	}
}

// --- the reaper ----------------------------------------------------------

// reapPortsEntry writes a registry entry whose port resources are exactly
// the given values, and returns it.
func reapPortsEntry(t *testing.T, h *Harness, ports ...int) *store.Entry {
	t.Helper()
	now := time.Now().UTC().Format(time.RFC3339Nano)
	resources := map[string]spec.Resolved{}
	for i, p := range ports {
		resources[fmt.Sprintf("api-%d", i)] = spec.Resolved{Type: "port", Value: p}
	}
	e := &store.Entry{
		App: "compose-app", Slug: "wt-1", Slot: 1,
		Owner: "4242", OwnerKind: protocol.KindHost,
		Path: "/tmp/wt/wt-1", State: store.StateActive,
		Resources: resources, CreatedAt: now, LastSeen: now,
	}
	if err := h.Store.WriteRegistry(store.RegistryFile{Entries: []store.Entry{*e}}); err != nil {
		t.Fatalf("writing the registry: %v", err)
	}
	return e
}

// listenProc starts a process that listens on 127.0.0.1:port and returns
// its pid and the command /proc reports for it.
func listenProc(t *testing.T, port int) (pid int, comm string) {
	t.Helper()
	script := fmt.Sprintf(`python3 -c "import socket,time
s=socket.socket(); s.setsockopt(socket.SOL_SOCKET, socket.SO_REUSEADDR, 1)
s.bind(('127.0.0.1', %d)); s.listen(1); time.sleep(300)"`, port)
	cmd := exec.Command("sh", "-c", script)
	if err := cmd.Start(); err != nil {
		t.Fatalf("starting the listener: %v", err)
	}
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	t.Cleanup(func() {
		exec.Command("kill", "-9", strconv.Itoa(cmd.Process.Pid)).Run()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
		}
	})
	// Wait until the socket is listening.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		conn, err := net.DialTimeout("tcp", fmt.Sprintf("127.0.0.1:%d", port), 200*time.Millisecond)
		if err == nil {
			conn.Close()
			break
		}
		time.Sleep(100 * time.Millisecond)
	}
	// The command name comes from the same discovery the reaper uses, rather
	// than from a second implementation. Reading /proc/<pid>/comm directly
	// works only on Linux, and the reaper's own allowlist is matched against
	// whatever platform.Listeners reports — so asking it is both portable and
	// the thing actually under test.
	holders, err := platform.Listeners([]int{port})
	if err != nil {
		t.Fatalf("discovering the listener on port %d: %v", port, err)
	}
	for _, h := range holders {
		if h.PID == cmd.Process.Pid {
			return h.PID, h.Command
		}
	}
	// The listener may be a child that replaced the shell, or the shell may
	// have forked; either way the discovery's pid is the one the reaper acts
	// on, so take it.
	if len(holders) == 1 {
		return holders[0].PID, holders[0].Command
	}
	t.Fatalf("discovery found %d holders of port %d, want the listener started here: %+v", len(holders), port, holders)
	return 0, ""
}

// reapHarness is the harness with the container-detection seam disabled,
// so the reap logic itself is exercised in whatever namespace the test
// runs in.
func reapHarness(t *testing.T) *Harness {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InContainer = func() bool { return false }
	return h
}

// TestCoordReapSignalsOnlyNamedBinaries: a holder whose command is one of
// the spec's binaries is signalled (TERM, then KILL after the wait); a
// holder that merely grabbed the port is reported and never signalled —
// it is probably the developer's own instance (B7.1).
func TestCoordReapSignalsOnlyNamedBinaries(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	old := reapGraceWait
	reapGraceWait = 100 * time.Millisecond
	defer func() { reapGraceWait = old }()

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) == 0 {
		t.Fatalf("no signal recorded for the named binary %q: %+v", comm, report)
	}
	if len(report.Holders) != 0 {
		t.Errorf("holders = %+v, want none (the only holder is a named binary)", report.Holders)
	}
	// The process must actually be gone.
	deadline := time.Now().Add(5 * time.Second)
	for alivePid(pid) && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if alivePid(pid) {
		t.Errorf("the named binary (pid %d) survived the reap", pid)
	}
}

// alivePid is the test-side liveness check.
func alivePid(pid int) bool {
	return exec.Command("kill", "-0", strconv.Itoa(pid)).Run() == nil
}

// TestCoordReapNeverSignalsANonSpecHolder: a process that merely grabbed
// the port is reported and never signalled, even with a reaper installed.
func TestCoordReapNeverSignalsANonSpecHolder(t *testing.T) {
	port := freeCoordPort(t)
	pid, _ := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{"some-other-binary"} }

	report := h.H.reap(e, sp, false, false)
	if !report.Available {
		t.Fatalf("reap unavailable: %s", report.Note)
	}
	if len(report.Signalled) != 0 {
		t.Fatalf("signalled %+v; a non-spec holder is never signalled", report.Signalled)
	}
	if len(report.Holders) != 1 {
		t.Fatalf("holders = %+v, want the one reported holder", report.Holders)
	}
	if !alivePid(pid) {
		t.Error("the reported holder was killed; only the spec's binaries may be signalled")
	}
}

// TestCoordReapNeverTouchesReservedPorts: a holder on a port in the spec's
// reserved block (or the ledger's host-global reservations) is never
// signalled, even when its command is a spec binary — the exclusion list
// is reused, never a second one (B8.3).
func TestCoordReapNeverTouchesReservedPorts(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	sp.Reserved.Ports = []int{port}
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, false, false)
	if len(report.SkippedPorts) != 1 || report.SkippedPorts[0] != port {
		t.Fatalf("skipped_ports = %v, want [%d]", report.SkippedPorts, port)
	}
	if len(report.Signalled) != 0 {
		t.Fatalf("signalled %+v; a reserved port's holder is never touched", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("the reserved port's holder was killed")
	}

	// The ledger's host-global reservations are the same rail.
	port2 := freeCoordPort(t)
	pid2, comm2 := listenProc(t, port2)
	e2 := reapPortsEntry(t, h, port2)
	if err := h.Store.WriteBands(store.BandsFile{Reservations: []store.Reservation{
		{Ports: []int{port2}, Note: "the co-resident production stack"},
	}}); err != nil {
		t.Fatalf("writing the ledger: %v", err)
	}
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm2} }
	report2 := h.H.reap(e2, sp, false, false)
	if len(report2.Signalled) != 0 || !alivePid(pid2) {
		t.Errorf("host-reserved port holder signalled or killed: %+v", report2.Signalled)
	}
}

// TestCoordReapInContainerReportsUnavailable: a coordinator inside a
// container reports the reap as unavailable with the remedy named — never
// as a successful reap of zero processes (04-lifecycle.md §6).
func TestCoordReapInContainerReportsUnavailable(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InContainer = func() bool { return true }
	e := reapPortsEntry(t, h, freeCoordPort(t))
	sp := materialiseSpec(t)
	report := h.H.reap(e, sp, false, false)
	if report.Available {
		t.Error("Available = true inside a container, want false")
	}
	if !strings.Contains(report.Note, "container") || !strings.Contains(report.Note, "host") {
		t.Errorf("the note must name the cause and the remedy: %q", report.Note)
	}
}

// TestCoordReapDryRunListsWithoutSignalling: --dry-run lists what would be
// signalled and touches nothing (B7.1).
func TestCoordReapDryRunListsWithoutSignalling(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, false, true)
	if len(report.Signalled) != 1 || report.Signalled[0].Signal != "would-signal" {
		t.Fatalf("signalled = %+v, want one would-signal", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("the dry-run reap killed the process")
	}
}

// TestCoordReapKeepProcessesOptsOut: --keep-processes opts the reaper out
// entirely, stating the opt-out.
func TestCoordReapKeepProcessesOptsOut(t *testing.T) {
	port := freeCoordPort(t)
	pid, comm := listenProc(t, port)
	h := reapHarness(t)
	e := reapPortsEntry(t, h, port)
	sp := materialiseSpec(t)
	h.H.ReapBinaries = func(*spec.Spec) []string { return []string{comm} }

	report := h.H.reap(e, sp, true, false)
	if !report.KeptProcesses {
		t.Error("KeptProcesses = false, want true")
	}
	if len(report.Signalled) != 0 {
		t.Errorf("signalled %+v under --keep-processes", report.Signalled)
	}
	if !alivePid(pid) {
		t.Error("--keep-processes did not keep the process")
	}
}

// --- the rm verb ---------------------------------------------------------

// TestRmVerbEntryNotFoundIsDataOutcome: rm of a slug with no entry is not
// an error — the client's no-entry paths decide (04-lifecycle.md §7.3).
func TestRmVerbEntryNotFoundIsDataOutcome(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := materialiseSpec(t)
	resp := h.Request(context.Background(), sess, verbRm, &protocol.RmArgs{
		App: sp.App, Slug: "never-allocated", Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm of a missing entry = %+v, want a data outcome", resp.Error)
	}
	var res protocol.RmResult
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
	resp := h.Request(context.Background(), sess, verbRm, &protocol.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp, DryRun: true,
	})
	if resp.Error != nil {
		t.Fatalf("rm --dry-run refused: %+v", resp.Error)
	}
	var res protocol.RmResult
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
	resp := h.Request(context.Background(), sess, verbRm, &protocol.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp,
	})
	if resp.Error != nil {
		t.Fatalf("rm refused: %+v", resp.Error)
	}
	var res protocol.RmResult
	decodeResult(t, resp, &res)
	if !res.EntryFound || !res.Removed {
		t.Fatalf("result = %+v, want entry found and removed", res)
	}
	if len(stub.tornDown) != 1 {
		t.Errorf("teardowns = %v, want the entry's namespace torn down", stub.tornDown)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
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
	other, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("second hello refused: %+v", reply.Error)
	}
	resp := h.Request(context.Background(), other, verbRm, &protocol.RmArgs{
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
	resp := h.Request(context.Background(), sess, verbRm, &protocol.RmArgs{
		App: ref.App, Slug: ref.Slug, Spec: *sp,
	})
	if resp.Error == nil || resp.Error.Code != 1 {
		t.Fatalf("rm with survivors = %+v, want exit 1", resp.Error)
	}
	if !strings.Contains(resp.Error.Msg, "survived") {
		t.Errorf("the error must name what survived: %q", resp.Error.Msg)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil || e.State != store.StateTearingDown {
		t.Errorf("entry = %+v, want tearing-down with the slot held", e)
	}
}

// decodeResult decodes a response's result into v.
func decodeResult(t *testing.T, resp *protocol.Response, v any) {
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
