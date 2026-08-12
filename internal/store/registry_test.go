package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// TestRegistryRoundTrip: entries survive a write and a strict read, with the
// schema_version envelope on the file and on every entry, and JSON rather
// than YAML on disk (a slug of "no" would come back as a boolean through a
// YAML 1.1 parser — ARCHITECTURE.md §8.2).
func TestRegistryRoundTrip(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("ReadRegistry on an empty store: %v", err)
	}
	if len(f.Entries) != 0 || f.SchemaVersion != SchemaVersion {
		t.Errorf("new store registry = %+v, want empty at schema %d", f, SchemaVersion)
	}

	now := "2026-08-12T10:00:00.123456789Z"
	want := Entry{
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
	f.Entries = []Entry{want}
	if err := st.WriteRegistry(f); err != nil {
		t.Fatalf("WriteRegistry: %v", err)
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
	if e.Resources["api"].Value != 4201 || e.Resources["api"].Type != "port" {
		t.Errorf("resources round trip = %+v", e.Resources)
	}
	if e.Resources["compose"].Value != "compose-app-brisk-otter-1" {
		t.Errorf("compose resource round trip = %+v", e.Resources["compose"])
	}
	if e.Secrets["admin_password"] != "hunter2" {
		t.Errorf("secrets round trip = %+v", e.Secrets)
	}
	if got.SchemaVersion != SchemaVersion || e.SchemaVersion != SchemaVersion {
		t.Errorf("schema_version = file %d entry %d, want %d", got.SchemaVersion, e.SchemaVersion, SchemaVersion)
	}

	raw, err := os.ReadFile(filepath.Join(root, RegistryFileName))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	if !strings.Contains(text, `"schema_version": 1`) {
		t.Errorf("registry lacks its schema_version envelope:\n%s", text)
	}
	if !strings.Contains(text, "admin_password") {
		t.Errorf("registry lost the secrets field:\n%s", text)
	}
}

// TestReadRegistryListReadsNewerSchema: a registry written by a newer schema
// version is read well enough to list — the lenient read decodes it and
// reports the file's own version — while the strict read refuses.
func TestReadRegistryListReadsNewerSchema(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	newer := `{"schema_version":2,"entries":[{"app":"future-app","slug":"x","slot":1,"owner":"1","owner_kind":"host","state":"active","created_at":"2026-01-01T00:00:00Z","last_seen":"2026-01-01T00:00:00Z"}]}`
	if err := st.WriteFile(RegistryFileName, []byte(newer)); err != nil {
		t.Fatal(err)
	}

	// Lists: the lenient read decodes the entry and carries the file's
	// version, so the caller can see it is newer.
	f, err := st.ReadRegistryList()
	if err != nil {
		t.Fatalf("ReadRegistryList: %v", err)
	}
	if f.SchemaVersion != 2 {
		t.Errorf("listed registry schema_version = %d, want 2 (the file's own)", f.SchemaVersion)
	}
	if len(f.Entries) != 1 || f.Entries[0].App != "future-app" || f.Entries[0].Slug != "x" {
		t.Errorf("listed entries = %+v, want the future entry", f.Entries)
	}

	// Writes: the strict read refuses the newer file naming the upgrade.
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

// TestWriteRegistryRefusesNewerVersion: writing a registry whose own
// schema_version claims a newer schema is refused at the write path — the
// "does not write" half is structural, not a habit of the caller.
func TestWriteRegistryRefusesNewerVersion(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f := RegistryFile{Versioned: Versioned{SchemaVersion: 2}}
	err = st.WriteRegistry(f)
	if err == nil {
		t.Fatal("WriteRegistry of a schema-2 file succeeded, want a refusal")
	}
	var ve *VersionError
	if !errors.As(err, &ve) || ve.File != 2 {
		t.Errorf("WriteRegistry error = %v, want a VersionError for schema 2", err)
	}
}

// TestReadRegistryListReportsUnparseable: an unparseable registry is
// reported and never truncated and recreated — the file survives untouched
// and both read paths name it.
func TestReadRegistryListReportsUnparseable(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	garbage := []byte("{not json")
	if err := st.WriteFile(RegistryFileName, garbage); err != nil {
		t.Fatal(err)
	}
	if _, err := st.ReadRegistryList(); err == nil {
		t.Fatal("ReadRegistryList of an unparseable file succeeded")
	}
	raw, err := os.ReadFile(filepath.Join(root, RegistryFileName))
	if err != nil {
		t.Fatal(err)
	}
	if string(raw) != string(garbage) {
		t.Error("the unparseable registry was modified; it must be reported, never truncated and recreated")
	}
}

// TestBandsRoundTrip: app bands and host reservations survive a write and a
// read, with the schema_version envelope.
func TestBandsRoundTrip(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	f, err := st.ReadBands()
	if err != nil {
		t.Fatalf("ReadBands on an empty store: %v", err)
	}
	if len(f.Bands) != 0 || len(f.Reservations) != 0 {
		t.Errorf("new store bands = %+v, want empty", f)
	}
	f.Bands = []Band{{App: "compose-app", Bases: map[string]int{"api": 4200, "proxy": 4200}}}
	f.Reservations = []Reservation{{Ports: []int{5319, 5320}, Note: "compose-app production stack"}}
	if err := st.WriteBands(f); err != nil {
		t.Fatalf("WriteBands: %v", err)
	}
	got, err := st.ReadBands()
	if err != nil {
		t.Fatalf("ReadBands: %v", err)
	}
	if got.SchemaVersion != SchemaVersion || len(got.Bands) != 1 || len(got.Reservations) != 1 {
		t.Fatalf("bands round trip = %+v", got)
	}
	if got.Bands[0].App != "compose-app" || got.Bands[0].Bases["api"] != 4200 || got.Bands[0].Bases["proxy"] != 4200 {
		t.Errorf("band = %+v", got.Bands[0])
	}
	if got.Reservations[0].Ports[0] != 5319 || got.Reservations[0].Note != "compose-app production stack" {
		t.Errorf("reservation = %+v", got.Reservations[0])
	}
	raw, err := os.ReadFile(filepath.Join(root, BandsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Errorf("bands.json lacks its schema_version envelope:\n%s", raw)
	}
}
