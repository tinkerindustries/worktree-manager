package treecheck

// setaside_test.go proves the fast half of WorktreeRemove against real
// repositories and real directory links: the risks are git refusing after
// the ignored files have already left the tree, and a deletion following a
// link out of it, neither of which a canned Runner can show.

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// ignoredFixture is removeFixture with a committed .gitignore and a tree
// of ignored files in the worktree, shaped like an installed node_modules:
// wide, nested, and beside an ignored loose file.
func ignoredFixture(t *testing.T) (main, worktree string) {
	t.Helper()
	main, worktree = removeFixture(t)
	writeT(t, filepath.Join(worktree, ".gitignore"), "deps/\n*.log\n")
	gitT(t, worktree, "add", ".gitignore")
	gitT(t, worktree, "commit", "-q", "-m", "ignore deps")
	for i := range 20 {
		for j := range 10 {
			writeT(t, filepath.Join(worktree, "deps", fmt.Sprintf("pkg%d", i), "lib", fmt.Sprintf("f%d.js", j)), "x\n")
		}
	}
	writeT(t, filepath.Join(worktree, "build.log"), "log\n")
	return main, worktree
}

func writeT(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// makeDirLink makes link a directory link to target: a junction where
// cmd's mklink exists, which is what pnpm makes on Windows and needs no
// symlink privilege, and a symlink everywhere else. Choosing by what
// succeeds rather than by GOOS keeps the platform branch in
// internal/platform, where ARCHITECTURE.md puts every one.
func makeDirLink(t *testing.T, target, link string) {
	t.Helper()
	if exec.Command("cmd", "/c", "mklink", "/J", link, target).Run() == nil {
		return
	}
	if err := os.Symlink(target, link); err != nil {
		t.Fatalf("could not link %s to %s: %v", link, target, err)
	}
}

// trashBeside names every trash directory left next to the tree.
func trashBeside(t *testing.T, worktree string) []string {
	t.Helper()
	entries, err := os.ReadDir(filepath.Dir(worktree))
	if err != nil {
		t.Fatal(err)
	}
	var found []string
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".wt-trash-") {
			found = append(found, e.Name())
		}
	}
	return found
}

// TestWorktreeRemoveDeletesIgnoredFiles: a tree whose bulk is ignored is
// removed whole, and the trash directory the ignored files went through
// is gone with it. It fails if the set-aside strands the ignored files
// beside the tree, or if moving them out makes git refuse.
func TestWorktreeRemoveDeletesIgnoredFiles(t *testing.T) {
	_, worktree := ignoredFixture(t)
	leftover, err := WorktreeRemove(worktree, Git)
	if err != nil {
		t.Fatalf("WorktreeRemove(%s): %v", worktree, err)
	}
	if leftover != "" {
		t.Errorf("WorktreeRemove left %s behind", leftover)
	}
	if _, err := os.Stat(worktree); !os.IsNotExist(err) {
		t.Errorf("%s still exists after WorktreeRemove (stat err %v)", worktree, err)
	}
	if trash := trashBeside(t, worktree); len(trash) > 0 {
		t.Errorf("trash directories left beside the tree: %v", trash)
	}
}

// TestRefusedRemoveRestoresIgnoredFiles: git refuses a tree with an
// untracked file, and the ignored files that were moved aside before it
// ran are back where they were, with their contents. This is what makes
// the set-aside safe: without the restore, a refused removal would still
// have deleted the tree's dependencies.
func TestRefusedRemoveRestoresIgnoredFiles(t *testing.T) {
	_, worktree := ignoredFixture(t)
	writeT(t, filepath.Join(worktree, "dirty.txt"), "x\n")
	if _, err := WorktreeRemove(worktree, Git); err == nil {
		t.Fatal("WorktreeRemove removed a tree with an untracked file; it must never force")
	}
	for _, rel := range []string{"deps/pkg0/lib/f0.js", "deps/pkg19/lib/f9.js", "build.log", "dirty.txt"} {
		if _, err := os.Stat(filepath.Join(worktree, filepath.FromSlash(rel))); err != nil {
			t.Errorf("%s is not back in the refused tree: %v", rel, err)
		}
	}
	if trash := trashBeside(t, worktree); len(trash) > 0 {
		t.Errorf("trash directories left beside the refused tree: %v", trash)
	}
}

// TestRemoveAllParallelLeavesLinkTargets: a directory link inside the tree
// is removed as a link, and the directory it points at keeps its files.
// pnpm and npm workspaces put such links in node_modules; following one
// would delete a package store or a sibling checkout. Two links are
// planted — one where fanOut hands it to a worker directly, one deep
// enough that os.RemoveAll meets it during its own recursion.
func TestRemoveAllParallelLeavesLinkTargets(t *testing.T) {
	base := t.TempDir()
	outside := filepath.Join(base, "outside")
	writeT(t, filepath.Join(outside, "keep.txt"), "keep\n")

	root := filepath.Join(base, "root")
	deep := filepath.Join(root, "d1", "d2", "d3", "d4", "d5")
	writeT(t, filepath.Join(deep, "f.txt"), "x\n")
	makeDirLink(t, outside, filepath.Join(root, "shallow-link"))
	makeDirLink(t, outside, filepath.Join(deep, "deep-link"))

	if err := removeAllParallel(root); err != nil {
		t.Fatalf("removeAllParallel(%s): %v", root, err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("%s still exists (stat err %v)", root, err)
	}
	if _, err := os.Stat(filepath.Join(outside, "keep.txt")); err != nil {
		t.Errorf("a link's target lost its file: %v", err)
	}
}

// TestFanOutSplitsOneLargeDirectory: a tree that is one directory holding
// everything — node_modules alone under the trash directory — is opened up
// into its entries, so the workers share it rather than one worker taking
// all of it.
func TestFanOutSplitsOneLargeDirectory(t *testing.T) {
	root := t.TempDir()
	for i := range 300 {
		writeT(t, filepath.Join(root, "only", fmt.Sprintf("f%d", i)), "x\n")
	}
	jobs := fanOut(root)
	if len(jobs) < removeFanout {
		t.Errorf("fanOut gave %d jobs, want at least %d", len(jobs), removeFanout)
	}
	for _, j := range jobs {
		if j == root || j == filepath.Join(root, "only") {
			t.Errorf("fanOut handed out the expanded directory %s as a job", j)
		}
	}
}
