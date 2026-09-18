package descriptor

// ignore.go is 05-delivery.md §2.2: making sure the descriptor's filename
// is gitignored. The descriptor is per-worktree, per-machine state;
// committing it would carry one user's allocations into another's tree.
//
// The mechanism matters more than the goal. Appending to .gitignore from
// init is wrong because in a linked worktree .gitignore is a tracked file
// shared through the branch: every write would dirty a working tree another
// tool had just created clean — the mechanism the requirements (A8) could
// not have anticipated. So the fallback is $GIT_COMMON_DIR/info/exclude,
// which git reads from the common directory: one write covers every linked
// worktree of the repo and touches no tracked file. The right home is a
// line committed to .gitignore once during adoption (phase 7's skill); when
// adoption has committed it, this write is skipped — detected, never
// duplicated.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// IgnoreLine is the ignore line for a descriptor filename: the bare
// filename, matching at any depth — the descriptor sits at the worktree
// root, and the same line is what adoption commits to .gitignore.
func IgnoreLine(filename string) string {
	return filename
}

// IgnoreResult is what EnsureIgnored did, so the caller can say so: where
// the line now lives and whether this call wrote anything. Bounded coverage
// is stated, never silent (plan.md §3).
type IgnoreResult struct {
	// Line is the ignore line.
	Line string `json:"line"`
	// Location is where the line lives: "gitignore" (already committed by
	// adoption — nothing written) or "exclude" ($GIT_COMMON_DIR/info/exclude).
	Location string `json:"location"`
	// Wrote reports whether this call wrote the exclude file. A second call
	// on the same repo writes nothing.
	Wrote bool `json:"wrote"`
}

// EnsureIgnored makes sure the descriptor's filename is gitignored for the
// whole repository, idempotently:
//
//  1. if the worktree root's .gitignore already contains the line —
//     adoption committed it — nothing is written and the result names
//     .gitignore as the home;
//  2. otherwise the line goes to $GIT_COMMON_DIR/info/exclude, appended
//     atomically if it is not already there.
//
// A tracked .gitignore is never touched. worktreeRoot and gitCommonDir come
// from the classification (identity.Classification), the only way the
// caller should reach either.
func EnsureIgnored(worktreeRoot, gitCommonDir, filename string) (IgnoreResult, error) {
	return EnsureIgnoredLine(worktreeRoot, gitCommonDir, IgnoreLine(filename))
}

// EnsureIgnoredLine is the mechanism EnsureIgnored is one caller of: make
// git ignore one line for the whole repository, idempotently and without
// touching a tracked file. `wt init` uses it a second time for the
// directory the repository's worktrees live in (spec.WorktreeIgnoreLine),
// which is untracked content in the main checkout whenever the trees land
// inside the repository — and the create skill's pre-flight requires
// `git status --porcelain` to print nothing.
func EnsureIgnoredLine(worktreeRoot, gitCommonDir, line string) (IgnoreResult, error) {
	res := IgnoreResult{Line: line}

	// 1. Adoption's home: a committed line. Detected, never duplicated.
	if hasLine(filepath.Join(worktreeRoot, ".gitignore"), line) {
		res.Location = "gitignore"
		return res, nil
	}

	// 2. The fallback: $GIT_COMMON_DIR/info/exclude. Git reads this file
	// from the common directory, so one write covers every linked worktree
	// of the repo and touches no tracked file.
	excludePath := filepath.Join(gitCommonDir, "info", "exclude")
	if hasLine(excludePath, line) {
		res.Location = "exclude"
		return res, nil
	}
	content, err := os.ReadFile(excludePath)
	if err != nil && !os.IsNotExist(err) {
		return res, fmt.Errorf("reading %s: %w", excludePath, err)
	}
	if len(content) > 0 && !strings.HasSuffix(string(content), "\n") {
		content = append(content, '\n')
	}
	content = append(content, []byte(line+"\n")...)
	if err := platform.AtomicWrite(excludePath, content, 0o644); err != nil {
		return res, fmt.Errorf("writing %s: %w", excludePath, err)
	}
	res.Location = "exclude"
	res.Wrote = true
	return res, nil
}

// hasLine reports whether path exists and contains line as a whole line
// (trailing carriage returns tolerated, so a CRLF checkout still matches).
func hasLine(path, line string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		if strings.TrimRight(sc.Text(), "\r") == line {
			return true
		}
	}
	return false
}
