package identity

// nested.go is the "worktree nested inside a foreign tree" refusal
// (04-lifecycle.md §7, deferred from phase 1 for want of a real-repo
// fixture): nothing stops a standalone clone (or another repository's
// worktree) from sitting inside a worktree, and a tree of one repository
// nested inside a tree of another is indistinguishable from it in every
// listing the user reads. init holds the classification and refuses on it,
// naming both trees.
//
// A tree of the *same* repository is not that case and is not refused.
// `.claude/worktrees/<slug>` — the default `worktrees.path`, and what
// Claude Code's own isolation creates — puts every worktree inside the
// main checkout, and git allows it (the comment that used to stand here
// said git refused such a path; it does not). Such a tree is listed by
// `git worktree list` and by `wt list` under its own slug, so the hazard
// the refusal exists for does not arise: the two trees share a repository
// and every listing tells them apart. Sameness is decided by the git
// common directory, which is the one fact that distinguishes a linked
// worktree from a clone that merely sits in the same place.

import (
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// NestedInside returns the enclosing git working tree of worktreeRoot, or
// "" when the tree is not nested inside a tree of another repository. The
// walk starts at the tree's parent and stops at the filesystem root: the
// first ancestor that is a working tree root of a different repository is
// the enclosing tree.
//
// gitCommonDir is worktreeRoot's own common directory, from the
// classification. An ancestor sharing it is a tree of the same repository
// — the `.claude/worktrees/<slug>` convention — and the walk continues
// past it, because that ancestor may itself be nested inside a foreign
// tree.
//
// The check is conservative in the safe direction: an ancestor carrying a
// `.git` entry is verified with git itself (rev-parse must name the
// ancestor as its own root), so a stray `.git` directory cannot trigger
// the refusal.
func NestedInside(worktreeRoot, gitCommonDir string) (string, error) {
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
			same, err := sameRepository(dir, gitCommonDir)
			if err != nil {
				return "", err
			}
			if !same {
				return root, nil
			}
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
	root, err := gitPath(dir, "rev-parse", "--show-toplevel")
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

// sameRepository reports whether the working tree at dir belongs to the
// repository whose common directory is gitCommonDir. git answers with a
// path that is relative to dir when dir is the tree root, so it is made
// absolute before the comparison; an unreadable answer is "not the same
// repository", the safe direction, which leaves the refusal standing.
func sameRepository(dir, gitCommonDir string) (bool, error) {
	if gitCommonDir == "" {
		return false, nil
	}
	common, err := gitPath(dir, "rev-parse", "--git-common-dir")
	if err != nil {
		return false, nil
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(dir, common)
	}
	return platform.SamePath(common, gitCommonDir)
}
