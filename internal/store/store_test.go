package store

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
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

// openStore opens a store and closes it when the test ends.
//
// An open SQLite handle holds wt.db and its -wal/-shm sidecars, and a
// handle left open is a leak on every platform. Windows is the one that
// says so: it refuses to unlink an open file, so t.TempDir()'s own
// RemoveAll fails and the test that leaked the store is the test that
// fails. Every store a test opens goes through here.
func openStore(t *testing.T, root string) *Store {
	t.Helper()
	st, err := Open(root)
	if err != nil {
		t.Fatalf("Open(%s): %v", root, err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("closing the store at %s: %v", root, err)
		}
	})
	return st
}

// homeEnv is the variable that names the user's home directory on this
// platform: os.UserHomeDir — which Root() calls — reads USERPROFILE on
// Windows and HOME everywhere else, so a test that sets HOME on Windows
// changes nothing.
func homeEnv() string {
	if runtime.GOOS == "windows" {
		return "USERPROFILE"
	}
	return "HOME"
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
	t.Setenv(homeEnv(), filepath.Join(tempRoot(t), "home"))
	got, err = Root()
	if err != nil {
		t.Fatalf("Root: %v", err)
	}
	if got != filepath.Join(os.Getenv(homeEnv()), ".wt") {
		t.Errorf("Root with the home directory set = %q, want <home>/.wt", got)
	}

	t.Setenv(homeEnv(), "")
	if _, err := Root(); err == nil {
		t.Error("Root with WT_HOME and $HOME both unset succeeded, want a refusal to invent a location")
	}
}

func TestOpenCreatesPrivateDir(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st := openStore(t, root)
	if st.Root() != root {
		t.Errorf("Root() = %q, want %q", st.Root(), root)
	}
	fi, err := os.Stat(root)
	if err != nil {
		t.Fatal(err)
	}
	// Mode bits are the permission model on unix; on Windows the ACL is
	// (asserted in internal/platform's TestEnsurePrivateDirWindowsACL).
	if runtime.GOOS != "windows" && fi.Mode().Perm() != 0o700 {
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

// TestOpenCreatesPrivateDatabase is the R0 file-modes criterion: Open
// creates wt.db, and the WAL and SHM sidecars the driver creates at the
// process umask are all 0600 — entry secrets live in the database, so the
// modes are verified at open, not assumed. The database is usable and the
// schema is in place.
func TestOpenCreatesPrivateDatabase(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st := openStore(t, root)
	if st.Root() != root {
		t.Errorf("Root() = %q, want %q", st.Root(), root)
	}
	// The three files exist and are 0600 on unix; on Windows the store
	// root's current-user ACL covers them (platform's own test).
	if runtime.GOOS != "windows" {
		for _, name := range []string{DBFileName, DBFileName + "-wal", DBFileName + "-shm"} {
			fi, err := os.Stat(filepath.Join(root, name))
			if err != nil {
				t.Fatalf("stat %s: %v", name, err)
			}
			if fi.Mode().Perm() != 0o600 {
				t.Errorf("%s mode = %v, want 0600", name, fi.Mode().Perm())
			}
		}
	}
	// A fresh store is empty at every whole-collection read.
	reg, err := st.ReadRegistry()
	if err != nil {
		t.Fatalf("ReadRegistry: %v", err)
	}
	if len(reg.Entries) != 0 || reg.SchemaVersion != SchemaVersion {
		t.Errorf("fresh registry = %+v, want empty at schema %d", reg, SchemaVersion)
	}
	bands, err := st.ReadBands()
	if err != nil {
		t.Fatalf("ReadBands: %v", err)
	}
	if len(bands.Bands) != 0 || len(bands.Reservations) != 0 {
		t.Errorf("fresh bands = %+v, want empty", bands)
	}
	clients, err := st.ReadClients()
	if err != nil {
		t.Fatalf("ReadClients: %v", err)
	}
	if len(clients.Clients) != 0 {
		t.Errorf("fresh clients = %+v, want empty", clients)
	}
	specs, err := st.ReadSpecs()
	if err != nil {
		t.Fatalf("ReadSpecs: %v", err)
	}
	if len(specs.Specs) != 0 {
		t.Errorf("fresh specs = %+v, want empty", specs)
	}
}

// TestOpenRefusesBrokenDatabase: a database that cannot be opened is
// refused with the reasoning an unparseable registry carried — the
// registry is the only source of repository locations, so
// rebuild-from-descriptors cannot be safe. A garbage file standing in for
// wt.db fails at open, naming the path.
func TestOpenRefusesBrokenDatabase(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	if err := os.MkdirAll(root, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, DBFileName), []byte("not a sqlite database"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Open(root)
	if err == nil {
		t.Fatal("Open over a garbage wt.db succeeded, want a refusal")
	}
	msg := err.Error()
	if !strings.Contains(msg, DBFileName) {
		t.Errorf("error does not name the database file: %v", msg)
	}
	if !strings.Contains(msg, "cannot be opened") {
		t.Errorf("error does not say the database cannot be opened: %v", msg)
	}
}

// TestSchemaVersionRefusal: a database whose meta.schema_version is newer
// than this build is refused at every strict read and every mutation,
// naming the upgrade — the "lists but does not write" rail
// (02-coordination.md §11) — while the lenient read still lists it.
func TestSchemaVersionRefusal(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	st := openStore(t, root)
	if _, err := st.db.Exec("UPDATE meta SET value = '2' WHERE key = 'schema_version'"); err != nil {
		t.Fatal(err)
	}
	_, err := st.ReadRegistry()
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
	// Every other strict read and mutation refuses the same way.
	if _, err := st.ReadBands(); !errors.As(err, &ve) {
		t.Errorf("ReadBands = %v, want a VersionError", err)
	}
	if _, err := st.ReadClients(); !errors.As(err, &ve) {
		t.Errorf("ReadClients = %v, want a VersionError", err)
	}
	if _, err := st.ReadSpecs(); !errors.As(err, &ve) {
		t.Errorf("ReadSpecs = %v, want a VersionError", err)
	}
	if _, _, err := st.GetEntry("app", "slug"); !errors.As(err, &ve) {
		t.Errorf("GetEntry = %v, want a VersionError", err)
	}
	if err := st.UpsertEntry(Entry{}); !errors.As(err, &ve) {
		t.Errorf("UpsertEntry = %v, want a VersionError", err)
	}
	if err := st.UpdateEntryState("a", "s", "active", ""); !errors.As(err, &ve) {
		t.Errorf("UpdateEntryState = %v, want a VersionError", err)
	}
	if err := st.WithTx(func(*Tx) error { return nil }); !errors.As(err, &ve) {
		t.Errorf("WithTx = %v, want a VersionError", err)
	}
	// The lenient read still lists: it carries the database's own version.
	f, err := st.ReadRegistryList()
	if err != nil {
		t.Fatalf("ReadRegistryList of a newer database: %v", err)
	}
	if f.SchemaVersion != 2 {
		t.Errorf("listed registry schema_version = %d, want 2 (the database's own)", f.SchemaVersion)
	}
}
