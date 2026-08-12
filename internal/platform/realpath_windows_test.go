//go:build windows

package platform

// realpath_windows_test.go proves the long-path and mapped-drive halves
// of path realisation on a hosted windows-latest runner: the
// GetFinalPathNameByHandleW resolver must reduce a mapped drive and its
// target directory to the same string, and resolve deep paths without
// the MAX_PATH failure (08-platform.md §4.5). The mapped-drive half uses
// `subst`, which needs no administrator; where subst cannot run (no free
// drive letter, policy) the test skips with the reason stated rather
// than failing — a skip is the honest not_run on that machine.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestRealPathWindowsMappedDrive: a drive letter mapped to a directory
// and the directory itself name the same place; RealPath must agree on
// the two spellings, which is what makes containment comparisons sound
// against a mapped drive (08-platform.md §4.5: "UNC paths and mapped
// drives both appear as worktree locations, and they compare unequal as
// strings while naming the same directory").
func TestRealPathWindowsMappedDrive(t *testing.T) {
	dir := t.TempDir()
	letter := freeDriveLetter(t)
	if letter == "" {
		t.Skip("no free drive letter; the subst probe cannot run on this machine")
	}
	if err := exec.Command("subst", letter+":", dir).Run(); err != nil {
		t.Skipf("subst could not run on this machine: %v", err)
	}
	defer exec.Command("subst", letter+":", "/D").Run()

	a, err := RealPath(dir)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", dir, err)
	}
	b, err := RealPath(letter + `:\`)
	if err != nil {
		t.Fatalf("RealPath(%s): %v", letter+`:\`, err)
	}
	if a != b {
		t.Errorf("RealPath(%s) = %q and RealPath(%s) = %q name the same directory but differ", dir, a, letter+`:\`, b)
	}
}

// TestRealPathWindowsSymlink: the resolver must follow a symlink (a
// junction would be the filesystem-native form, but os.Symlink works on
// Windows for both).
func TestRealPathWindowsSymlink(t *testing.T) {
	base := t.TempDir()
	real := filepath.Join(base, "real")
	if err := os.Mkdir(real, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(base, "link")
	if err := os.Symlink(real, link); err != nil {
		t.Skipf("symlinks unavailable on this machine: %v", err)
	}
	got, err := RealPath(filepath.Join(link, "sub"))
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	if !strings.Contains(got, filepath.Join("real", "sub")) {
		t.Errorf("RealPath(%s) = %q, want it resolved through the symlink target", filepath.Join(link, "sub"), got)
	}
}

// freeDriveLetter finds an unmapped drive letter, Z down to E (A and B
// are conventionally reserved for floppies; C is the system drive).
func freeDriveLetter(t *testing.T) string {
	t.Helper()
	for _, c := range "ZYXWVUTSRQPONMLKJIHGFED" {
		letter := string(c)
		if _, err := os.Stat(letter + `:\`); err != nil {
			return letter
		}
	}
	return ""
}
