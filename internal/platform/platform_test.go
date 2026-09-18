package platform

import (
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

// tempDir is t.TempDir() with symlinks already resolved. Every expectation in
// this file is a path RealPath produced, and on macOS the temp root sits under
// /var, which is a symlink to /private/var — so a comparison against the raw
// t.TempDir() fails there while passing on Linux.
func tempDir(t *testing.T) string {
	t.Helper()
	dir, err := RealPath(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	return dir
}

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
	dir := tempDir(t)
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	symlinkOrSkip(t, real, link)
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
	dir := tempDir(t)
	real := filepath.Join(dir, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	symlinkOrSkip(t, real, link)
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
	dir := tempDir(t)
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
	root := tempDir(t)
	// The expected value is the resolved root plus the tail, not the raw
	// path: on macOS the temp root is under /var, which is a symlink to
	// /private/var, so the resolved form differs from the path as given.
	resolvedRoot, err := RealPath(root)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", root, err)
	}
	dir := filepath.Join(root, "does-not-exist")
	want := filepath.Join(resolvedRoot, "does-not-exist")
	got, err := RealPath(dir)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", dir, err)
	}
	if got != want {
		t.Errorf("RealPath(%s) = %s, want %s", dir, got, want)
	}
}

// TestCaseSensitiveProbeMatchesReality validates the probe against the only
// ground truth available: whether two case-differing names actually collide
// on this mount.
func TestCaseSensitiveProbeMatchesReality(t *testing.T) {
	dir := tempDir(t)
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
	dir := filepath.Join(tempDir(t), "gone")
	if _, err := CaseSensitive(dir); err == nil {
		t.Errorf("CaseSensitive(%s) succeeded, want an error", dir)
	}
}

// TestCanonical pins both halves of the firmlink branch. On macOS
// /System/Volumes/Data is a firmlink rather than a symlink, so EvalSymlinks
// leaves it in place and canonical strips it; on every other platform the
// prefix is an ordinary path component and must survive untouched.
func TestCanonical(t *testing.T) {
	const firmlinked = "/System/Volumes/Data/Users/alex"
	got := canonical(firmlinked)
	want := firmlinked
	if runtime.GOOS == "darwin" {
		want = "/Users/alex"
	}
	if got != want {
		t.Errorf("canonical(%s) = %s, want %s", firmlinked, got, want)
	}
	if runtime.GOOS == "darwin" {
		if got := canonical("/System/Volumes/Data"); got != "/" {
			t.Errorf("canonical of the firmlink root = %s, want /", got)
		}
	}
}

// symlinkOrSkip creates a symlink, skipping the test when the platform
// will not let it.
//
// Creating a symlink on Windows needs SeCreateSymbolicLinkPrivilege, which
// an ordinary account holds only with Developer Mode on; without it
// os.Symlink fails with "A required privilege is not held by the client".
// A test about symlink semantics cannot run there, and a skip naming the
// reason is the honest answer — the same call the mapped-drive probe in
// realpath_windows_test.go makes. Every other platform, and Windows CI,
// creates the link and runs the test.
func symlinkOrSkip(t *testing.T, oldname, newname string) {
	t.Helper()
	if err := os.Symlink(oldname, newname); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("cannot create a symlink on this machine (%v); creating one needs SeCreateSymbolicLinkPrivilege, which Developer Mode grants", err)
		}
		t.Fatalf("symlink %s -> %s: %v", newname, oldname, err)
	}
}
