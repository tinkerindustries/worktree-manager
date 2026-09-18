// Package identity is M1: classification of the current directory, root
// resolution, containment and naming (docs/design/01-identity.md). It is
// pure in the design's sense — it reads git and the environment and returns
// values; it writes nothing, allocates nothing and calls no coordinator.
//
// Classification is tested against real repositories (01-identity.md §8):
// the bug this module exists to prevent (D4) was a misreading of what git
// reports, so a test built on a mock of that same misreading would pass while
// the bug survived.
package identity

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// Outcome is the four-way classification of 01-identity.md §2.
type Outcome int

const (
	// NotARepository: git rev-parse fails from this directory.
	NotARepository Outcome = iota
	// PrimaryCheckout: --git-dir equals --git-common-dir, no standalone
	// declaration. Slot 0, never allocated and never managed (A2).
	PrimaryCheckout
	// LinkedWorktree: --git-dir differs from --git-common-dir.
	LinkedWorktree
	// StandaloneClone: --git-dir equals --git-common-dir, standalone
	// declared. Git cannot tell a disposable clone from the real main
	// checkout, so the declaration is the only thing making that call
	// (01-identity.md §2.1).
	StandaloneClone
)

// String names the outcome for verdict output and tests.
func (o Outcome) String() string {
	switch o {
	case NotARepository:
		return "not-a-repository"
	case PrimaryCheckout:
		return "primary-checkout"
	case LinkedWorktree:
		return "linked-worktree"
	case StandaloneClone:
		return "standalone-clone"
	}
	return "unknown"
}

// Classification is the answer to "where am I?". It carries the outcome, the
// acting root, the main checkout path and the git common dir.
//
// The main checkout path is a field on the result, never a function: a
// mainCheckoutPath() sitting beside worktreeRoot() is the shape of the D4
// bug, where init run from a linked worktree overwrote the main worktree's
// manifest (01-identity.md §3). A call site has to reach through the
// classification to get it, which makes the reach visible in review.
type Classification struct {
	Outcome Outcome
	// WorktreeRoot is the acting root, always the current worktree's own
	// root from `git rev-parse --show-toplevel` — never a walk back to the
	// main checkout.
	WorktreeRoot string
	// MainCheckoutPath is the primary checkout's path: the first entry of
	// `git worktree list --porcelain` for a linked worktree, the worktree
	// root itself otherwise.
	MainCheckoutPath string
	// GitCommonDir is the repository's common git directory, e.g.
	// /repo/.git for a standard layout.
	GitCommonDir string
	// Standalone records the declaration that produced a StandaloneClone
	// outcome, for verbs that want to echo it.
	Standalone bool
}

// DeletedWorktreeError is the M1 §7 "worktree directory deleted" refusal:
// git fails, and the failure is reported as itself rather than translated
// into "not a repository".
type DeletedWorktreeError struct{ Dir string }

func (e *DeletedWorktreeError) Error() string {
	return fmt.Sprintf("%s no longer exists, so it cannot be classified as a git worktree", e.Dir)
}

// StandaloneInLinkedWorktreeError is the M1 §7 refusal for a standalone
// declaration in a linked worktree: the combination is meaningless and is
// probably a copied descriptor.
type StandaloneInLinkedWorktreeError struct{ Dir string }

func (e *StandaloneInLinkedWorktreeError) Error() string {
	return fmt.Sprintf("standalone declared in %s, which is a linked worktree — the combination is meaningless and probably a copied declaration", e.Dir)
}

// GitNotFoundError names the install command for a missing git (M1 §7,
// 08-platform.md §6).
type GitNotFoundError struct{ Err error }

func (e *GitNotFoundError) Error() string {
	return fmt.Sprintf("git is not installed or not on PATH: %v", e.Err)
}

// StandaloneDeclared reads the standalone declaration from the environment.
// WT_STANDALONE=1 is accepted for container images whose entire purpose is
// disposable clones (01-identity.md §2.1, ARCHITECTURE.md §10.4). The
// descriptor also records the declaration once init exists — phase 5 — and
// that path supplies the bool to Classify directly; this function is the
// environment half of the same input.
func StandaloneDeclared() bool {
	return os.Getenv("WT_STANDALONE") == "1"
}

// Classify answers the four-way question for cwd. standalone is the
// standalone declaration: StandaloneDeclared() from the environment, or the
// descriptor's standalone field once init exists (phase 5).
func Classify(cwd string, standalone bool) (*Classification, error) {
	// The directory must exist before git is consulted: a worktree whose
	// directory has been removed makes git fail with "cannot change to",
	// and that is reported as itself (M1 §7), not translated into the
	// "not a repository" outcome.
	fi, err := os.Stat(cwd)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, &DeletedWorktreeError{Dir: cwd}
		}
		return nil, fmt.Errorf("classifying %s: %w", cwd, err)
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("classifying %s: not a directory", cwd)
	}

	toplevel, err := gitPath(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		// git rev-parse fails: not a repository (01-identity.md §2). The
		// remaining fields are meaningless and stay empty.
		return &Classification{Outcome: NotARepository}, nil
	}
	gitDirRaw, err := gitPath(cwd, "rev-parse", "--git-dir")
	if err != nil {
		return nil, fmt.Errorf("classifying %s: rev-parse --git-dir: %w", cwd, err)
	}
	commonDirRaw, err := gitPath(cwd, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil, fmt.Errorf("classifying %s: rev-parse --git-common-dir: %w", cwd, err)
	}

	// Both rev-parse outputs are relative to cwd; absolutise, then compare
	// the realised forms so two spellings of one directory still compare
	// equal.
	gitDir := absJoin(cwd, gitDirRaw)
	commonDir := absJoin(cwd, commonDirRaw)
	equal, err := platform.SamePath(gitDir, commonDir)
	if err != nil {
		return nil, fmt.Errorf("classifying %s: comparing git dirs: %w", cwd, err)
	}

	cls := &Classification{
		WorktreeRoot: toplevel,
		GitCommonDir: commonDir,
		Standalone:   standalone,
	}
	if !equal {
		if standalone {
			return nil, &StandaloneInLinkedWorktreeError{Dir: cwd}
		}
		main, err := mainCheckoutPath(cwd)
		if err != nil {
			return nil, err
		}
		cls.Outcome = LinkedWorktree
		cls.MainCheckoutPath = main
		return cls, nil
	}
	if standalone {
		cls.Outcome = StandaloneClone
	} else {
		cls.Outcome = PrimaryCheckout
	}
	cls.MainCheckoutPath = toplevel
	return cls, nil
}

// WorktreeRoot is the acting root for cwd, per 01-identity.md §3: always the
// current worktree's own root from --show-toplevel, never a walk back to the
// main checkout. Classification carries the same value; this is the
// M1 §6 surface for callers that need only the root.
func WorktreeRoot(cwd string) (string, error) {
	if _, err := os.Stat(cwd); err != nil {
		if os.IsNotExist(err) {
			return "", &DeletedWorktreeError{Dir: cwd}
		}
		return "", fmt.Errorf("classifying %s: %w", cwd, err)
	}
	root, err := gitPath(cwd, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("%s is not inside a git work tree", cwd)
	}
	return root, nil
}

// mainCheckoutPath derives the primary checkout's path from
// `git worktree list --porcelain`: the first worktree entry is the main
// checkout, by git's own contract.
func mainCheckoutPath(cwd string) (string, error) {
	out, err := runGit(cwd, "worktree", "list", "--porcelain")
	if err != nil {
		return "", fmt.Errorf("listing worktrees for %s: %w", cwd, err)
	}
	for _, line := range strings.Split(out, "\n") {
		if rest, ok := strings.CutPrefix(line, "worktree "); ok {
			return filepath.FromSlash(rest), nil
		}
	}
	return "", fmt.Errorf("listing worktrees for %s: no worktree entry found", cwd)
}

// absJoin absolutises a possibly-relative path against base. Git reports
// --git-dir and --git-common-dir relative to cwd, and cwd itself may be
// relative, so the base is absolutised first and the result cleaned.
func absJoin(base, p string) string {
	absBase, err := filepath.Abs(base)
	if err != nil {
		absBase = base
	}
	if filepath.IsAbs(p) {
		return filepath.Clean(p)
	}
	return filepath.Clean(filepath.Join(absBase, p))
}

// gitPath is runGit for a command whose output is a path. git reports
// paths with forward slashes on every platform, so on Windows
// --show-toplevel answers C:/Users/u/wt1 while every other path in this
// process is native. filepath.FromSlash is the whole conversion (the
// identity on unix), applied here so a classification carries a path in
// the same spelling as the ones it is joined onto and compared with.
func gitPath(cwd string, args ...string) (string, error) {
	out, err := runGit(cwd, args...)
	if err != nil {
		return "", err
	}
	return filepath.FromSlash(out), nil
}

// runGit runs one git command with cwd as the working directory and returns
// its trimmed stdout. A missing git binary is reported as GitNotFoundError,
// naming the install command.
//
// The lookup is platform.HelperCommand's, so a git that lives only in a
// known install directory is found here as well as by the coordinator's
// doctor, and the child is given a PATH leading with the directory git came
// from — git runs its own subcommands and credential helpers from there.
func runGit(cwd string, args ...string) (string, error) {
	cmd, err := platform.HelperCommand("git", args...)
	if err != nil {
		return "", &GitNotFoundError{Err: err}
	}
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}
