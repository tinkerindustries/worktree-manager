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

// submoduleFixture builds a repository with one submodule and a linked
// worktree whose submodule is initialised, and returns the main checkout
// and the worktree. git keeps the worktree's submodule clone under the
// worktree's own git dir, which is what makes it refuse an unforced
// removal.
func submoduleFixture(t *testing.T) (main, worktree string) {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git is not on PATH")
	}
	base := t.TempDir()
	sub := filepath.Join(base, "sub")
	main = filepath.Join(base, "main")
	for _, dir := range []string{sub, main} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		gitT(t, dir, "init", "-q", "-b", "main", ".")
		gitT(t, dir, "config", "user.email", "t@example.com")
		gitT(t, dir, "config", "user.name", "T")
	}
	gitT(t, sub, "commit", "-q", "--allow-empty", "-m", "sub initial")
	gitT(t, main, "-c", "protocol.file.allow=always", "submodule", "add", "-q", sub, "sub")
	gitT(t, main, "commit", "-q", "-m", "add submodule")
	worktree = filepath.Join(base, "wt-1")
	gitT(t, main, "worktree", "add", "-q", worktree, "-b", "wt-1")
	gitT(t, worktree, "-c", "protocol.file.allow=always", "submodule", "update", "-q", "--init")
	return main, worktree
}

// TestWorktreeRemoveRemovesATreeWithSubmodules: git refuses every tree
// with an initialised submodule unless forced, so a clean one is removed
// through the gated --force.
func TestWorktreeRemoveRemovesATreeWithSubmodules(t *testing.T) {
	main, worktree := submoduleFixture(t)
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

// TestWorktreeRemoveRefusesADirtySubmodule: an untracked file inside the
// submodule is what git's own check would have refused on, so the forced
// path refuses it too.
func TestWorktreeRemoveRefusesADirtySubmodule(t *testing.T) {
	_, worktree := submoduleFixture(t)
	if err := os.WriteFile(filepath.Join(worktree, "sub", "dirty.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := WorktreeRemove(worktree, Git)
	if err == nil {
		t.Fatal("WorktreeRemove removed a tree whose submodule has an untracked file")
	}
	if _, serr := os.Stat(filepath.Join(worktree, "sub", "dirty.txt")); serr != nil {
		t.Errorf("the refused tree was removed anyway: %v", serr)
	}
	if !strings.Contains(err.Error(), "modified or untracked files") {
		t.Errorf("the refusal does not say the tree is dirty: %v", err)
	}
}

// TestWorktreeRemoveRefusesUnpushedSubmoduleCommits: a submodule commit
// the superproject has recorded leaves status clean, but its only copy is
// the clone the removal deletes.
func TestWorktreeRemoveRefusesUnpushedSubmoduleCommits(t *testing.T) {
	_, worktree := submoduleFixture(t)
	sub := filepath.Join(worktree, "sub")
	gitT(t, sub, "-c", "user.email=t@example.com", "-c", "user.name=T", "commit", "-q", "--allow-empty", "-m", "local only")
	gitT(t, worktree, "add", "sub")
	gitT(t, worktree, "commit", "-q", "-m", "bump submodule")
	_, err := WorktreeRemove(worktree, Git)
	if err == nil {
		t.Fatal("WorktreeRemove removed a tree whose submodule holds a commit on no remote")
	}
	if _, serr := os.Stat(sub); serr != nil {
		t.Errorf("the refused tree was removed anyway: %v", serr)
	}
	if !strings.Contains(err.Error(), "submodule sub has 1 commit(s) on no remote") {
		t.Errorf("the refusal does not name the submodule's unpushed commit: %v", err)
	}
}
