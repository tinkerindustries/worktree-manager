package spec

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestFindSpecPathWalkUp pins the resolution rule: the spec is found by
// walking up from cwd to the worktree root and stopping there — never above
// it. The generated reader in a later phase depends on exactly this rule.
func TestFindSpecPathWalkUp(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "a", "b", "c"), 0o755); err != nil {
		t.Fatal(err)
	}
	specFile := filepath.Join(root, "a", "b", SpecFilename)
	if err := os.WriteFile(specFile, []byte("version: 1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	// Found from a deep directory under the worktree root.
	got, err := FindSpecPath(filepath.Join(root, "a", "b", "c"))
	if err != nil {
		t.Fatalf("FindSpecPath: %v", err)
	}
	if got != specFile {
		t.Errorf("FindSpecPath = %q, want %q", got, specFile)
	}

	// Found from the worktree root itself.
	got, err = FindSpecPath(filepath.Join(root, "a", "b"))
	if err != nil {
		t.Fatalf("FindSpecPath(root): %v", err)
	}
	if got != specFile {
		t.Errorf("FindSpecPath = %q, want %q", got, specFile)
	}
}

// TestFindSpecPathStopsAtFirst pins "never above it": a wt.yaml in a parent
// directory must not shadow or be reached past the worktree's own.
func TestFindSpecPathStopsAtFirst(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "repo", "src"), 0o755); err != nil {
		t.Fatal(err)
	}
	inner := filepath.Join(root, "repo", SpecFilename)
	outer := filepath.Join(root, SpecFilename)
	for _, p := range []string{inner, outer} {
		if err := os.WriteFile(p, []byte("version: 1\n"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	got, err := FindSpecPath(filepath.Join(root, "repo", "src"))
	if err != nil {
		t.Fatalf("FindSpecPath: %v", err)
	}
	if got != inner {
		t.Errorf("FindSpecPath = %q, want the inner %q — the walk must stop at the first wt.yaml and never go above it", got, inner)
	}
}

// TestFindSpecPathNotAdopted: no wt.yaml anywhere up the tree is reported as
// not adopted, not as an obscure failure.
func TestFindSpecPathNotAdopted(t *testing.T) {
	dir := t.TempDir()
	_, err := FindSpecPath(dir)
	var nae *NotAdoptedError
	if !errors.As(err, &nae) {
		t.Fatalf("FindSpecPath error = %v, want *NotAdoptedError", err)
	}
}

// TestFixtureWalkUpFromSubtree: every fixture's spec is found from inside
// its own tree, the way the generated reader will find it.
func TestFixtureWalkUpFromSubtree(t *testing.T) {
	dirs := map[string]string{
		"compose-app": "cmd/server",
		"plain-app":   "bin",
		"vm-app":      "scripts",
	}
	for fixture, sub := range dirs {
		t.Run(fixture, func(t *testing.T) {
			start := filepath.Join("..", "..", "testdata", "fixtures", fixture, sub)
			got, err := FindSpecPath(start)
			if err != nil {
				t.Fatalf("FindSpecPath: %v", err)
			}
			want := fixturePath(t, fixture)
			if got != want {
				t.Errorf("FindSpecPath = %q, want %q", got, want)
			}
		})
	}
}
