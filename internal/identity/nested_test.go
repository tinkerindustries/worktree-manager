package identity

// nested_test.go proves the "worktree nested inside a foreign tree"
// refusal against real repositories (the fixture the phase-1 deferral
// wanted): a standalone clone inside a linked worktree, the negative
// cases, and the case the `.claude/worktrees/<slug>` convention depends
// on — a linked worktree of the same repository inside the main checkout,
// which is not nesting and is not refused.

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
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
	enclosing, err := NestedInside(clone, commonDir(t, clone))
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
		enclosing, err := NestedInside(dir, commonDir(t, dir))
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
	symlinkOrSkip(t, clone, link)
	enclosing, err := NestedInside(link, commonDir(t, clone))
	if err != nil {
		t.Fatalf("NestedInside through a symlink: %v", err)
	}
	if same, _ := platform.SamePath(enclosing, worktree); !same {
		t.Errorf("enclosing = %s, want the worktree %s (through the symlink)", enclosing, worktree)
	}
}

// TestNestedInsideAllowsTheSameRepositorysWorktree: the default
// `worktrees.path` puts a linked worktree at `<main>/.claude/worktrees/
// <slug>`, which git allows and every listing tells apart from the main
// checkout. It is not the hazard the refusal exists for, so it is not
// refused — without this, the convention the generated skill hands agents
// would be uninitialisable.
func TestNestedInsideAllowsTheSameRepositorysWorktree(t *testing.T) {
	_, _, main := buildNestedRepo(t)
	inside := filepath.Join(main, ".claude", "worktrees", "brisk-otter")
	git(t, main, "worktree", "add", "-b", "brisk-otter", inside, "main")

	enclosing, err := NestedInside(inside, commonDir(t, inside))
	if err != nil {
		t.Fatalf("NestedInside: %v", err)
	}
	if enclosing != "" {
		t.Errorf("NestedInside(%s) = %q, want \"\": a worktree of the same repository is not nesting", inside, enclosing)
	}
}

// TestNestedInsideWalksPastTheSameRepository: the walk does not stop at
// the same-repository ancestor it accepts. A repository whose own trees
// are inside it, itself sitting inside a foreign working tree, is still
// refused — and named against the foreign tree, not its own checkout.
func TestNestedInsideWalksPastTheSameRepository(t *testing.T) {
	outer := t.TempDir()
	git(t, "", "init", "-b", "main", outer)
	git(t, outer, "config", "user.email", "t@example.com")
	git(t, outer, "config", "user.name", "T")
	writeFile(t, filepath.Join(outer, "outer.txt"), "outer\n")
	git(t, outer, "add", ".")
	git(t, outer, "commit", "-m", "initial")

	main := filepath.Join(outer, "inner")
	git(t, "", "init", "-b", "main", main)
	git(t, main, "config", "user.email", "t@example.com")
	git(t, main, "config", "user.name", "T")
	writeFile(t, filepath.Join(main, "file.txt"), "one\n")
	git(t, main, "add", ".")
	git(t, main, "commit", "-m", "initial")
	inside := filepath.Join(main, ".claude", "worktrees", "brisk-otter")
	git(t, main, "worktree", "add", "-b", "brisk-otter", inside, "main")

	enclosing, err := NestedInside(inside, commonDir(t, inside))
	if err != nil {
		t.Fatalf("NestedInside: %v", err)
	}
	if same, _ := platform.SamePath(enclosing, outer); !same {
		t.Errorf("enclosing = %q, want the foreign tree %s", enclosing, outer)
	}
}

// commonDir is the tree's own git common directory, absolute — what the
// classification hands NestedInside.
func commonDir(t *testing.T, dir string) string {
	t.Helper()
	out, err := exec.Command("git", "-C", dir, "rev-parse", "--git-common-dir").Output()
	if err != nil {
		t.Fatalf("git rev-parse --git-common-dir in %s: %v", dir, err)
	}
	common := strings.TrimSpace(string(out))
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return common
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
