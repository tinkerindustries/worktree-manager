package cli

import (
	"flag"
	"fmt"
	"io"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/treecheck"
)

// claudeRemoveResult is the one JSON object `wt claude remove-worktree
// --json` prints.
type claudeRemoveResult struct {
	Removed  string `json:"removed"`
	Leftover string `json:"leftover,omitempty"`
}

// runClaudeRemoveWorktree is the WorktreeRemove hook's removal for a
// repository with no wt.yaml: treecheck.WorktreeRemove on one path, with no
// spec, no registry entry and no coordinator. An adopted repository's
// worktree goes through `wt rm` instead, which tears its environment down
// first.
func runClaudeRemoveWorktree(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("claude remove-worktree", flag.ContinueOnError)
	fs.SetOutput(stderr)
	path := fs.String("path", "", "the worktree to remove")
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if *path == "" || fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt claude remove-worktree --path <worktree>'",
			"remove-worktree takes --path and no arguments"))
		return ExitUsage
	}
	root, err := platform.RealPath(*path)
	if err != nil {
		e := New(ExitFailure, fmt.Sprintf("cannot resolve %s: %v", *path, err),
			"check the path names an existing worktree")
		WriteError(stderr, e)
		return e.Code
	}
	leftover, err := treecheck.WorktreeRemove(root, treecheck.Git)
	if err != nil {
		e := New(ExitFailure,
			fmt.Sprintf("git worktree remove refused to remove %s: %s", root, err),
			"resolve what git names, then remove the worktree again")
		WriteError(stderr, e)
		return e.Code
	}
	if *jsonOut {
		if err := WriteJSON(stdout, claudeRemoveResult{Removed: root, Leftover: leftover}); err != nil {
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintf(stdout, "removed %s\n", root)
	if leftover != "" {
		fmt.Fprintf(stderr, "warning: the worktree is removed, but its ignored files could not all be deleted: %s — delete that directory yourself\n", leftover)
	}
	return ExitOK
}
