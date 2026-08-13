package identity

// nested_test.go proves the "worktree nested inside another worktree"
// refusal against real repositories (the fixture the phase-1 deferral
// wanted): a standalone clone inside a linked worktree, and the negative
// cases.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// buildNestedRepo creates a repository with one linked worktree, then a
// standalone clone of the same repo inside that worktree. It returns the
// worktree root, the clone root and the enclosing main checkout.
func buildNestedRepo(t *testing.T) (worktree, clone, main string) {
	t.Helper()
	base := t.TempDir()
	main = filepath.Join(base, "main")
	git(t, "", "init", "-b", "main", main)
	git(t, main, "config", "user.email", "t@example.com")
	git(t, main, "config", "user.name", "T")
	writeFile(t, filepath.Join(main, "file.txt"), "one\n")
	git(t, main, "add", ".")
	git(t, main, "commit", "-m", "initial")
	worktree = filepath.Join(base, "wt-1")
	git(t, main, "worktree", "add", "-b", "wt-1", worktree, "main")
	// A clone inside the worktree: the nested tree.
	clone = filepath.Join(worktree, "nested-clone")
	git(t, main, "clone", main, clone)
	return worktree, clone, main
}

// TestNestedInsideFindsACloneInsideAWorktree: a standalone clone nested
// inside a linked worktree is detected, naming the enclosing tree.
func TestNestedInsideFindsACloneInsideAWorktree(t *testing.T) {
	worktree, clone, main := buildNestedRepo(t)
	enclosing, err := NestedInside(clone)
	if err != nil {
		t.Fatalf("NestedInside: %v", err)
	}
	if enclosing == "" {
		t.Fatalf("NestedInside(%s) = \"\", want the enclosing worktree", clone)
	}
	// The enclosing tree is the worktree, not the main checkout — the walk
	// stops at the first enclosing git root.
	if same, _ := platform.SamePath(enclosing, worktree); !same {
		t.Errorf("enclosing = %s, want the worktree %s", enclosing, worktree)
	}
	if same, _ := platform.SamePath(enclosing, main); same {
		t.Errorf("enclosing = the main checkout %s; the direct enclosure is the worktree", enclosing)
	}
}

// TestNestedInsideNegative: an ordinary worktree and the main checkout are
// not nested.
func TestNestedInsideNegative(t *testing.T) {
	worktree, _, main := buildNestedRepo(t)
	for _, dir := range []string{worktree, main} {
		enclosing, err := NestedInside(dir)
		if err != nil {
			t.Fatalf("NestedInside(%s): %v", dir, err)
		}
		if enclosing != "" {
			t.Errorf("NestedInside(%s) = %q, want \"\"", dir, enclosing)
		}
	}
}

// TestNestedInsideThroughSymlink: the nesting is detected through a
// symlinked path to the clone, like the classification fixtures' symlink
// variants.
func TestNestedInsideThroughSymlink(t *testing.T) {
	worktree, clone, _ := buildNestedRepo(t)
	link := filepath.Join(t.TempDir(), "via-link")
	if err := os.Symlink(clone, link); err != nil {
		t.Fatalf("symlinking the clone: %v", err)
	}
	enclosing, err := NestedInside(link)
	if err != nil {
		t.Fatalf("NestedInside through a symlink: %v", err)
	}
	if same, _ := platform.SamePath(enclosing, worktree); !same {
		t.Errorf("enclosing = %s, want the worktree %s (through the symlink)", enclosing, worktree)
	}
}

// git runs one git command.
func git(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %s: %s", args, strings.TrimSpace(string(out)))
	}
}

// writeFile writes a file.
func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("writing %s: %v", path, err)
	}
}
