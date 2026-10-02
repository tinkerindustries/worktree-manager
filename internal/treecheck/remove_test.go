package treecheck

// remove_test.go proves WorktreeRemove against a real repository rather
// than a canned runner: the thing that broke was where git was run from,
// which a fake Runner cannot show.

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// gitT runs one git command in dir and fails the test on error.
func gitT(t *testing.T, dir string, args ...string) {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git %v in %s: %v\n%s", args, dir, err, out)
	}
}

// removeFixture builds a repository with one linked worktree and returns
// the main checkout and the worktree.
func removeFixture(t *testing.T) (main, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	base := t.TempDir()
	main = filepath.Join(base, "main")
	if err := os.MkdirAll(main, 0o755); err != nil {
		t.Fatal(err)
	}
	gitT(t, main, "init", "-q", "-b", "main", ".")
	gitT(t, main, "config", "user.email", "t@example.com")
	gitT(t, main, "config", "user.name", "T")
	if err := os.WriteFile(filepath.Join(main, "file.txt"), []byte("one\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	gitT(t, main, "add", ".")
	gitT(t, main, "commit", "-q", "-m", "initial")
	worktree = filepath.Join(base, "wt-1")
	gitT(t, main, "worktree", "add", "-q", worktree, "-b", "wt-1")
	return main, worktree
}

// TestWorktreeRemoveRemovesTheTree is the whole contract: the directory is
// gone and git no longer lists it.
//
// The failure this pins is Windows-specific and silent everywhere else:
// git was run with the tree as its working directory, and Windows will not
// remove a directory a process is standing in — the removal came back
// "failed to delete '<tree>': Permission denied" for a tree with nothing
// wrong with it.
func TestWorktreeRemoveRemovesTheTree(t *testing.T) {
	main, worktree := removeFixture(t)
	if leftover, err := WorktreeRemove(worktree, Git); err != nil || leftover != "" {
		t.Fatalf("WorktreeRemove(%s) = %q, %v", worktree, leftover, err)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("%s still exists after WorktreeRemove (stat err %v)", worktree, err)
	}
	out, err := Git(main, "worktree", "list", "--porcelain")
	if err != nil {
		t.Fatalf("git worktree list: %v", err)
	}
	if strings.Contains(strings.ReplaceAll(string(out), "\\", "/"),
		strings.ReplaceAll(worktree, "\\", "/")) {
		t.Errorf("git still lists the removed worktree:\n%s", out)
	}
}

// TestWorktreeRemoveIsNeverForced: a tree with an uncommitted change is
// git's to refuse, and the refusal is reported as itself. Nothing here
// passes --force, and this is what would notice if something did.
func TestWorktreeRemoveIsNeverForced(t *testing.T) {
	_, worktree := removeFixture(t)
	if err := os.WriteFile(filepath.Join(worktree, "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WorktreeRemove(worktree, Git)
	if err == nil {
		t.Fatal("WorktreeRemove removed a tree with an untracked file; it must never force")
	}
	if _, serr := os.Stat(worktree); serr != nil {
		t.Errorf("the refused tree was removed anyway: %v", serr)
	}
	if !strings.Contains(err.Error(), "contains modified or untracked files") {
		t.Errorf("the refusal does not carry git's own words: %v", err)
	}
}

// TestRemoveFromFallsBackToTheTree: with git unable to answer where the
// repository is, the removal still runs from the tree — the pre-existing
// behaviour, which is correct on every platform but Windows.
func TestRemoveFromFallsBackToTheTree(t *testing.T) {
	dir := filepath.Join("some", "tree")
	if got := removeFrom(dir, fakeRunner("", errors.New("not a git repository"))); got != dir {
		t.Errorf("removeFrom with git failing = %q, want the tree %q", got, dir)
	}
	if got := removeFrom(dir, fakeRunner("", nil)); got != dir {
		t.Errorf("removeFrom with an empty common dir = %q, want the tree %q", got, dir)
	}
}
