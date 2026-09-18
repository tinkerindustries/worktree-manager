package store

import (
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// testEntry is the shared entry shape the registry fixtures mutate.
func testEntry() Entry {
	now := "2026-08-12T10:00:00.123456789Z"
	return Entry{
		App: "compose-app", Slug: "brisk-otter", Slot: 1,
		Owner: "4242", OwnerKind: "host", Ephemeral: false,
		Path: "/Users/test/wt/brisk-otter", PathVisible: true,
		State: StateActive,
		Resources: map[string]spec.Resolved{
			"api":     {Type: "port", Value: 4201},
			"compose": {Type: "namespace", Value: "compose-app-brisk-otter-1"},
		},
		DescriptorPath: "/Users/test/wt/brisk-otter/wt-env.yaml",
		Description:    "two worktrees side by side",
		Secrets:        map[string]string{"admin_password": "hunter2"},
		CreatedAt:      now, LastSeen: now,
	}
}

// TestRegistryRoundTrip: entries survive an upsert and a read, including
// the JSON resources and secrets columns; the port value comes back as an
// int, the same normalisation the JSON file applied.
func TestRegistryRoundTrip(t *testing.T) {
	root := tempRoot(t)
	st := openStore(t, root)
	want := testEntry()
	if err := st.UpsertEntry(want); err != nil {
		t.Fatalf("UpsertEntry: %v", err)
	}
	got, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if len(got.Entries) != 1 {
		t.Fatalf("read back %d entries, want 1", len(got.Entries))
	}
	e := got.Entries[0]
	if e.App != want.App || e.Slug != want.Slug || e.Slot != want.Slot ||
		e.Owner != want.Owner || e.OwnerKind != want.OwnerKind || !e.PathVisible ||
		e.State != want.State || e.Description != want.Description ||
		e.DescriptorPath != want.DescriptorPath {
		t.Errorf("entry round trip changed it:\n got %+v\nwant %+v", e, want)
	}
	if v, ok := e.Resources["api"].Value.(int); !ok || v != 4201 {
		t.Errorf("port resource round trip = %#v, want int 4201", e.Resources["api"].Value)
	}
	if e.Resources["compose"].Value != "compose-app-brisk-otter-1" {
		t.Errorf("compose resource round trip = %+v", e.Resources["compose"])
	}
	if e.Secrets["admin_password"] != "hunter2" {
		t.Errorf("secrets round trip = %+v", e.Secrets)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}
}

// TestGetEntry: one row by app and slug, with the not-found outcome
// distinct from an error.
func TestGetEntry(t *testing.T) {
	st := openStore(t, tempRoot(t))
	if e, ok, err := st.GetEntry("compose-app", "brisk-otter"); err != nil || ok || e != nil {
		t.Errorf("GetEntry of a missing entry = (%v, %v, %v), want (nil, false, nil)", e, ok, err)
	}
	want := testEntry()
	if err := st.UpsertEntry(want); err != nil {
		t.Fatal(err)
	}
	e, ok, err := st.GetEntry("compose-app", "brisk-otter")
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%v, %v, %v)", e, ok, err)
	}
	if e.Slot != 1 || e.State != StateActive {
		t.Errorf("GetEntry = %+v", e)
	}
}

// TestDuplicateSlotRefusedByDatabase is the R0 criterion that the
// constraint carries its own weight: two entries with the same (app,
// slot) inserted directly through the store — no coordinator, no mutex —
// with the second refused by the database's UNIQUE (app, slot) index.
func TestDuplicateSlotRefusedByDatabase(t *testing.T) {
	st := openStore(t, tempRoot(t))
	first := testEntry()
	if err := st.UpsertEntry(first); err != nil {
		t.Fatalf("first upsert: %v", err)
	}
	second := testEntry()
	second.Slug = "another-slug" // same app, same slot, different key
	if err := st.UpsertEntry(second); err == nil {
		t.Fatal("upserting a second entry with the same (app, slot) succeeded; the database must refuse it")
	} else if !strings.Contains(err.Error(), "UNIQUE constraint failed: entries.app, entries.slot") {
		t.Errorf("refusal = %v, want the database's UNIQUE (app, slot) constraint to be named", err)
	}
	// The first entry is untouched.
	got, err := st.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Entries) != 1 || got.Entries[0].Slug != "brisk-otter" {
		t.Errorf("registry after the refused upsert = %+v, want only the first entry", got.Entries)
	}
}

// TestEntryLifecycle: the state transition and last-seen move, and the
// entry drop, all through the row-level mutations.
func TestEntryLifecycle(t *testing.T) {
	st := openStore(t, tempRoot(t))
	if err := st.UpsertEntry(testEntry()); err != nil {
		t.Fatal(err)
	}
	seen := time.Date(2026, 8, 13, 9, 0, 0, 0, time.UTC)
	if err := st.UpdateEntryState("compose-app", "brisk-otter", StateTearingDown, "teardown left the compose project behind"); err != nil {
		t.Fatal(err)
	}
	if err := st.TouchEntry("compose-app", "brisk-otter", seen); err != nil {
		t.Fatal(err)
	}
	e, ok, err := st.GetEntry("compose-app", "brisk-otter")
	if err != nil || !ok {
		t.Fatalf("GetEntry = (%v, %v, %v)", e, ok, err)
	}
	if e.State != StateTearingDown || !strings.Contains(e.TeardownNote, "compose project") {
		t.Errorf("entry after the state update = %+v", e)
	}
	if e.LastSeen != "2026-08-13T09:00:00Z" {
		t.Errorf("last_seen = %q, want the touched time in RFC3339Nano UTC", e.LastSeen)
	}
	if err := st.DeleteEntry("compose-app", "brisk-otter"); err != nil {
		t.Fatal(err)
	}
	if e, ok, err := st.GetEntry("compose-app", "brisk-otter"); err != nil || ok {
		t.Errorf("GetEntry after DeleteEntry = (%v, %v, %v), want gone", e, ok, err)
	}
}

// TestWithTxRollsBack: a transaction whose function fails applies
// nothing — the half-apply rail the coordinator leans on for teardown
// state writes and reclamation.
func TestWithTxRollsBack(t *testing.T) {
	st := openStore(t, tempRoot(t))
	boom := errors.New("boom")
	err := st.WithTx(func(tx *Tx) error {
		if err := tx.UpsertEntry(testEntry()); err != nil {
			return err
		}
		if err := tx.UpsertClient(ClientEntry{Identity: "4242", Kind: "host", LastSeen: "2026-08-13T09:00:00Z"}); err != nil {
			return err
		}
		return boom
	})
	if !errors.Is(err, boom) {
		t.Fatalf("WithTx = %v, want the function's error", err)
	}
	reg, err := st.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 0 {
		t.Errorf("the rolled-back transaction left %d entries", len(reg.Entries))
	}
	clients, err := st.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(clients.Clients) != 0 {
		t.Errorf("the rolled-back transaction left %d clients", len(clients.Clients))
	}
	// A committed transaction applies everything.
	if err := st.WithTx(func(tx *Tx) error {
		if err := tx.UpsertEntry(testEntry()); err != nil {
			return err
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	reg, err = st.ReadRegistry()
	if err != nil {
		t.Fatal(err)
	}
	if len(reg.Entries) != 1 {
		t.Errorf("the committed transaction left %d entries, want 1", len(reg.Entries))
	}
}

// TestBandsRoundTrip: app bands and host reservations survive an upsert
// and a read, bases with their spans and reservations with their ports,
// names and note.
func TestBandsRoundTrip(t *testing.T) {
	st := openStore(t, tempRoot(t))
	if err := st.UpsertBand(Band{App: "compose-app", Bases: map[string]int{"api": 4200, "proxy": 4200}, Spans: map[string]int{"api": 64, "proxy": 64}}); err != nil {
		t.Fatalf("UpsertBand: %v", err)
	}
	if err := st.AddReservation(Reservation{Ports: []int{5319, 5320}, Names: []string{"compose-app-prod"}, Note: "compose-app production stack"}); err != nil {
		t.Fatalf("AddReservation: %v", err)
	}
	got, err := st.ReadBands()
	if err != nil {
		t.Fatalf("ReadBands: %v", err)
	}
	if len(got.Bands) != 1 || len(got.Reservations) != 1 {
		t.Fatalf("bands round trip = %+v", got)
	}
	if got.Bands[0].App != "compose-app" || got.Bands[0].Bases["api"] != 4200 || got.Bands[0].Spans["proxy"] != 64 {
		t.Errorf("band = %+v", got.Bands[0])
	}
	if got.Reservations[0].Ports[0] != 5319 || got.Reservations[0].Names[0] != "compose-app-prod" ||
		got.Reservations[0].Note != "compose-app production stack" {
		t.Errorf("reservation = %+v", got.Reservations[0])
	}
}

// TestBandReRegistrationReplaces: one app, one band — a re-registration
// with fewer resources drops the stale bases rather than leaving them
// behind.
func TestBandReRegistrationReplaces(t *testing.T) {
	st := openStore(t, tempRoot(t))
	full := Band{App: "compose-app", Bases: map[string]int{"api": 4200, "proxy": 4300}, Spans: map[string]int{"api": 64, "proxy": 64}}
	if err := st.UpsertBand(full); err != nil {
		t.Fatal(err)
	}
	reduced := Band{App: "compose-app", Bases: map[string]int{"api": 4400}, Spans: map[string]int{"api": 64}}
	if err := st.UpsertBand(reduced); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadBands()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Bands) != 1 {
		t.Fatalf("bands = %+v, want one band", got.Bands)
	}
	if len(got.Bands[0].Bases) != 1 || got.Bands[0].Bases["api"] != 4400 {
		t.Errorf("re-registration left stale bases: %+v", got.Bands[0])
	}
}

// TestClientsRoundTrip: the client table survives upserts and deletes.
func TestClientsRoundTrip(t *testing.T) {
	st := openStore(t, tempRoot(t))
	now := "2026-08-13T09:00:00.123456789Z"
	host := ClientEntry{Identity: "4242", Kind: "host", LastSeen: now}
	eph := ClientEntry{Identity: "s1a2b3", Kind: "ephemeral", LastSeen: now, Ephemeral: true}
	if err := st.UpsertClient(host); err != nil {
		t.Fatal(err)
	}
	if err := st.UpsertClient(eph); err != nil {
		t.Fatal(err)
	}
	// Reconnection updates the row in place, one row per (identity, kind).
	if err := st.UpsertClient(ClientEntry{Identity: "4242", Kind: "host", LastSeen: "2026-08-13T10:00:00Z"}); err != nil {
		t.Fatal(err)
	}
	got, err := st.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("clients = %+v, want 2 rows", got.Clients)
	}
	for _, c := range got.Clients {
		if c.Identity == "4242" && c.LastSeen != "2026-08-13T10:00:00Z" {
			t.Errorf("host row was not updated in place: %+v", c)
		}
	}
	if err := st.DeleteClient("s1a2b3", "ephemeral"); err != nil {
		t.Fatal(err)
	}
	got, err = st.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(got.Clients) != 1 || got.Clients[0].Identity != "4242" {
		t.Errorf("clients after DeleteClient = %+v", got.Clients)
	}
}

// TestSpecsCacheRoundTrip: the per-app spec cache survives an upsert and
// a delete, and a fresh store is an empty cache.
func TestSpecsCacheRoundTrip(t *testing.T) {
	st := openStore(t, tempRoot(t))
	sp := spec.Spec{Version: 1, App: "compose-app"}
	if err := st.UpsertSpec("compose-app", sp); err != nil {
		t.Fatal(err)
	}
	f, err := st.ReadSpecs()
	if err != nil {
		t.Fatal(err)
	}
	got, ok := f.Specs["compose-app"]
	if !ok || got.App != "compose-app" {
		t.Errorf("cache = %+v", f.Specs)
	}
	if err := st.DeleteSpec("compose-app"); err != nil {
		t.Fatal(err)
	}
	f, err = st.ReadSpecs()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Specs) != 0 {
		t.Errorf("cache after DeleteSpec = %+v, want empty", f.Specs)
	}
}

// TestReadRegistryListReadsNewerSchema: a database written by a newer
// schema is read well enough to list — the lenient read decodes it and
// reports the database's own version — while the strict read refuses.
func TestReadRegistryListReadsNewerSchema(t *testing.T) {
	st := openStore(t, tempRoot(t))
	if err := st.UpsertEntry(testEntry()); err != nil {
		t.Fatal(err)
	}
	if _, err := st.db.Exec("UPDATE meta SET value = '2' WHERE key = 'schema_version'"); err != nil {
		t.Fatal(err)
	}

	// Lists: the lenient read decodes the entry and carries the database's
	// version, so the caller can see it is newer.
	f, err := st.ReadRegistryList()
	if err != nil {
		t.Fatalf("ReadRegistryList: %v", err)
	}
	if f.SchemaVersion != 2 {
		t.Errorf("listed registry schema_version = %d, want 2 (the database's own)", f.SchemaVersion)
	}
	if len(f.Entries) != 1 || f.Entries[0].Slug != "brisk-otter" {
		t.Errorf("listed entries = %+v, want the stored entry", f.Entries)
	}

	// Writes: the strict read refuses the newer database naming the upgrade.
	_, err = st.ReadRegistry()
	var ve *VersionError
	if !errors.As(err, &ve) {
		t.Fatalf("ReadRegistry = %v, want a VersionError", err)
	}
	if ve.File != 2 || ve.Current != SchemaVersion {
		t.Errorf("VersionError = %+v, want file 2 current %d", ve, SchemaVersion)
	}
	if !strings.Contains(ve.Error(), "upgrade wtd") {
		t.Errorf("VersionError does not name the upgrade: %v", ve.Error())
	}
}
