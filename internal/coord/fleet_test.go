package coord

// fleet_test.go exercises phase 6's coordinator surface: the list markers
// and secret redaction, doctor's findings (every one produced by a fixture
// and naming a command — exit criterion 5), doctor writing nothing,
// reconcile's eligibility and repairs (exit criterion 3's coordinator
// half), clients.list, reclamation, the spec cache and the reaper's spec
// allowlist. The docker-dependent halves live in the acceptance-tagged
// files.

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io/fs"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/descriptor"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// fleetSpec is the allocation spec the fleet tests run against: a port and
// a state path, the .env block (for doctor's duplicate-key finding), no
// hooks, no docker. The band must be registered for the port resource.
func fleetSpec(t *testing.T) *spec.Spec {
	t.Helper()
	max := 8
	s := &spec.Spec{
		Version: 1, App: "fleet-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
			{Type: "state-path", Name: "db",
				Template: strPtr("{home}/.fleet-app/{slug}-{slot}/db.sqlite")},
		},
		Emit: spec.Emit{
			Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"},
			Env:        &spec.EnvEmit{Path: ".env", Keys: map[string]string{"API_PORT": "{api}"}},
		},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("fleet spec does not validate: %v", err)
	}
	return s
}

// registerFleetBand registers an app's band through the handler.
func registerFleetBand(t *testing.T, h *Harness, sp *spec.Spec, base int) {
	t.Helper()
	sess, reply := h.ConnectPeer(Peer{UID: 4242, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	resp := h.Request(context.Background(), sess, verbBandsReserve,
		&protocol.ReserveBandArgs{Spec: *sp, Bases: map[string]int{"api": base}})
	if resp.Error != nil {
		t.Fatalf("bands.reserve refused: %+v", resp.Error)
	}
}

// fleetHarness builds the harness with the driver registry installed, the
// band registered and a short reclamation interval.
func fleetHarness(t *testing.T) *Harness {
	t.Helper()
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	h.H.ReclaimInterval = time.Hour
	registerFleetBand(t, h, fleetSpec(t), 7000)
	return h
}

// fleetEntry writes one entry straight into the registry, bypassing
// allocation, for the marker and doctor fixtures.
func fleetEntry(t *testing.T, h *Harness, e *store.Entry) {
	t.Helper()
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	reg.Entries = append(reg.Entries, *e)
	if err := h.Store.WriteRegistry(reg); err != nil {
		t.Fatalf("writing the registry: %v", err)
	}
}

// baseFleetEntry is the shared entry shape the marker fixtures mutate.
func baseFleetEntry(path string, visible bool) *store.Entry {
	now := time.Now().UTC().Format(time.RFC3339Nano)
	resources := map[string]spec.Resolved{
		"api": {Type: "port", Value: 7001},
		"db":  {Type: "state-path", Value: "/home/.fleet-app/wt-1-1/db.sqlite"},
	}
	return &store.Entry{
		App: "fleet-app", Slug: "wt-1", Slot: 1,
		Owner: "4242", OwnerKind: protocol.KindHost,
		Path: path, PathVisible: visible, State: store.StateActive,
		Resources: resources, CreatedAt: now, LastSeen: now,
	}
}

// listEntries runs the list verb and returns the decoded result.
func listEntries(t *testing.T, h *Harness, sess *Session, wide bool) protocol.ListResult {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbList, &protocol.ListArgs{Wide: wide})
	if resp.Error != nil {
		t.Fatalf("list refused: %+v", resp.Error)
	}
	var res protocol.ListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding the list result: %v", err)
	}
	return res
}

// TestListStaleAndUnverifiableStayDistinct is exit criterion 4's marker
// half: an entry whose path the coordinator cannot stat is unverifiable,
// never stale, and an entry whose visible directory is gone is stale,
// never unverifiable.
func TestListStaleAndUnverifiableStayDistinct(t *testing.T) {
	h := fleetHarness(t)
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	gone := filepath.Join(tempRoot(t), "gone")
	containerPath := filepath.Join("/container", "worktrees", "wt-2")

	goneEntry := baseFleetEntry(gone, true) // visible, gone → stale
	fleetEntry(t, h, goneEntry)
	containerEntry := baseFleetEntry(containerPath, false) // invisible → unverifiable
	containerEntry.Slug = "wt-2"
	fleetEntry(t, h, containerEntry)
	live := baseFleetEntry(tempRoot(t), true)
	live.Slug = "wt-3"
	if err := os.MkdirAll(live.Path, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}
	fleetEntry(t, h, live) // visible, present → neither marker

	res := listEntries(t, h, sess, false)
	bySlug := map[string][]string{}
	for _, e := range res.Entries {
		bySlug[e.Slug] = e.Flags
	}
	if !hasFlag(bySlug["wt-1"], "stale") || hasFlag(bySlug["wt-1"], "unverifiable") {
		t.Errorf("wt-1 flags = %v, want stale and not unverifiable", bySlug["wt-1"])
	}
	if !hasFlag(bySlug["wt-2"], "unverifiable") || hasFlag(bySlug["wt-2"], "stale") {
		t.Errorf("wt-2 flags = %v, want unverifiable and never stale", bySlug["wt-2"])
	}
	if len(bySlug["wt-3"]) != 0 {
		t.Errorf("wt-3 flags = %v, want none (visible and present)", bySlug["wt-3"])
	}
}

// TestListForeignAndReclaimableMarkers: an entry owned by another client is
// foreign; an ephemeral entry whose owner has aged out past the reclamation
// interval is reclaimable.
func TestListForeignAndReclaimableMarkers(t *testing.T) {
	h := fleetHarness(t)
	host, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")

	// The other host client's entry: foreign to the first host.
	foreign := baseFleetEntry(tempRoot(t), true)
	foreign.Owner, foreign.OwnerKind = "5000", protocol.KindHost
	fleetEntry(t, h, foreign)

	// An ephemeral client's entry, aged out: reclaimable (and foreign).
	eph, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}
	ephEntry := baseFleetEntry(filepath.Join("/container", "wt-eph"), false)
	ephEntry.Slug = "wt-eph"
	ephEntry.Owner, ephEntry.OwnerKind = eph.Identity.Key, protocol.KindEphemeral
	ephEntry.Ephemeral = true
	fleetEntry(t, h, ephEntry)
	// Age the ephemeral client out: its last-seen becomes two intervals ago.
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("reading clients: %v", err)
	}
	for i := range clients.Clients {
		if clients.Clients[i].Kind == protocol.KindEphemeral {
			clients.Clients[i].LastSeen = time.Now().UTC().Add(-2 * h.H.ReclaimInterval).Format(time.RFC3339Nano)
		}
	}
	if err := h.Store.WriteClients(clients); err != nil {
		t.Fatalf("writing clients: %v", err)
	}

	res := listEntries(t, h, host, false)
	bySlug := map[string][]string{}
	for _, e := range res.Entries {
		bySlug[e.Slug] = e.Flags
	}
	if !hasFlag(bySlug["wt-1"], "foreign") {
		t.Errorf("host's view of the other client's entry = %v, want foreign", bySlug["wt-1"])
	}
	if !hasFlag(bySlug["wt-eph"], "reclaimable") || !hasFlag(bySlug["wt-eph"], "foreign") {
		t.Errorf("eph entry flags = %v, want reclaimable and foreign", bySlug["wt-eph"])
	}
	// The ephemeral client itself sees its own entry as reclaimable but not
	// foreign.
	res2 := listEntries(t, h, eph, false)
	for _, e := range res2.Entries {
		if e.Slug == "wt-eph" {
			if hasFlag(e.Flags, "foreign") || !hasFlag(e.Flags, "reclaimable") {
				t.Errorf("eph's own view = %v, want reclaimable and not foreign", e.Flags)
			}
		}
	}
}

// TestListSecretsRedactedExceptOwnerWide is the security rail: seed
// credentials are served to the owning client alone, and only under --wide;
// `wt list` from any other client — an agent container included — cannot
// read them.
func TestListSecretsRedactedExceptOwnerWide(t *testing.T) {
	h := fleetHarness(t)
	owner, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	other, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("second hello refused: %+v", reply.Error)
	}
	e := baseFleetEntry(tempRoot(t), true)
	e.Secrets = map[string]string{"admin_password": "hunter2", "token": "s3cret"}
	fleetEntry(t, h, e)

	cases := []struct {
		name string
		sess *Session
		wide bool
		want bool // secrets present?
	}{
		{"owner, no wide", owner, false, false},
		{"owner, wide", owner, true, true},
		{"other, no wide", other, false, false},
		{"other, wide", other, true, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			res := listEntries(t, h, c.sess, c.wide)
			got := res.Entries[0].Secrets
			if c.want && (got == nil || got["admin_password"] != "hunter2") {
				t.Errorf("secrets = %+v, want the owner's credentials under --wide", got)
			}
			if !c.want && got != nil {
				t.Errorf("secrets = %+v, want redacted", got)
			}
		})
	}
}

// TestClientsListShowsEntriesAndAgedOut: the client table reports kind,
// measured last-seen, entries owned, and which ephemeral clients have aged
// out.
func TestClientsListShowsEntriesAndAgedOut(t *testing.T) {
	h := fleetHarness(t)
	host, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	eph, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}
	// One entry each.
	e1 := baseFleetEntry(tempRoot(t), true)
	e1.Owner, e1.OwnerKind = host.Identity.Key, protocol.KindHost
	fleetEntry(t, h, e1)
	e2 := baseFleetEntry(filepath.Join("/container", "wt-eph"), false)
	e2.Owner, e2.OwnerKind = eph.Identity.Key, protocol.KindEphemeral
	e2.Ephemeral = true
	fleetEntry(t, h, e2)

	// Age the ephemeral client out.
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("reading clients: %v", err)
	}
	for i := range clients.Clients {
		if clients.Clients[i].Kind == protocol.KindEphemeral {
			clients.Clients[i].LastSeen = time.Now().UTC().Add(-2 * h.H.ReclaimInterval).Format(time.RFC3339Nano)
		}
	}
	if err := h.Store.WriteClients(clients); err != nil {
		t.Fatalf("writing clients: %v", err)
	}

	resp := h.Request(context.Background(), host, verbClients, nil)
	if resp.Error != nil {
		t.Fatalf("clients.list refused: %+v", resp.Error)
	}
	var res protocol.ClientsListResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(res.Clients) != 2 {
		t.Fatalf("clients = %d, want 2", len(res.Clients))
	}
	var hostRow, ephRow *protocol.ClientInfo
	for i := range res.Clients {
		switch res.Clients[i].Kind {
		case protocol.KindHost:
			hostRow = &res.Clients[i]
		case protocol.KindEphemeral:
			ephRow = &res.Clients[i]
		}
	}
	if hostRow == nil || hostRow.Entries != 1 || hostRow.AgedOut {
		t.Errorf("host row = %+v, want 1 entry and not aged out", hostRow)
	}
	if ephRow == nil || ephRow.Entries != 1 || !ephRow.Ephemeral || !ephRow.AgedOut {
		t.Errorf("ephemeral row = %+v, want 1 entry, ephemeral and aged out", ephRow)
	}
}

// TestReclaimEphemeralReclaimsAgedOutOnly: reclamation tears down an aged
// out ephemeral client's entries by handle (the spec comes from the cache —
// the entry path is not visible to the host) and leaves a live client's
// entries and every host entry alone.
func TestReclaimEphemeralReclaimsAgedOutOnly(t *testing.T) {
	h := fleetHarness(t)
	sp := fleetSpec(t)
	host, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	eph, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}

	// Allocate through the verb, so the spec cache is written (the cache is
	// what reclamation falls back to for the invisible container path).
	for _, slug := range []string{"wt-1", "wt-2"} {
		if _, perr := allocate(t, h, host, sp, slug); perr != nil {
			t.Fatalf("host allocation refused: %+v", perr)
		}
	}
	ephSlug := "wt-eph"
	if _, perr := allocate(t, h, eph, sp, ephSlug); perr != nil {
		t.Fatalf("ephemeral allocation refused: %+v", perr)
	}

	// The spec cache was written by the allocations.
	specs, err := h.Store.ReadSpecs()
	if err != nil {
		t.Fatalf("reading the spec cache: %v", err)
	}
	if _, ok := specs.Specs["fleet-app"]; !ok {
		t.Fatal("allocate did not record the spec in the cache")
	}

	// Age the ephemeral client out by two intervals.
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("reading clients: %v", err)
	}
	for i := range clients.Clients {
		if clients.Clients[i].Kind == protocol.KindEphemeral {
			clients.Clients[i].LastSeen = time.Now().UTC().Add(-2 * h.H.ReclaimInterval).Format(time.RFC3339Nano)
		}
	}
	if err := h.Store.WriteClients(clients); err != nil {
		t.Fatalf("writing clients: %v", err)
	}

	reclaimed, err := h.H.ReclaimEphemeral(time.Now())
	if err != nil {
		t.Fatalf("reclaiming: %v", err)
	}
	if reclaimed != 1 {
		t.Errorf("reclaimed = %d, want the one aged-out ephemeral entry", reclaimed)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	for _, e := range reg.Entries {
		if e.Slug == ephSlug {
			t.Errorf("the aged-out ephemeral entry %s survived reclamation", ephSlug)
		}
		if e.Slug != "wt-1" && e.Slug != "wt-2" {
			t.Errorf("unexpected surviving entry %s/%s", e.App, e.Slug)
		}
	}
	if len(reg.Entries) != 2 {
		t.Errorf("entries = %d, want the two host entries only", len(reg.Entries))
	}
}

// TestReclaimEphemeralSkipsWithoutSpec: an aged-out entry whose spec is
// unavailable — no visible path and no cache — is skipped with the bound
// stated, never torn down blind.
func TestReclaimEphemeralSkipsWithoutSpec(t *testing.T) {
	h := fleetHarness(t)
	eph, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}
	// Write the entry directly, bypassing allocate, so no spec is cached.
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fleetEntry(t, h, &store.Entry{
		App: "fleet-app", Slug: "wt-orphan", Slot: 1,
		Owner: eph.Identity.Key, OwnerKind: protocol.KindEphemeral, Ephemeral: true,
		Path: "/container/gone", PathVisible: false, State: store.StateActive,
		Resources: map[string]spec.Resolved{"api": {Type: "port", Value: 7001}},
		CreatedAt: now, LastSeen: now,
	})
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("reading clients: %v", err)
	}
	for i := range clients.Clients {
		if clients.Clients[i].Kind == protocol.KindEphemeral {
			clients.Clients[i].LastSeen = time.Now().UTC().Add(-2 * h.H.ReclaimInterval).Format(time.RFC3339Nano)
		}
	}
	if err := h.Store.WriteClients(clients); err != nil {
		t.Fatalf("writing clients: %v", err)
	}
	reclaimed, err := h.H.ReclaimEphemeral(time.Now())
	if err != nil {
		t.Fatalf("reclaiming: %v", err)
	}
	if reclaimed != 0 {
		t.Errorf("reclaimed = %d, want 0 (no spec: the skip is stated, not a blind teardown)", reclaimed)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if len(reg.Entries) != 1 {
		t.Errorf("entries = %d, want the skipped orphan to stay", len(reg.Entries))
	}
}

// activateEntry flips an allocated entry to active, as init's step 6 does.
func activateEntry(t *testing.T, h *Harness, sess *Session, app, slug string) {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbActivate, &protocol.EntryRef{App: app, Slug: slug})
	if resp.Error != nil {
		t.Fatalf("activate refused: %+v", resp.Error)
	}
}

// TestReconcileTearsDownStaleEntryAndDropsIt is exit criterion 3's
// coordinator half: an entry whose directory is gone is repaired by the rm
// sequence — reap, driver teardown by handle, entry drop — and the slot
// frees.
func TestReconcileTearsDownStaleEntryAndDropsIt(t *testing.T) {
	stub := &stubDriver{}
	h := fleetHarness(t)
	h.H.InstallDrivers(driver.NewRegistry(stub))
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	sp := fleetSpec(t)
	res, perr := allocate(t, h, sess, sp, "wt-1")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	activateEntry(t, h, sess, sp.App, res.Slug)

	resp := h.Request(context.Background(), sess, verbReconcile, &protocol.ReconcileArgs{
		App: sp.App, Spec: *sp, Refs: []protocol.EntryRef{{App: sp.App, Slug: res.Slug}},
	})
	if resp.Error != nil {
		t.Fatalf("reconcile refused: %+v", resp.Error)
	}
	var out protocol.ReconcileResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Outcomes) != 1 || out.Outcomes[0].Action != "torn-down" {
		t.Fatalf("outcomes = %+v, want one torn-down", out.Outcomes)
	}
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if registryEntry(reg, sp.App, res.Slug) != nil {
		t.Error("the entry survived reconcile; the slot is not freed")
	}
}

// TestReconcileEligibility: a live client's entry is not this caller's to
// repair; an aged-out ephemeral owner's entry is (reclamation by handle); a
// reserving entry is rolled back only past its timeout.
func TestReconcileEligibility(t *testing.T) {
	stub := &stubDriver{}
	h := fleetHarness(t)
	h.H.InstallDrivers(driver.NewRegistry(stub))
	sp := fleetSpec(t)
	owner, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	other, reply := h.ConnectPeer(Peer{UID: 5000, Known: true}, protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("second hello refused: %+v", reply.Error)
	}
	eph, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}
	ctx := context.Background()

	// The other client's live entry: skipped by the owner's reconcile.
	resForeign, perr := allocate(t, h, other, sp, "wt-foreign")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	activateEntry(t, h, other, sp.App, resForeign.Slug)
	// An ephemeral client's entry, aged out: reclaimable.
	resEph, perr := allocate(t, h, eph, sp, "wt-eph")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	activateEntry(t, h, eph, sp.App, resEph.Slug)
	// The owner's own reserving entry, fresh: within its timeout.
	resOwn, perr := allocate(t, h, owner, sp, "wt-own")
	if perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	clients, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("reading clients: %v", err)
	}
	for i := range clients.Clients {
		if clients.Clients[i].Kind == protocol.KindEphemeral {
			clients.Clients[i].LastSeen = time.Now().UTC().Add(-2 * h.H.ReclaimInterval).Format(time.RFC3339Nano)
		}
	}
	if err := h.Store.WriteClients(clients); err != nil {
		t.Fatalf("writing clients: %v", err)
	}

	resp := h.Request(ctx, owner, verbReconcile, &protocol.ReconcileArgs{
		App: sp.App, Spec: *sp, Refs: []protocol.EntryRef{
			{App: sp.App, Slug: resForeign.Slug},
			{App: sp.App, Slug: resEph.Slug},
			{App: sp.App, Slug: resOwn.Slug},
		},
	})
	if resp.Error != nil {
		t.Fatalf("reconcile refused: %+v", resp.Error)
	}
	var out protocol.ReconcileResult
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	bySlug := map[string]protocol.ReconcileOutcome{}
	for _, oc := range out.Outcomes {
		bySlug[oc.Slug] = oc
	}
	if bySlug[resForeign.Slug].Action != "skipped" || !strings.Contains(bySlug[resForeign.Slug].Note, "owned by") {
		t.Errorf("foreign outcome = %+v, want skipped naming the owner", bySlug[resForeign.Slug])
	}
	if bySlug[resEph.Slug].Action != "torn-down" {
		t.Errorf("aged-out ephemeral outcome = %+v, want torn-down", bySlug[resEph.Slug])
	}
	if bySlug[resOwn.Slug].Action != "skipped" || !strings.Contains(bySlug[resOwn.Slug].Note, "reserving") {
		t.Errorf("fresh reserving outcome = %+v, want skipped within its timeout", bySlug[resOwn.Slug])
	}

	// Backdate the owner's reserving entry past its timeout: reconcile then
	// rolls the allocation back.
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	e := registryEntry(reg, sp.App, resOwn.Slug)
	e.CreatedAt = time.Now().UTC().Add(-2 * ReservingTimeout).Format(time.RFC3339Nano)
	if err := h.Store.WriteRegistry(reg); err != nil {
		t.Fatalf("writing the registry: %v", err)
	}
	resp = h.Request(ctx, owner, verbReconcile, &protocol.ReconcileArgs{
		App: sp.App, Spec: *sp, Refs: []protocol.EntryRef{{App: sp.App, Slug: resOwn.Slug}},
	})
	if resp.Error != nil {
		t.Fatalf("reconcile refused: %+v", resp.Error)
	}
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(out.Outcomes) != 1 || out.Outcomes[0].Action != "rolled-back" {
		t.Errorf("backdated reserving outcome = %+v, want rolled-back", out.Outcomes)
	}
	reg, err = h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if registryEntry(reg, sp.App, resOwn.Slug) != nil {
		t.Error("the rolled-back entry survived")
	}
}

// TestDoctorWritesNothing is the whole doctor contract: the store's files
// are byte-identical before and after a doctor run that produced findings.
func TestDoctorWritesNothing(t *testing.T) {
	h := fleetHarness(t)
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	sp := fleetSpec(t)
	if _, perr := allocate(t, h, sess, sp, "wt-1"); perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	// Something to find: delete the worktree directory.
	os.RemoveAll(filepath.Join("/tmp/wt", "wt-1"))

	snapshot := storeSnapshot(t, h.Store.Root())
	resp := h.Request(context.Background(), sess, verbDoctor, nil)
	if resp.Error != nil {
		t.Fatalf("doctor refused: %+v", resp.Error)
	}
	var res protocol.DoctorResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	if len(res.Findings) == 0 {
		t.Fatal("doctor found nothing in a fixture that should produce findings")
	}
	after := storeSnapshot(t, h.Store.Root())
	for path, h1 := range snapshot {
		h2, ok := after[path]
		if !ok {
			t.Errorf("%s disappeared during doctor", path)
			continue
		}
		if h1 != h2 {
			t.Errorf("%s changed during doctor: doctor wrote", path)
		}
	}
	if len(after) != len(snapshot) {
		t.Errorf("doctor created store files")
	}
}

// storeSnapshot hashes every store file, keyed by path.
func storeSnapshot(t *testing.T, root string) map[string][32]byte {
	t.Helper()
	out := map[string][32]byte{}
	err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		data, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[path] = sha256.Sum256(data)
		return nil
	})
	if err != nil {
		t.Fatalf("snapshotting %s: %v", root, err)
	}
	return out
}

// fleetGit runs one git command for the doctor fixtures.
func fleetGit(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(out)))
	}
}

// gitFleetRepo builds a real repository with one linked worktree and a
// committed spec, returning the main checkout and the worktree root.
func gitFleetRepo(t *testing.T, sp *spec.Spec) (main, worktree string) {
	t.Helper()
	base := tempRoot(t)
	main = filepath.Join(base, "main")
	fleetGit(t, "", "init", "-b", "main", main)
	fleetGit(t, main, "config", "user.email", "t@example.com")
	fleetGit(t, main, "config", "user.name", "T")
	data, err := spec.EmitYAML(sp)
	if err != nil {
		t.Fatalf("emitting the spec: %v", err)
	}
	if err := os.WriteFile(filepath.Join(main, "wt.yaml"), data, 0o644); err != nil {
		t.Fatalf("writing the spec: %v", err)
	}
	fleetGit(t, main, "add", ".")
	fleetGit(t, main, "commit", "-m", "initial")
	worktree = filepath.Join(base, "wt-1")
	fleetGit(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	return main, worktree
}

// doctorHarness is the harness with the driver registry, the band and a
// real repo whose worktree has a registry entry with a real descriptor.
func doctorHarness(t *testing.T) (*Harness, *Session, *spec.Spec, string, string) {
	t.Helper()
	sp := fleetSpec(t)
	main, worktree := gitFleetRepo(t, sp)
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.InstallDrivers(driver.NewRegistry(&driver.Port{}, &driver.Namespace{}, &driver.StatePath{}))
	registerFleetBand(t, h, sp, 7000)
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	fleetEntry(t, h, &store.Entry{
		App: sp.App, Slug: "wt-1", Slot: 1,
		Owner: "4242", OwnerKind: protocol.KindHost,
		Path: worktree, PathVisible: true, State: store.StateActive,
		DescriptorPath: filepath.Join(worktree, "wt-env.yaml"),
		Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 7001},
			"db":  {Type: "state-path", Value: "/home/.fleet-app/wt-1-1/db.sqlite"},
		},
		CreatedAt: now, LastSeen: now,
	})
	// The descriptor, written through the real emitter so the reader accepts
	// it: doctor's descriptor check must see a healthy entry.
	d := &descriptor.Descriptor{
		Version: descriptor.Version, App: sp.App, Slug: "wt-1", Slot: 1,
		Path: worktree, Description: "doctor fixture",
		Resources: map[string]spec.Resolved{
			"api": {Type: "port", Value: 7001},
			"db":  {Type: "state-path", Value: "/home/.fleet-app/wt-1-1/db.sqlite"},
		},
		State:  descriptor.BuildState(sp),
		Extras: map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(worktree, "wt-env.yaml"), "yaml", d); err != nil {
		t.Fatalf("writing the descriptor: %v", err)
	}
	return h, sess, sp, main, worktree
}

// runDoctor runs the doctor verb and returns the findings.
func runDoctor(t *testing.T, h *Harness, sess *Session) protocol.DoctorResult {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbDoctor, nil)
	if resp.Error != nil {
		t.Fatalf("doctor refused: %+v", resp.Error)
	}
	var res protocol.DoctorResult
	if err := json.Unmarshal(resp.Result, &res); err != nil {
		t.Fatalf("decoding: %v", err)
	}
	return res
}

// TestDoctorFindingsEachNameACommand is exit criterion 5: the 06-fleet.md
// §4 findings this phase produces are each produced by a fixture and each
// names the exact command that fixes it.
func TestDoctorFindingsEachNameACommand(t *testing.T) {
	h, sess, sp, main, worktree := doctorHarness(t)

	// A second worktree of the repo with no entry: the "directory present,
	// no entry" fixture.
	uninit := filepath.Join(filepath.Dir(worktree), "wt-uninit")
	fleetGit(t, main, "worktree", "add", "-b", "wt-uninit", uninit, "main")
	t.Cleanup(func() { os.RemoveAll(uninit) })

	// A managed key defined outside the block: the .env duplicate fixture.
	if err := os.WriteFile(filepath.Join(worktree, ".env"), []byte("API_PORT=9999\n"), 0o644); err != nil {
		t.Fatalf("writing the .env: %v", err)
	}

	// An entry whose visible directory is gone: the stale fixture.
	stale := baseFleetEntry(filepath.Join(tempRoot(t), "gone-dir"), true)
	stale.Slug = "wt-stale"
	fleetEntry(t, h, stale)

	// A listener holding the entry's port: the resource-drift fixture.
	l, err := net.Listen("tcp", "127.0.0.1:7001")
	if err != nil {
		t.Fatalf("binding port 7001: %v", err)
	}
	defer l.Close()

	// A second app's band overlapping this one's: the band-overlap fixture.
	max := 8
	other := &spec.Spec{Version: 1, App: "other-app",
		Slots:     spec.Slots{Max: &max},
		Resources: []spec.Resource{{Type: "port", Name: "api"}},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(other); err != nil {
		t.Fatalf("other spec: %v", err)
	}
	registerFleetBand(t, h, other, 7000) // same base: the ranges overlap

	// Six more entries push the app to 7 of max 8 occupied, so fewer than a
	// quarter of the slots remain: the slot-ceiling fixture.
	for i := 2; i <= 7; i++ {
		now := time.Now().UTC().Format(time.RFC3339Nano)
		fleetEntry(t, h, &store.Entry{
			App: sp.App, Slug: fmt.Sprintf("wt-%d", i), Slot: i,
			Owner: "4242", OwnerKind: protocol.KindHost,
			Path: worktree, PathVisible: true, State: store.StateActive,
			Resources: map[string]spec.Resolved{
				"api": {Type: "port", Value: 7000 + i},
				"db":  {Type: "state-path", Value: fmt.Sprintf("/home/.fleet-app/wt-%d-%d/db.sqlite", i, i)},
			},
			CreatedAt: now, LastSeen: now,
		})
	}

	res := runDoctor(t, h, sess)
	if len(res.Findings) == 0 {
		t.Fatal("the doctor fixture produced no findings")
	}
	// Every §4 row this phase produces, with a remedy naming a command.
	want := []string{
		"the worktree directory",            // entry present, directory gone → wt rm / wt reconcile
		"present but has no registry entry", // directory present, no entry → wt init
		"managed key",                       // duplicate managed key outside the block → wt init
		"port 7001 is bound",                // resource drift → wt init
		"bands of app",                      // band ledger overlap → wt bands reserve
		"approaching its slot ceiling",      // slot ceiling → wt cleanup / wt rm
		"names no reaper binaries",          // reaper can never signal → reaper.binaries
	}
	for _, substr := range want {
		found := false
		for _, f := range res.Findings {
			if !strings.Contains(f.Message, substr) {
				continue
			}
			found = true
			if f.Level == "info" {
				t.Errorf("finding %q is info-level; a §4 finding reports a problem", f.Message)
			}
			if f.Remedy == "" {
				t.Errorf("finding %q has no remedy", f.Message)
			} else if !strings.Contains(f.Remedy, "wt ") && !strings.Contains(f.Remedy, "wt.yaml") {
				t.Errorf("finding %q remedy %q names no command", f.Message, f.Remedy)
			}
		}
		if !found {
			t.Errorf("no finding matches %q; findings:\n%s", substr, findingsDump(res))
		}
	}
}

// findingsDump renders findings for a failure message.
func findingsDump(res protocol.DoctorResult) string {
	var b strings.Builder
	for _, f := range res.Findings {
		fmt.Fprintf(&b, "  [%s] %s (fix: %s)\n", f.Level, f.Message, f.Remedy)
	}
	for _, n := range res.Notes {
		fmt.Fprintf(&b, "  note: %s\n", n)
	}
	return b.String()
}

// TestDoctorReservingAndTearingDownFindings: a reserving entry past its
// timeout and a tearing-down entry with survivors both produce findings
// naming their commands.
func TestDoctorReservingAndTearingDownFindings(t *testing.T) {
	h, sess, _, _, worktree := doctorHarness(t)

	reserving := baseFleetEntry(worktree, true)
	reserving.Slug = "wt-reserving"
	reserving.State = store.StateReserving
	reserving.CreatedAt = time.Now().UTC().Add(-2 * ReservingTimeout).Format(time.RFC3339Nano)
	fleetEntry(t, h, reserving)

	td := baseFleetEntry(worktree, true)
	td.Slug = "wt-td"
	td.State = store.StateTearingDown
	td.TeardownNote = "teardown left resources behind: container c1"
	fleetEntry(t, h, td)

	res := runDoctor(t, h, sess)
	found := map[string]bool{}
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "reserving since") {
			found["reserving"] = strings.Contains(f.Remedy, "wt reconcile")
		}
		if strings.Contains(f.Message, "tearing-down") {
			found["tearing-down"] = strings.Contains(f.Remedy, "wt rm")
		}
	}
	if !found["reserving"] {
		t.Errorf("no reserving-overdue finding naming 'wt reconcile': %s", findingsDump(res))
	}
	if !found["tearing-down"] {
		t.Errorf("no tearing-down finding naming 'wt rm': %s", findingsDump(res))
	}
}

// TestDoctorDescriptorMissingAndSpecMissing: deleting the descriptor
// produces the descriptor-missing finding; deleting the committed spec
// produces the spec-missing finding — both fix with wt init.
func TestDoctorDescriptorMissingAndSpecMissing(t *testing.T) {
	h, sess, _, main, worktree := doctorHarness(t)

	if err := os.Remove(filepath.Join(worktree, "wt-env.yaml")); err != nil {
		t.Fatalf("removing the descriptor: %v", err)
	}
	res := runDoctor(t, h, sess)
	found := false
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "descriptor at") && strings.Contains(f.Remedy, "wt init") {
			found = true
		}
	}
	if !found {
		t.Errorf("no descriptor-missing finding naming 'wt init': %s", findingsDump(res))
	}

	// Restore the descriptor, remove the committed spec from the branch: the
	// walk-up finds nothing and the checks are skipped, stated.
	d := &descriptor.Descriptor{
		Version: descriptor.Version, App: "fleet-app", Slug: "wt-1", Slot: 1,
		Path: worktree, Description: "doctor fixture",
		Resources: map[string]spec.Resolved{"api": {Type: "port", Value: 7001}},
		State:     descriptor.BuildState(&spec.Spec{}), Extras: map[string]any{},
	}
	if err := descriptor.Write(filepath.Join(worktree, "wt-env.yaml"), "yaml", d); err != nil {
		t.Fatalf("writing the descriptor: %v", err)
	}
	if err := os.Remove(filepath.Join(main, "wt.yaml")); err != nil {
		t.Fatalf("removing the spec: %v", err)
	}
	// The worktree is a checkout of the same branch: the file is gone there
	// too.
	if err := os.Remove(filepath.Join(worktree, "wt.yaml")); err != nil {
		t.Fatalf("removing the spec from the worktree: %v", err)
	}
	res = runDoctor(t, h, sess)
	found = false
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "no spec can be found") && strings.Contains(f.Remedy, "wt init") {
			found = true
		}
	}
	if !found {
		t.Errorf("no spec-missing finding: %s", findingsDump(res))
	}
}

// TestDoctorUnverifiableIsObservationNotFinding: an unverifiable entry is
// reported as unverifiable — an observation without a remedy — never as
// stale.
func TestDoctorUnverifiableIsObservationNotFinding(t *testing.T) {
	h, sess, _, _, _ := doctorHarness(t)
	e := baseFleetEntry(filepath.Join("/container", "wt-2"), false)
	e.Slug = "wt-2"
	fleetEntry(t, h, e)
	res := runDoctor(t, h, sess)
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "not visible to the coordinator") {
			if f.Level != "info" {
				t.Errorf("unverifiable level = %q, want info (an observation)", f.Level)
			}
			if f.Remedy != "" {
				t.Errorf("unverifiable remedy = %q, want none (nothing to fix)", f.Remedy)
			}
			return
		}
	}
	t.Errorf("no unverifiable observation: %s", findingsDump(res))
}

// TestDoctorReportsNewerRegistry: a registry written by a newer schema
// version is listed, reported with the upgrade remedy, and never written
// back.
func TestDoctorReportsNewerRegistry(t *testing.T) {
	h, sess, _, _, _ := doctorHarness(t)
	reg, err := h.Store.ReadRegistry()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	reg.SchemaVersion = store.SchemaVersion + 1
	if err := h.Store.Save(store.RegistryFileName, &reg); err != nil {
		t.Fatalf("writing a newer registry: %v", err)
	}
	res := runDoctor(t, h, sess)
	found := false
	for _, f := range res.Findings {
		if strings.Contains(f.Message, "schema version") && strings.Contains(f.Remedy, "upgrade wtd") {
			found = true
		}
	}
	if !found {
		t.Errorf("no newer-registry finding naming the upgrade: %s", findingsDump(res))
	}
	after, err := h.Store.ReadRegistryList()
	if err != nil {
		t.Fatalf("reading the registry: %v", err)
	}
	if after.SchemaVersion != store.SchemaVersion+1 {
		t.Errorf("registry schema version = %d, want the newer %d (doctor wrote)", after.SchemaVersion, store.SchemaVersion+1)
	}
}

// TestReapBinariesDefaultsToSpecField: the reaper's allowlist seam reads
// reaper.binaries from the spec by default, and a spec that names nothing
// signals nothing.
func TestReapBinariesDefaultsToSpecField(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sp := &spec.Spec{Version: 1, App: "x", Reaper: spec.Reaper{Binaries: []string{"compose-app-dev"}}}
	got := h.H.reapBinaries(sp)
	if len(got) != 1 || got[0] != "compose-app-dev" {
		t.Errorf("reapBinaries = %v, want [compose-app-dev] from the spec", got)
	}
	sp.Reaper.Binaries = nil
	if got := h.H.reapBinaries(sp); len(got) != 0 {
		t.Errorf("reapBinaries = %v, want none for a spec that names nothing", got)
	}
}

// TestSpecCacheRoundTrip: allocate records the spec in the cache, and the
// cache survives the store's atomic write.
func TestSpecCacheRoundTrip(t *testing.T) {
	h := fleetHarness(t)
	sp := fleetSpec(t)
	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	if _, perr := allocate(t, h, sess, sp, "wt-1"); perr != nil {
		t.Fatalf("allocation refused: %+v", perr)
	}
	specs, err := h.Store.ReadSpecs()
	if err != nil {
		t.Fatalf("reading the cache: %v", err)
	}
	cached, ok := specs.Specs["fleet-app"]
	if !ok {
		t.Fatal("the spec cache lacks the app")
	}
	if cached.App != "fleet-app" || len(cached.Resources) != 2 {
		t.Errorf("cached spec = %+v", cached)
	}
}

// hasFlag is the marker helper.
func hasFlag(flags []string, want string) bool {
	for _, f := range flags {
		if f == want {
			return true
		}
	}
	return false
}
