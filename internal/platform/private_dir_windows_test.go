//go:build windows

package platform

// private_dir_windows_test.go proves on a hosted windows-latest runner
// the phase-8 exit criterion: a state directory whose ACL cannot be set
// refuses to hold credentials rather than writing them world-readable.
// The refusal is driven through the windowsSetACL seam, because a real
// filesystem that refuses ACLs (FAT) is not what a runner offers; the
// real path — ACL set and read back — is exercised against the temp dir.

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestEnsurePrivateDirWindowsACL: EnsurePrivateDir on Windows succeeds
// and the read-back finds the private ACL — the ACL is the permission
// model, replacing mode bits (08-platform.md §4.6).
func TestEnsurePrivateDirWindowsACL(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "wt-home")
	if err := EnsurePrivateDir(dir); err != nil {
		t.Fatalf("EnsurePrivateDir: %v", err)
	}
	if err := verifyPrivateACL(dir); err != nil {
		t.Errorf("the private ACL did not land: %v", err)
	}
	// The store must be able to write through it — the probe is part of
	// EnsurePrivateDir, and a real store open should work.
	if err := probePrivateDir(dir); err != nil {
		t.Errorf("probePrivateDir: %v", err)
	}
}

// TestEnsurePrivateDirRefusesWhenACLUnsettable is the refusal rail: where
// the ACL cannot be set, EnsurePrivateDir refuses with the path named —
// the credentials are never written, because the store never opens.
func TestEnsurePrivateDirRefusesWhenACLUnsettable(t *testing.T) {
	orig := windowsSetACL
	windowsSetACL = func(path string) error {
		return errors.New("the filesystem does not store ACLs")
	}
	defer func() { windowsSetACL = orig }()

	dir := filepath.Join(t.TempDir(), "wt-home")
	err := EnsurePrivateDir(dir)
	if err == nil {
		t.Fatal("EnsurePrivateDir succeeded with an unsettable ACL")
	}
	if !strings.Contains(err.Error(), dir) {
		t.Errorf("the refusal does not name the path %q: %v", dir, err)
	}
	if !strings.Contains(err.Error(), "world-readable") {
		t.Errorf("the refusal does not say what is being refused: %v", err)
	}
	// The directory was created but holds nothing — no credentials.
	entries, rerr := os.ReadDir(dir)
	if rerr != nil {
		t.Fatalf("reading the created root: %v", rerr)
	}
	if len(entries) != 0 {
		t.Errorf("the refused store root already holds %d entries", len(entries))
	}
}

// TestAtomicWriteWindows exercises the store's one write path on Windows:
// the temp-file-rename sequence with the directory-fsync step skipped
// (stated bounded coverage in atomic_windows.go).
func TestAtomicWriteWindows(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "registry.json")
	if err := AtomicWrite(path, []byte(`{"schema_version":1}`+"\n"), 0o600); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != `{"schema_version":1}`+"\n" {
		t.Errorf("round trip = %q", data)
	}
}
