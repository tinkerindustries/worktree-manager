package platform

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chdir changes the working directory for the duration of the test.
func chdir(t *testing.T, dir string) {
	t.Helper()
	wd, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := os.Chdir(wd); err != nil {
			t.Fatal(err)
		}
	})
}

func TestRealPathSymlink(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := RealPath(filepath.Join(link, "sub"))
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	want := filepath.Join(real, "sub")
	if got != want {
		t.Errorf("RealPath(%s) = %s, want %s", link, got, want)
	}
}

// TestRealPathNonexistentTail pins the shape the guard depends on: a write to
// a file that does not exist yet still resolves through its existing
// ancestors, so a first write inside the tree is contained.
func TestRealPathNonexistentTail(t *testing.T) {
	dir := t.TempDir()
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Fatal(err)
	}
	got, err := RealPath(filepath.Join(link, "new", "file.txt"))
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	want := filepath.Join(real, "new", "file.txt")
	if got != want {
		t.Errorf("RealPath = %s, want %s", got, want)
	}
}

func TestRealPathRelative(t *testing.T) {
	dir := t.TempDir()
	sub := filepath.Join(dir, "sub")
	if err := os.Mkdir(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	chdir(t, dir)
	got, err := RealPath(filepath.Join("sub", "file"))
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	if got != filepath.Join(sub, "file") {
		t.Errorf("RealPath = %s, want %s", got, filepath.Join(sub, "file"))
	}
}

// TestRealPathNonexistentAncestor documents the boundary of the
// deepest-existing-ancestor rule: a path under a directory that does not
// exist still resolves, because the ancestor walk finds the temp root and
// re-joins the tail. Errors are reserved for paths with no resolvable
// ancestor at all, which is unreachable in practice; the walk itself is the
// contract.
func TestRealPathNonexistentAncestor(t *testing.T) {
	root := t.TempDir()
	dir := filepath.Join(root, "does-not-exist")
	got, err := RealPath(dir)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", dir, err)
	}
	if got != dir {
		t.Errorf("RealPath(%s) = %s, want the absolute path itself", dir, got)
	}
}

// TestCaseSensitiveProbeMatchesReality validates the probe against the only
// ground truth available: whether two case-differing names actually collide
// on this mount.
func TestCaseSensitiveProbeMatchesReality(t *testing.T) {
	dir := t.TempDir()
	got, err := CaseSensitive(dir)
	if err != nil {
		t.Fatalf("CaseSensitive: %v", err)
	}
	probe := filepath.Join(dir, "groundtruth-a")
	if err := os.WriteFile(probe, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(probe)
	_, statErr := os.Stat(filepath.Join(dir, "GROUNDTRUTH-A"))
	reality := os.IsNotExist(statErr) // distinct file exists only when case-sensitive
	if got != reality {
		t.Errorf("CaseSensitive(%s) = %v, but the mount behaves case-sensitive=%v", dir, got, reality)
	}
}

func TestCaseSensitiveProbeNonexistentDir(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "gone")
	if _, err := CaseSensitive(dir); err == nil {
		t.Errorf("CaseSensitive(%s) succeeded, want an error", dir)
	}
}

// TestRealPathWindowsCanonicalUnused guards the darwin branch's shape: the
// firmlink prefix never appears on this platform, and canonical must leave
// paths untouched off macOS.
func TestCanonicalOffDarwin(t *testing.T) {
	if strings.HasPrefix(t.TempDir(), "/System/Volumes/Data") {
		t.Skip("running on a macOS firmlink path; nothing to assert here")
	}
	if got := canonical("/System/Volumes/Data/Users/geoff"); got != "/System/Volumes/Data/Users/geoff" {
		t.Errorf("canonical rewrote a path on a non-darwin build: %s", got)
	}
}
