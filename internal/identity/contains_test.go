package identity

import (
	"os"
	"path/filepath"
	"testing"
)

// TestContainsSegments is exit criterion 5, at the pure level: the comparison
// is on path segments, not string prefixes — contains("/a/b", "/a/bc") is
// false — and on a case-insensitive comparison a differently-cased path
// inside the tree is contained. The case-insensitive branch is exercised
// here through containsResolved because the test environments of this phase
// (Linux containers) have no case-insensitive mount to probe; the probe
// itself is tested against the mount's real behaviour in
// internal/platform.
func TestContainsSegments(t *testing.T) {
	tests := []struct {
		name          string
		root, path    string
		caseSensitive bool
		want          bool
	}{
		// The two rails of 01-identity.md §3.1.
		{"sibling prefix is not contained", "/a/b", "/a/bc", true, false},
		{"sibling prefix, insensitive", "/a/b", "/a/bc", false, false},
		{"exact path is contained", "/a/b", "/a/b", true, true},
		{"direct child is contained", "/a/b", "/a/b/c", true, true},
		{"parent is not contained", "/a/b/c", "/a/b", true, false},
		{"root is not contained in child", "/a/b", "/a", true, false},
		{"unrelated is not contained", "/a/b", "/x/y", true, false},
		// The casing rail of 08-platform.md §4.2.
		{"differently-cased child, sensitive", "/a/b", "/a/B/c", true, false},
		{"differently-cased child, insensitive", "/a/b", "/a/B/c", false, true},
		{"differently-cased root, insensitive", "/a/b", "/A/b/c", false, true},
		{"differently-cased root, sensitive", "/a/b", "/A/b/c", true, false},
		{"single-segment root", "/", "/etc/passwd", true, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := containsResolved(tt.root, tt.path, tt.caseSensitive); got != tt.want {
				t.Errorf("containsResolved(%q, %q, sensitive=%v) = %v, want %v",
					tt.root, tt.path, tt.caseSensitive, got, tt.want)
			}
		})
	}
}

// TestContainsReal exercises the full Contains against real directories and
// symlinks: both rails — resolution and segments — plus the new-file case
// the guard depends on.
func TestContainsReal(t *testing.T) {
	dir := t.TempDir()
	tree := filepath.Join(dir, "tree")
	if err := os.MkdirAll(filepath.Join(tree, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(dir, "outside")
	if err := os.Mkdir(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	// A symlink inside the tree pointing out of it: a write through the
	// link lands outside, and resolution must say so.
	leak := filepath.Join(tree, "sub", "leak")
	if err := os.Symlink(outside, leak); err != nil {
		t.Fatal(err)
	}
	// A symlink outside the tree pointing in: a write through it lands
	// inside, and resolution must say so.
	door := filepath.Join(outside, "door")
	if err := os.Symlink(tree, door); err != nil {
		t.Fatal(err)
	}

	tests := []struct {
		name string
		path string
		want bool
	}{
		{"child", filepath.Join(tree, "sub"), true},
		{"new file inside", filepath.Join(tree, "new", "file.txt"), true},
		{"sibling prefix", filepath.Join(dir, "tree2"), false},
		{"outside", filepath.Join(outside, "x"), false},
		{"through a leaking symlink", filepath.Join(tree, "sub", "leak", "x"), false},
		{"through a door symlink", filepath.Join(outside, "door", "sub"), true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := Contains(tree, tt.path); got != tt.want {
				t.Errorf("Contains(%q, %q) = %v, want %v", tree, tt.path, got, tt.want)
			}
		})
	}
}

// TestContainsMissingRoot documents the degenerate call: a root that does
// not exist resolves through its deepest existing ancestor, so the answer is
// the segment-wise one — a path under the missing root is contained, a path
// outside it is not. The guard never reaches Contains with a missing root:
// classification of a deleted worktree directory fails first.
func TestContainsMissingRoot(t *testing.T) {
	dir := t.TempDir()
	gone := filepath.Join(dir, "gone")
	if !Contains(gone, filepath.Join(gone, "x")) {
		t.Error("Contains(missing root, child) reported false")
	}
	if Contains(gone, filepath.Join(dir, "elsewhere")) {
		t.Error("Contains(missing root, unrelated path) reported true")
	}
}
