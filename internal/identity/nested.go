package identity

// nested.go is the "worktree nested inside another worktree" refusal
// (04-lifecycle.md §7, deferred from phase 1 for want of a real-repo
// fixture). `git worktree add` refuses a path inside its own repo's trees,
// but nothing stops a standalone clone (or another repo's worktree) from
// sitting inside a worktree — and a tree nested inside another tree is
// indistinguishable from it in every listing the user reads. init holds
// the classification and refuses on it, naming both trees.

import (
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// NestedInside returns the enclosing git working tree of worktreeRoot, or
// "" when the tree is not nested inside another one. The walk starts at
// the tree's parent and stops at the filesystem root: the first ancestor
// that is itself a git working tree root is the enclosing tree.
//
// The check is conservative in the safe direction: an ancestor carrying a
// `.git` entry is verified with git itself (rev-parse must name the
// ancestor as its own root), so a stray `.git` directory cannot trigger
// the refusal. A tree nested inside the main checkout of its own repo is
// impossible through `git worktree add` (git refuses the path), but a
// clone can land there — and is refused like any other nesting.
func NestedInside(worktreeRoot string) (string, error) {
	// Resolve the tree itself first: a symlinked spelling of the root must
	// walk the same ancestors as git's own resolution, or the walk would
	// start in the symlink's parent instead of the tree's.
	resolved, err := platform.RealPath(worktreeRoot)
	if err != nil {
		return "", err
	}
	dir := filepath.Dir(resolved)
	for {
		root, ok, err := gitTreeRoot(dir)
		if err != nil {
			return "", err
		}
		if ok {
			return root, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// gitTreeRoot reports whether dir is itself a git working tree root, and
// if so returns its root as git reports it (symlink-resolved).
func gitTreeRoot(dir string) (string, bool, error) {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return "", false, nil
	}
	root, err := runGit(dir, "rev-parse", "--show-toplevel")
	if err != nil {
		// A `.git` entry git does not honour (a stray directory, a broken
		// worktree registration): not a working tree root.
		return "", false, nil
	}
	same, err := platform.SamePath(root, dir)
	if err != nil {
		return "", false, err
	}
	return root, same, nil
}
