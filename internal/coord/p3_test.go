package coord

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// testSpec builds a valid minimal spec: one stride port resource "api" off
// the band, an emit block (validation requires a descriptor declaration),
// and the given slot ceiling and reserved ports.
func testSpec(t *testing.T, app string, slotMax int, reserved ...int) *spec.Spec {
	t.Helper()
	max := slotMax
	s := &spec.Spec{
		Version: 1,
		App:     app,
		Slots:   spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	s.Reserved.Ports = reserved
	if err := spec.Validate(s); err != nil {
		t.Fatalf("test spec does not validate: %v", err)
	}
	return s
}

// registerBand is the harness-side convenience for the explicit
// registration every allocation depends on: `wt bands reserve` for one
// stride base.
func registerBand(t *testing.T, h *Harness, sess *Session, s *spec.Spec, base int) {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
		Spec: *s, Bases: map[string]int{"api": base},
	})
	if resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}
}

// allocate runs one allocate request and returns the decoded result.
func allocate(t *testing.T, h *Harness, sess *Session, s *spec.Spec, slug string) (protocol.AllocateResult, *protocol.Error) {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *s, Slug: slug, Path: filepath.Join("/tmp/wt", slug),
	})
	if resp.Error != nil {
		return protocol.AllocateResult{}, resp.Error
	}
	var res protocol.AllocateResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding allocate result: %v\n%s", err, resp.Result)
	}
	return res, nil
}

// TestConcurrentAllocationsGetDistinctSlots is exit criterion 1: two
// concurrent allocations against one app get different slots, tested
// in-process. Both goroutines race through the handler at once; the
// handler's mutex serialises the registry's load-modify-save, so the two
// can never take the same slot — that serialisation is the whole
// concurrency design (no lock file, no generation counter).
func TestConcurrentAllocationsGetDistinctSlots(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	s1, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	s2, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, s1, sp, 4200)

	ctx := context.Background()
	start := make(chan struct{})
	slots := make([]int, 2)
	errs := make([]*protocol.Error, 2)
	var wg sync.WaitGroup
	for i, sess := range []*Session{s1, s2} {
		wg.Add(1)
		go func(i int, sess *Session) {
			defer wg.Done()
			<-start
			resp := h.Request(ctx, sess, verbAllocate, &protocol.AllocateArgs{
				Spec: *sp, Slug: fmt.Sprintf("wt-%d", i), Path: fmt.Sprintf("/tmp/wt/wt-%d", i),
			})
			if resp.Error != nil {
				errs[i] = resp.Error
				return
			}
			var res protocol.AllocateResult
			if err := json.Unmarshal(resp.Result, &res); err != nil {
				errs[i] = &protocol.Error{Code: 1, Msg: fmt.Sprintf("decoding: %v", err), Remedy: "test defect"}
				return
			}
			slots[i] = res.Slot
		}(i, sess)
	}
	close(start)
	wg.Wait()

	for i, e := range errs {
		if e != nil {
			t.Fatalf("concurrent allocation %d failed: %+v", i, e)
		}
	}
	if slots[0] == 0 || slots[1] == 0 {
		t.Errorf("a slot of 0 was allocated; slot 0 is the primary checkout and is never allocated: %v", slots)
	}
	if slots[0] == slots[1] {
		t.Errorf("two concurrent allocations took the same slot %d", slots[0])
	}
	// The two slots are exactly 1 and 2 — lowest free, first come first
	// served — whichever goroutine happened to serialise first.
	if slots[0] != 1 && slots[0] != 2 || slots[1] != 1 && slots[1] != 2 {
		t.Errorf("slots = %v, want 1 and 2 (lowest free)", slots)
	}
	// Both entries are committed in the registry.
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 2 {
		t.Errorf("registry holds %d entries, want 2", len(reg.Entries))
	}
	for _, e := range reg.Entries {
		if e.State != store.StateReserving {
			t.Errorf("entry %s state = %q, want reserving", e.Slug, e.State)
		}
	}
}

// TestForeignEntryMutationRefused is exit criterion 2: a mutating call
// against an entry owned by another client is refused, naming the owner and
// its last-seen time, with exit code 3.
func TestForeignEntryMutationRefused(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	owner, _ := h.Connect(protocol.KindHost, "") // uid 4242
	other, _ := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, owner, sp, 4200)

	if _, perr := allocate(t, h, owner, sp, "alpha"); perr != nil {
		t.Fatalf("owner allocation refused: %+v", perr)
	}

	// The owner's last-seen is measured in clients.json by the coordinator.
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	var ownerLastSeen string
	for _, c := range clients.Clients {
		if c.Identity == "4242" {
			ownerLastSeen = c.LastSeen
		}
	}
	if ownerLastSeen == "" {
		t.Fatal("the owner was never recorded in clients.json")
	}

	// activate: the mutating call the foreign client attempts.
	resp := h.Request(context.Background(), other, verbActivate, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error == nil {
		t.Fatal("a foreign activate succeeded, want a refusal")
	}
	if resp.Error.Code != 3 {
		t.Errorf("refusal code = %d, want 3 (refused by a safety check)", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Msg, "4242") || !strings.Contains(resp.Error.Msg, "host client") {
		t.Errorf("refusal does not name the owner: %s", resp.Error.Msg)
	}
	if !strings.Contains(resp.Error.Msg, ownerLastSeen) {
		t.Errorf("refusal does not name the owner's last-seen %q: %s", ownerLastSeen, resp.Error.Msg)
	}
	if resp.Error.Remedy == "" {
		t.Error("refusal has no remedy")
	}

	// release is refused the same way.
	resp = h.Request(context.Background(), other, verbRelease, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error == nil || resp.Error.Code != 3 {
		t.Fatalf("foreign release = %+v, want a refusal", resp.Error)
	}

	// The entry survives both refusals, still in reserving, still owned.
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].State != store.StateReserving || reg.Entries[0].Owner != "4242" {
		t.Errorf("registry after refusals = %+v", reg.Entries)
	}

	// The owner can still mutate it.
	resp = h.Request(context.Background(), owner, verbActivate, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error != nil {
		t.Fatalf("owner activate refused: %+v", resp.Error)
	}
}

// TestAllocationRequiresRegisteredBand is exit criterion 3: allocation for
// an app with no registered band refuses and names the registration
// command.
func TestAllocationRequiresRegisteredBand(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)

	_, perr := allocate(t, h, sess, sp, "alpha")
	if perr == nil {
		t.Fatal("allocation without a registered band succeeded, want a refusal")
	}
	if perr.Code != 3 {
		t.Errorf("refusal code = %d, want 3", perr.Code)
	}
	if !strings.Contains(perr.Msg, "compose-app") {
		t.Errorf("refusal does not name the app: %s", perr.Msg)
	}
	if !strings.Contains(perr.Remedy, "wt bands reserve") {
		t.Errorf("remedy does not name the registration command: %s", perr.Remedy)
	}
	// Nothing was written.
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("refused allocation wrote %d entries", len(reg.Entries))
	}
}

// TestSlotExhaustionNamesRangeCleanupAndForeignCount is exit criterion 4:
// the slot ceiling message names the range, the cleanup command, and how
// many of the occupied slots the caller cannot free — otherwise the remedy
// it names would appear to do nothing.
func TestSlotExhaustionNamesRangeCleanupAndForeignCount(t *testing.T) {
	t.Run("mixed ownership", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		alice, _ := h.Connect(protocol.KindHost, "") // uid 4242
		bob, _ := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 2)
		registerBand(t, h, alice, sp, 4200)

		if res, perr := allocate(t, h, alice, sp, "alpha"); perr != nil || res.Slot != 1 {
			t.Fatalf("first allocation = %+v / %+v, want slot 1", res, perr)
		}
		if res, perr := allocate(t, h, bob, sp, "beta"); perr != nil || res.Slot != 2 {
			t.Fatalf("second allocation = %+v / %+v, want slot 2", res, perr)
		}
		_, perr := allocate(t, h, alice, sp, "gamma")
		if perr == nil {
			t.Fatal("allocation past the ceiling succeeded, want a refusal")
		}
		if perr.Code != 3 {
			t.Errorf("refusal code = %d, want 3", perr.Code)
		}
		// The range.
		if !strings.Contains(perr.Msg, "1..2") {
			t.Errorf("message does not name the range: %s", perr.Msg)
		}
		// How many occupied slots the caller cannot free: slot 2 belongs to
		// bob, so 1.
		if !strings.Contains(perr.Msg, "1 of them are owned by other clients") {
			t.Errorf("message does not state the foreign count: %s", perr.Msg)
		}
		// The cleanup command.
		if !strings.Contains(perr.Remedy, "wt cleanup") {
			t.Errorf("remedy does not name the cleanup command: %s", perr.Remedy)
		}
	})

	t.Run("all slots are the caller's own", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 1)
		registerBand(t, h, sess, sp, 4200)
		if res, perr := allocate(t, h, sess, sp, "alpha"); perr != nil || res.Slot != 1 {
			t.Fatalf("allocation = %+v / %+v", res, perr)
		}
		_, perr := allocate(t, h, sess, sp, "beta")
		if perr == nil {
			t.Fatal("allocation past the ceiling succeeded, want a refusal")
		}
		if !strings.Contains(perr.Msg, "0 of them are owned by other clients") {
			t.Errorf("message does not state the foreign count is 0: %s", perr.Msg)
		}
		if !strings.Contains(perr.Remedy, "wt cleanup") {
			t.Errorf("remedy does not name the cleanup command: %s", perr.Remedy)
		}
	})

	t.Run("every slot excluded leaves nothing to clean up", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		// The spec reserves the ports both slots would derive (4201, 4202):
		// exhaustion here is an exclusion problem, not an occupancy one, and
		// the message must not promise that cleanup would help.
		sp := testSpec(t, "compose-app", 2, 4201, 4202)
		registerBand(t, h, sess, sp, 4200)
		_, perr := allocate(t, h, sess, sp, "alpha")
		if perr == nil {
			t.Fatal("allocation over a fully excluded range succeeded, want a refusal")
		}
		if !strings.Contains(perr.Msg, "1..2") {
			t.Errorf("message does not name the range: %s", perr.Msg)
		}
		if !strings.Contains(perr.Msg, "reserved block") {
			t.Errorf("message does not name the exclusions: %s", perr.Msg)
		}
		if !strings.Contains(perr.Remedy, "bands reserve --host") {
			t.Errorf("remedy does not name the reservation command: %s", perr.Remedy)
		}
	})
}

// TestReservedPortsNeverAllocated is exit criterion 5: a port in the
// ledger's reservations is never allocated, and neither is one in the
// spec's reserved block. The allocator skips the slot whose derived port
// falls in either exclusion set and takes the next free one.
func TestReservedPortsNeverAllocated(t *testing.T) {
	t.Run("spec reserved block", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		// Slot 1 would derive 4201; the spec's committed defaults reserve it.
		sp := testSpec(t, "compose-app", 8, 4201)
		registerBand(t, h, sess, sp, 4200)
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil {
			t.Fatalf("allocation refused: %+v", perr)
		}
		if res.Slot != 2 {
			t.Errorf("slot = %d, want 2 (slot 1's port 4201 is in the spec's reserved block)", res.Slot)
		}
		if res.Resources["api"].Value != float64(4202) {
			t.Errorf("api = %v, want 4202", res.Resources["api"].Value)
		}
	})

	t.Run("ledger host reservation", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 8)
		registerBand(t, h, sess, sp, 4200)
		// The host-global reservation covers the ports slots 1 and 2 would
		// derive (4201, 4202); the note names what holds the range.
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Host: true, Ports: []int{4202, 4201}, Note: "compose-app production stack",
		})
		if resp.Error != nil {
			t.Fatalf("host reservation refused: %+v", resp.Error)
		}
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil {
			t.Fatalf("allocation refused: %+v", perr)
		}
		if res.Slot != 3 {
			t.Errorf("slot = %d, want 3 (slots 1 and 2 derive reserved ports 4201/4202)", res.Slot)
		}
		// The reserved ports never appear in the allocation.
		if v, _ := res.Resources["api"].Value.(float64); v == 4201 || v == 4202 {
			t.Errorf("allocated the reserved port %v", v)
		}
	})

	t.Run("both exclusion sources together", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		// Spec reserves 4201 (slot 1); the ledger reserves 4202 (slot 2).
		sp := testSpec(t, "compose-app", 8, 4201)
		registerBand(t, h, sess, sp, 4200)
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Host: true, Ports: []int{4202}, Note: "the machine's second stack",
		})
		if resp.Error != nil {
			t.Fatalf("host reservation refused: %+v", resp.Error)
		}
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil {
			t.Fatalf("allocation refused: %+v", perr)
		}
		if res.Slot != 3 {
			t.Errorf("slot = %d, want 3 (slot 1 spec-reserved, slot 2 ledger-reserved)", res.Slot)
		}
	})
}

// TestNewerRegistryListsButDoesNotWrite is exit criterion 6: a registry
// written by a newer schema version lists but does not write.
func TestNewerRegistryListsButDoesNotWrite(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	newer := `{"schema_version":2,"entries":[{"app":"future-app","slug":"x","slot":1,"owner":"1","owner_kind":"host","state":"active","created_at":"2026-01-01T00:00:00Z","last_seen":"2026-01-01T00:00:00Z"}]}`
	if err := h.Store.WriteFile(store.RegistryFileName, []byte(newer)); err != nil {
		t.Fatal(err)
	}

	// Lists: the lenient read decodes the future entry.
	f, err := h.Store.ReadRegistryList()
	if err != nil {
		t.Fatalf("ReadRegistryList: %v", err)
	}
	if f.SchemaVersion != 2 || len(f.Entries) != 1 || f.Entries[0].App != "future-app" {
		t.Errorf("listed registry = %+v, want the future entry at schema 2", f)
	}

	// Does not write: a mutating call refuses, naming the upgrade.
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "alpha", Path: "/tmp/wt/alpha",
	})
	if resp.Error == nil {
		t.Fatal("allocation against a newer registry succeeded, want a refusal")
	}
	if resp.Error.Code != 3 {
		t.Errorf("refusal code = %d, want 3", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Msg, "upgrade wtd") {
		t.Errorf("refusal does not name the upgrade: %s", resp.Error.Msg)
	}

	// And the file on disk is untouched.
	raw, err := os.ReadFile(filepath.Join(h.Store.Root(), store.RegistryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != newer {
		t.Error("the newer registry was written back; it must list but never write")
	}
}

// TestAllocateIsIdempotentForExistingSlug pins ARCHITECTURE.md §8.6 rule 5:
// an existing entry's slot is authoritative and is never reassigned under a
// running worktree — re-running init reconciles and rebuilds, it never
// reallocates.
func TestAllocateIsIdempotentForExistingSlug(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	first, perr := allocate(t, h, sess, sp, "alpha")
	if perr != nil {
		t.Fatalf("first allocation refused: %+v", perr)
	}
	second, perr := allocate(t, h, sess, sp, "alpha")
	if perr != nil {
		t.Fatalf("re-allocation refused: %+v", perr)
	}
	if second.Slot != first.Slot {
		t.Errorf("re-allocation moved the slot from %d to %d; an entry's slot is authoritative", first.Slot, second.Slot)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 {
		t.Errorf("re-allocation wrote %d entries, want 1", len(reg.Entries))
	}

	// A second client re-running init on the same worktree is refused: it
	// would mutate an entry it does not own.
	other, _ := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	_, perr = allocate(t, h, other, sp, "alpha")
	if perr == nil || perr.Code != 3 {
		t.Fatalf("foreign re-allocation = %+v, want a refusal", perr)
	}
	if !strings.Contains(perr.Msg, "4242") {
		t.Errorf("refusal does not name the owner: %s", perr.Msg)
	}
}

// TestReservingEntryAgedOutOnTimer: the coordinator ages a reserving entry
// out on its own timer — a slot claimed and never materialised (a client
// that died mid-sequence) is released, while fresh reserving entries and
// active entries of any age survive. The server's sweeper calls
// AgeReserving on an interval; here the timer's work is driven directly.
func TestReservingEntryAgedOutOnTimer(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	allocate(t, h, sess, sp, "stale")
	allocate(t, h, sess, sp, "fresh")
	activateResp := h.Request(context.Background(), sess, verbActivate, &protocol.EntryRef{App: "compose-app", Slug: "fresh"})
	if activateResp.Error != nil {
		t.Fatalf("activate refused: %+v", activateResp.Error)
	}

	now := time.Now().UTC()
	// An old reserving entry is aged out; a fresh one is not; an old active
	// entry is never aged out.
	if n, err := h.H.AgeReserving(now.Add(11*time.Minute), ReservingTimeout); err != nil || n != 1 {
		t.Fatalf("AgeReserving = %d, %v; want 1 aged out", n, err)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 || reg.Entries[0].Slug != "fresh" || reg.Entries[0].State != store.StateActive {
		t.Errorf("registry after ageing = %+v, want only the active fresh entry", reg.Entries)
	}

	// The aged-out slot is allocatable again.
	res, perr := allocate(t, h, sess, sp, "reborn")
	if perr != nil {
		t.Fatalf("allocation after ageing refused: %+v", perr)
	}
	if res.Slot != 1 {
		t.Errorf("reborn slot = %d, want 1 (the aged-out slot)", res.Slot)
	}
}

// TestAllocatorProbeSeam: the phase-4 seam. The probe is a field on the
// handler; this phase's default reports everything free, and a slot whose
// resources the probe reports held is skipped — while an unavailable probe
// does not block allocation (plan.md §3). Phase 4's port driver installs
// the real probe here.
func TestAllocatorProbeSeam(t *testing.T) {
	t.Run("held skips the slot", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 8)
		registerBand(t, h, sess, sp, 4200)
		h.H.Probe = func(s *spec.Spec, slot int, resources map[string]spec.Resolved) ProbeResult {
			if slot == 1 {
				return ProbeHeld
			}
			return ProbeFree
		}
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil {
			t.Fatalf("allocation refused: %+v", perr)
		}
		if res.Slot != 2 {
			t.Errorf("slot = %d, want 2 (the probe held slot 1)", res.Slot)
		}
	})

	t.Run("unavailable does not block allocation", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		sess, _ := h.Connect(protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 8)
		registerBand(t, h, sess, sp, 4200)
		h.H.Probe = func(s *spec.Spec, slot int, resources map[string]spec.Resolved) ProbeResult {
			return ProbeUnavailable
		}
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil {
			t.Fatalf("allocation refused despite an unavailable probe: %+v", perr)
		}
		if res.Slot != 1 {
			t.Errorf("slot = %d, want 1", res.Slot)
		}
		// Bounded coverage is stated: the output carries the note that the
		// registry was the only check performed (03-drivers.md §4.1).
		if res.ProbeNote == "" {
			t.Error("ProbeNote is empty; an unavailable probe must state that the registry was the only check")
		}
	})

	t.Run("the shipped probe is the no-probe probe", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		if h.H.Probe == nil {
			t.Fatal("the handler has no probe at all; the no-probe case must be explicit")
		}
		sess, _ := h.Connect(protocol.KindHost, "")
		sp := testSpec(t, "compose-app", 8)
		registerBand(t, h, sess, sp, 4200)
		res, perr := allocate(t, h, sess, sp, "alpha")
		if perr != nil || res.Slot != 1 {
			t.Fatalf("default probe allocation = slot %d / %+v, want slot 1", res.Slot, perr)
		}
	})
}

// TestPathVisibleIsTheCoordinatorStat: path_visible records whether the
// coordinator can stat the path the creating client saw — a path that
// exists only inside a container is recorded, never checked, and never
// counted as stale.
func TestPathVisibleIsTheCoordinatorStat(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	real := tempRoot(t)
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "visible", Path: real,
	})
	if resp.Error != nil {
		t.Fatalf("allocation refused: %+v", resp.Error)
	}
	var res protocol.AllocateResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if !res.PathVisible {
		t.Errorf("path_visible = false for a path the coordinator can stat (%s)", real)
	}

	resp = h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "container-only", Path: filepath.Join(real, "inside-a-container"),
	})
	if resp.Error != nil {
		t.Fatalf("allocation refused: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.PathVisible {
		t.Error("path_visible = true for a path the coordinator cannot stat")
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Entries[1]
	if e.PathVisible || e.Path != filepath.Join(real, "inside-a-container") {
		t.Errorf("container-only entry = %+v; the path is recorded but never checked", e)
	}
}

// TestEntryLifecycle: reserving → active → released, with the states
// recorded in the registry, and the owner's last-seen moving on mutation.
func TestEntryLifecycle(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	res, perr := allocate(t, h, sess, sp, "alpha")
	if perr != nil || res.State != store.StateReserving {
		t.Fatalf("allocation = %+v / %+v, want state reserving", res, perr)
	}
	reg, _ := h.Store.ReadRegistry()
	if reg.Entries[0].State != store.StateReserving {
		t.Errorf("state = %q, want reserving", reg.Entries[0].State)
	}

	resp := h.Request(context.Background(), sess, verbActivate, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error != nil {
		t.Fatalf("activate refused: %+v", resp.Error)
	}
	var act protocol.ActivateResult
	if err := json.Unmarshal(resp.Result, &act); err != nil || act.State != store.StateActive {
		t.Errorf("activate result = %+v / %v, want state active", act, err)
	}
	reg, _ = h.Store.ReadRegistry()
	if reg.Entries[0].State != store.StateActive {
		t.Errorf("state = %q, want active", reg.Entries[0].State)
	}
	created, _ := time.Parse(time.RFC3339Nano, reg.Entries[0].CreatedAt)
	seen, _ := time.Parse(time.RFC3339Nano, reg.Entries[0].LastSeen)
	if !seen.After(created) {
		t.Errorf("entry last_seen %v did not advance past created_at %v", seen, created)
	}

	resp = h.Request(context.Background(), sess, verbRelease, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error != nil {
		t.Fatalf("release refused: %+v", resp.Error)
	}
	reg, _ = h.Store.ReadRegistry()
	if len(reg.Entries) != 0 {
		t.Errorf("registry after release = %+v, want empty", reg.Entries)
	}

	// Activating a released entry names the missing entry.
	resp = h.Request(context.Background(), sess, verbActivate, &protocol.EntryRef{App: "compose-app", Slug: "alpha"})
	if resp.Error == nil || resp.Error.Code != 1 {
		t.Fatalf("activate after release = %+v, want a failure", resp.Error)
	}
}

// TestBandReserveAndList: registration is explicit and the ledger round
// trips; re-registration replaces in place; bands.list reports both halves
// of the ledger sorted.
func TestBandReserveAndList(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
		Host: true, Ports: []int{5320, 5319}, Note: "compose-app production stack",
	})
	if resp.Error != nil {
		t.Fatalf("host reservation refused: %+v", resp.Error)
	}
	var hostRes protocol.ReserveBandResult
	if err := json.Unmarshal(resp.Result, &hostRes); err != nil {
		t.Fatal(err)
	}
	if !hostRes.Host || hostRes.Note != "compose-app production stack" {
		t.Errorf("host reservation result = %+v", hostRes)
	}
	if len(hostRes.Ports) != 2 || hostRes.Ports[0] != 5319 || hostRes.Ports[1] != 5320 {
		t.Errorf("host reservation ports = %v, want sorted 5319,5320", hostRes.Ports)
	}

	// Re-registration replaces the app's band in place: one app, one band.
	registerBand(t, h, sess, sp, 4300)
	reg, err := h.Store.ReadBands()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Bands) != 1 || reg.Bands[0].Bases["api"] != 4300 {
		t.Errorf("bands after re-registration = %+v, want one band at 4300", reg.Bands)
	}

	// bands.list reports both halves.
	resp = h.Request(context.Background(), sess, verbBandsList, nil)
	if resp.Error != nil {
		t.Fatalf("bands.list refused: %+v", resp.Error)
	}
	var list protocol.BandsListResult
	if err := json.Unmarshal(resp.Result, &list); err != nil {
		t.Fatal(err)
	}
	if len(list.Bands) != 1 || list.Bands[0].App != "compose-app" || list.Bands[0].Bases["api"] != 4300 {
		t.Errorf("listed bands = %+v", list.Bands)
	}
	if len(list.Reservations) != 1 || list.Reservations[0].Note != "compose-app production stack" {
		t.Errorf("listed reservations = %+v", list.Reservations)
	}
}

// TestBandReserveValidations: the coordinator enforces the registration
// contract — a host reservation needs its note, bases must cover the spec's
// port resources exactly, and a band whose span leaves the port space is
// refused while the skill is choosing bases.
func TestBandReserveValidations(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)

	t.Run("host reservation requires a note", func(t *testing.T) {
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Host: true, Ports: []int{5319},
		})
		if resp.Error == nil || resp.Error.Code != 3 {
			t.Fatalf("unlabelled host reservation = %+v, want a refusal", resp.Error)
		}
		if !strings.Contains(resp.Error.Remedy, "--note") {
			t.Errorf("refusal does not name --note: %s", resp.Error.Remedy)
		}
		bands, _ := h.Store.ReadBands()
		if len(bands.Reservations) != 0 {
			t.Errorf("refused reservation was written: %+v", bands.Reservations)
		}
	})

	t.Run("base for a non-port resource is refused", func(t *testing.T) {
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Spec: *sp, Bases: map[string]int{"not-a-resource": 4200},
		})
		if resp.Error == nil || !strings.Contains(resp.Error.Msg, "not-a-resource") {
			t.Fatalf("base for a non-port resource = %+v, want a refusal naming it", resp.Error)
		}
	})

	t.Run("a port resource without a base is refused", func(t *testing.T) {
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Spec: *sp, Bases: map[string]int{},
		})
		if resp.Error == nil || !strings.Contains(resp.Error.Msg, `"api"`) {
			t.Fatalf("missing base = %+v, want a refusal naming api", resp.Error)
		}
	})

	t.Run("a band whose span leaves the port space is refused", func(t *testing.T) {
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Spec: *sp, Bases: map[string]int{"api": 65535},
		})
		// slots.max 8: the top slot derives 65535 + 7 = 65542.
		if resp.Error == nil || !strings.Contains(resp.Error.Msg, "65535") || !strings.Contains(resp.Error.Msg, "65542") {
			t.Fatalf("overflowing band = %+v, want a refusal naming the top port", resp.Error)
		}
	})

	t.Run("the span is the slot ceiling times ports per slot", func(t *testing.T) {
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Spec: *sp, Bases: map[string]int{"api": 4200},
		})
		if resp.Error != nil {
			t.Fatalf("registration refused: %+v", resp.Error)
		}
		var res protocol.ReserveBandResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatal(err)
		}
		if res.Spans["api"] != 8 {
			t.Errorf("span = %v, want 8 (slots.max 8 × 1 port per slot)", res.Spans)
		}
	})

	t.Run("group-form span multiplies by the group size", func(t *testing.T) {
		// A group of size 2: each slot consumes two ports of the base.
		form, size, offset := "group", 2, 0
		groupSpec := testSpec(t, "group-app", 4)
		groupSpec.Resources = []spec.Resource{
			{Type: "port", Name: "api", Form: &form, Size: &size, Offset: &offset},
		}
		if err := spec.Validate(groupSpec); err != nil {
			t.Fatalf("group spec does not validate: %v", err)
		}
		resp := h.Request(context.Background(), sess, verbBandsReserve, &protocol.ReserveBandArgs{
			Spec: *groupSpec, Bases: map[string]int{"api": 5000},
		})
		if resp.Error != nil {
			t.Fatalf("group registration refused: %+v", resp.Error)
		}
		var res protocol.ReserveBandResult
		if err := json.Unmarshal(resp.Result, &res); err != nil {
			t.Fatal(err)
		}
		if res.Spans["api"] != 8 {
			t.Errorf("group span = %v, want 8 (slots.max 4 × group size 2)", res.Spans)
		}
	})
}

// TestAllocationWithoutPortResourcesNeedsNoBand: an app with no port
// resources has no bases to register and allocates without consulting the
// ledger — there is nothing to collide with.
func TestAllocationWithoutPortResourcesNeedsNoBand(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	app := "vm-app"
	max := 4
	s := &spec.Spec{
		Version: 1,
		App:     app,
		Slots:   spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "machine", Name: "vm", Template: ptr("{app}-{slug}-{slot}")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("spec does not validate: %v", err)
	}
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *s, Slug: "alpha", Path: "/tmp/wt/alpha",
	})
	if resp.Error != nil {
		t.Fatalf("allocation for a portless app refused: %+v", resp.Error)
	}
	var res protocol.AllocateResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Slot != 1 {
		t.Errorf("slot = %d, want 1", res.Slot)
	}
	if res.Resources["vm"].Value != "vm-app-alpha-1" {
		t.Errorf("vm resource = %+v", res.Resources["vm"])
	}
}

// TestDescriptionWordBound: the description field is for ten words or
// fewer, enforced at the coordinator.
func TestDescriptionWordBound(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	ten := "one two three four five six seven eight nine ten"
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "alpha", Path: "/tmp/wt/alpha", Description: ten,
	})
	if resp.Error != nil {
		t.Fatalf("ten-word description refused: %+v", resp.Error)
	}
	eleven := ten + " eleven"
	resp = h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "beta", Path: "/tmp/wt/beta", Description: eleven,
	})
	if resp.Error == nil || resp.Error.Code != 3 || !strings.Contains(resp.Error.Msg, "ten words or fewer") {
		t.Fatalf("eleven-word description = %+v, want a refusal naming the bound", resp.Error)
	}
}

// TestSecretsAreRecordedForTheOwner: allocate records the seed credentials
// and serves them back to the owning client alone in the allocate result.
func TestSecretsAreRecordedForTheOwner(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "alpha", Path: "/tmp/wt/alpha",
		Secrets: map[string]string{"admin_password": "hunter2"},
	})
	if resp.Error != nil {
		t.Fatalf("allocation refused: %+v", resp.Error)
	}
	var res protocol.AllocateResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatal(err)
	}
	if res.Secrets["admin_password"] != "hunter2" {
		t.Errorf("secrets were not served back to the owner: %+v", res.Secrets)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if reg.Entries[0].Secrets["admin_password"] != "hunter2" {
		t.Errorf("secrets were not recorded: %+v", reg.Entries[0].Secrets)
	}
}

// TestEphemeralEntryRecorded: an ephemeral client's entry records the
// ephemeral flag; ageing those out is phase 6's work, not this phase's.
func TestEphemeralEntryRecorded(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindEphemeral, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "alpha", Path: "/tmp/wt/alpha",
	})
	if resp.Error != nil {
		t.Fatalf("allocation refused: %+v", resp.Error)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Entries[0]
	if !e.Ephemeral {
		t.Error("ephemeral flag not recorded on the entry")
	}
	if e.Owner != sess.Identity.Key || e.OwnerKind != protocol.KindEphemeral {
		t.Errorf("owner = %s/%s, want the ephemeral session id", e.OwnerKind, e.Owner)
	}
}

func ptr[T any](v T) *T { return &v }

// TestAllocationAcrossAppsSharesNothing: slots are per app — compose-app
// slot 1 and plain-app slot 1 coexist, and each app's band feeds only its
// own resolution (02-coordination.md §5.1).
func TestAllocationAcrossAppsSharesNothing(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	ca := testSpec(t, "compose-app", 8)
	pa := testSpec(t, "plain-app", 8)
	registerBand(t, h, sess, ca, 4200)
	registerBand(t, h, sess, pa, 8200)

	r1, perr := allocate(t, h, sess, ca, "alpha")
	if perr != nil {
		t.Fatalf("compose-app allocation refused: %+v", perr)
	}
	r2, perr := allocate(t, h, sess, pa, "alpha")
	if perr != nil {
		t.Fatalf("plain-app allocation refused: %+v", perr)
	}
	if r1.Slot != 1 || r2.Slot != 1 {
		t.Errorf("slots = %d, %d; both apps should start at 1", r1.Slot, r2.Slot)
	}
	if r1.Resources["api"].Value == r2.Resources["api"].Value {
		t.Errorf("both apps derived the same port %v; bands are per app", r1.Resources["api"].Value)
	}
}

// TestMalformedRequestsAreRefused: a request that cannot be decoded is a
// failure naming the problem, never a panic.
func TestMalformedRequestsAreRefused(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	ctx := context.Background()
	for _, verb := range []string{verbAllocate, verbActivate, verbRelease, verbBandsReserve} {
		resp := h.Request(ctx, sess, verb, "not an object")
		if resp.Error == nil {
			t.Errorf("%s with malformed args succeeded, want a refusal", verb)
		}
		if resp.Error.Code != 1 {
			t.Errorf("%s malformed code = %d, want 1", verb, resp.Error.Code)
		}
	}
}

// TestAllocationEntryIsDenormalised: the registry entry carries the derived
// resources so a cross-repo list never needs the repo's spec
// (ARCHITECTURE.md §8.3).
func TestAllocationEntryIsDenormalised(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)

	res, perr := allocate(t, h, sess, sp, "alpha")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	e := reg.Entries[0]
	if len(e.Resources) != 1 || e.Resources["api"].Value != float64(res.Slot)+4200 {
		t.Errorf("entry resources = %+v, want the denormalised api port for slot %d", e.Resources, res.Slot)
	}
	if e.Path != "/tmp/wt/alpha" {
		t.Errorf("entry path = %q, want the path as the client saw it", e.Path)
	}
}

// TestVersionedStoreFiles: bands.json carries the schema_version envelope
// through the coordinator's writes.
func TestVersionedStoreFiles(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)
	bands, err := h.Store.ReadBands()
	if err != nil {
		t.Fatal(err)
	}
	if bands.SchemaVersion != store.SchemaVersion {
		t.Errorf("bands schema_version = %d, want %d", bands.SchemaVersion, store.SchemaVersion)
	}
}

// TestStoreErrUnparseableRegistry: an unparseable registry is reported with
// the never-truncate remedy, and the file survives.
func TestStoreErrUnparseableRegistry(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	if err := h.Store.WriteFile(store.RegistryFileName, []byte("{not json")); err != nil {
		t.Fatal(err)
	}
	sess, _ := h.Connect(protocol.KindHost, "")
	sp := testSpec(t, "compose-app", 8)
	registerBand(t, h, sess, sp, 4200)
	resp := h.Request(context.Background(), sess, verbAllocate, &protocol.AllocateArgs{
		Spec: *sp, Slug: "alpha", Path: "/tmp/wt/alpha",
	})
	if resp.Error == nil {
		t.Fatal("allocation over an unparseable registry succeeded")
	}
	if !strings.Contains(resp.Error.Remedy, "never truncated") {
		t.Errorf("remedy does not state the never-truncate rule: %s", resp.Error.Remedy)
	}
	if _, err := os.Stat(filepath.Join(h.Store.Root(), store.RegistryFileName)); err != nil {
		t.Errorf("the registry file vanished: %v", err)
	}
}
