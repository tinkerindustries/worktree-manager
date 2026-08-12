package store

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// tempRoot is t.TempDir() with symlinks resolved, so a path expectation
// built on it holds on macOS too (the temp root sits under /var there,
// which is a symlink to /private/var).
func tempRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	return dir
}

func TestRootResolution(t *testing.T) {
	t.Setenv("WT_HOME", "/tmp/wt-home-test")
	got, err := Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != "/tmp/wt-home-test" {
		t.Errorf("Root with WT_HOME = %q", got)
	}

	t.Setenv("WT_HOME", "")
	t.Setenv("HOME", filepath.Join(tempRoot(t), "home"))
	got, err = Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != filepath.Join(os.Getenv("HOME"), ".wt") {
		t.Errorf("Root with $HOME = %q, want $HOME/.wt", got)
	}

	t.Setenv("HOME", "")
	if _, err := Root(); err == nil {
		t.Error("Root with WT_HOME and $HOME both unset succeeded, want a refusal to invent a location")
	}
}

func TestOpenCreatesPrivateDir(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if st.Root() != root {
		t.Errorf("Root() = %q, want %q", st.Root(), root)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("store dir mode = %v, want 0700", fi.Mode().Perm())
	}
}

// TestOpenUnwritableRootNamesThePath: an unwritable store root produces a
// clear error naming the path at open time — not a rename failure at the
// end of a long operation. A file where a directory must be stands in for
// the ownership problem (chmod cannot stop root, which is what tests run
// as).
func TestOpenUnwritableRootNamesThePath(t *testing.T) {
	blocker := filepath.Join(tempRoot(t), "blocker")
	if err := os.WriteFile(blocker, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	bad := filepath.Join(blocker, "wt")
	_, err := Open(bad)
	if err == nil {
		t.Fatalf("Open(%s) succeeded over a file", bad)
	}
	msg := err.Error()
	if !strings.Contains(msg, bad) {
		t.Errorf("error does not name the path %q: %v", bad, msg)
	}
	if !strings.Contains(msg, "writable") && !strings.Contains(msg, "created") {
		t.Errorf("error does not explain the ownership problem: %v", msg)
	}
}

func TestAtomicWriteRoundTrip(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(root, "clients.json")
	if err := AtomicWrite(path, []byte("{\"schema_version\":1}\n"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "{\"schema_version\":1}\n" {
		t.Errorf("read back %q", data)
	}
	fi, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode().Perm() != 0o600 {
		t.Errorf("store file mode = %v, want 0600", fi.Mode().Perm())
	}
	// Overwrite: the rename path replaces the existing file atomically.
	if err := AtomicWrite(path, []byte("{\"schema_version\":1,\"clients\":[]}\n"), 0o600); err != nil {
		t.Fatalf("AtomicWrite overwrite: %v", err)
	}
	data, err = os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "\"clients\":[]") {
		t.Errorf("overwrite did not take: %q", data)
	}
	// No temp files survive.
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Errorf("directory holds %d entries after the writes, want exactly clients.json", len(entries))
	}
}

func TestSaveLoadClientsJSON(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	// A missing file is an empty table.
	f, err := st.ReadClients()
	if err != nil {
		t.Fatalf("ReadClients on an empty store: %v", err)
	}
	if len(f.Clients) != 0 {
		t.Errorf("new store has %d clients, want 0", len(f.Clients))
	}
	if f.SchemaVersion != SchemaVersion {
		t.Errorf("new store schema_version = %d, want %d", f.SchemaVersion, SchemaVersion)
	}

	now := time.Now().UTC().Format(time.RFC3339)
	f.Clients = []ClientEntry{
		{Identity: "1000", Kind: "host", LastSeen: now, Ephemeral: false},
		{Identity: "s1a2b3", Kind: "ephemeral", LastSeen: now, Ephemeral: true},
	}
	if err := st.WriteClients(f); err != nil {
		t.Fatalf("WriteClients: %v", err)
	}

	got, err := st.ReadClients()
	if err != nil {
		t.Fatalf("ReadClients: %v", err)
	}
	if len(got.Clients) != 2 {
		t.Fatalf("read back %d clients, want 2", len(got.Clients))
	}
	if got.Clients[0] != f.Clients[0] || got.Clients[1] != f.Clients[1] {
		t.Errorf("round trip changed the table: %+v", got.Clients)
	}
	if got.SchemaVersion != SchemaVersion {
		t.Errorf("round trip schema_version = %d, want %d", got.SchemaVersion, SchemaVersion)
	}

	// The file on disk really carries schema_version and is JSON, never YAML
	// — a slug of "no" would come back as a boolean through a YAML 1.1
	// parser, which is why the store is JSON (ARCHITECTURE.md §8.2).
	raw, err := os.ReadFile(filepath.Join(root, ClientsFileName))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(raw), `"schema_version": 1`) {
		t.Errorf("clients.json lacks its schema_version field:\n%s", raw)
	}
}

// TestSchemaVersionRefusal: a store file written by a newer schema is
// refused, naming the upgrade — the "lists but does not write" rail
// (02-coordination.md §11).
func TestSchemaVersionRefusal(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st, err := Open(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := st.WriteFile(ClientsFileName, []byte("{\"schema_version\":2,\"clients\":[]}\n")); err != nil {
		t.Fatal(err)
	}
	_, err = st.ReadClients()
	var ve *VersionError
	if !errors.As(err, &ve) {
		t.Fatalf("ReadClients = %v, want a VersionError", err)
	}
	if ve.File != 2 || ve.Current != SchemaVersion {
		t.Errorf("VersionError = %+v, want file 2 current %d", ve, SchemaVersion)
	}
	if !strings.Contains(ve.Error(), "upgrade wtd") {
		t.Errorf("VersionError does not name the upgrade: %v", ve.Error())
	}
}
