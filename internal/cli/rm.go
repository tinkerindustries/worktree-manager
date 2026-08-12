package cli

// rm.go is `wt rm` (04-lifecycle.md §7, ARCHITECTURE.md §9.2): the safety
// checks in the client, then reap, teardown and deallocate in the
// coordinator, then `git worktree remove` — teardown first, git removal
// second, because teardown needs the registry entry the removal would
// orphan.
//
// The three safety checks read the working tree and run before anything is
// destroyed: uncommitted changes; unpushed commits via `git log @{u}..HEAD`
// — an absent upstream is itself a stop, not a pass; and an open PR, which
// needs gh. When gh is missing or unauthenticated the check reports
// unavailable and rm stops with exit 4: a safety check that cannot run
// fails closed.
//
// rm accepts a slug rather than only inferring its target from cwd, because
// the routine case is that the directory is already gone. It never picks a
// target on its own — "the most recent worktree" is not an inference this
// tool makes. The destruction rails: never --force a git worktree remove
// (git refusing is signal that a check missed something); never delete the
// worktree the caller is standing in; never delete the remote branch (the
// PR points at it).
//
// Partial states each get their own path (04-lifecycle.md §7.3): directory
// gone with an entry present is a registry-and-label-only teardown;
// directory present with no entry is a `git worktree remove` and nothing to
// deallocate; neither is a message and a stop.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// rmResult is the one JSON object `wt rm --json` prints.
type rmResult struct {
	App  string `json:"app"`
	Slug string `json:"slug"`
	// Removed reports the teardown outcome: the entry dropped.
	Removed bool `json:"removed"`
	// WorktreeRemoved reports whether the git worktree was removed.
	WorktreeRemoved bool `json:"worktree_removed,omitempty"`
	// Reap is the reaper's bounded-coverage statement.
	Reap protocol.ReapReport `json:"reap"`
	// Notes are the bounded-coverage statements: skipped checks, skipped
	// git removal, and what survived.
	Notes []string `json:"notes,omitempty"`
}

// runRm implements `wt rm [--json] [--dry-run] [--cwd <dir>] [--slug <s>]
// [--keep-processes] [--purge <flag>]...`.
func runRm(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "preview what would be torn down and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	slug := fs.String("slug", "", "the worktree's slug (default: the cwd's basename when cwd is a worktree)")
	keepProcesses := fs.Bool("keep-processes", false, "do not signal processes bound to the worktree's ports")
	var purgeFlags []string
	fs.Var(stringListFlag{target: &purgeFlags}, "purge", "purge this state-path resource on teardown (repeatable)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt rm --slug <slug>' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}

	dir := *cwd
	if dir == "" {
		dir = "."
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("resolving %s: %v", dir, err), ""))
		return ExitFailure
	}

	// The target: an explicit slug, or the cwd's worktree — never an
	// inference of "the most recent worktree" (04-lifecycle.md §7.1).
	targetSlug := *slug
	if targetSlug == "" {
		cls, err := identity.Classify(absDir, identity.StandaloneDeclared())
		if err != nil || (cls.Outcome != identity.LinkedWorktree && cls.Outcome != identity.StandaloneClone) {
			e := New(ExitUsage,
				"no target: give --slug <slug>, or run rm from inside the repository",
				"run 'wt rm --slug <slug>' from the main checkout (or any other directory)")
			WriteError(stderr, e)
			return e.Code
		}
		targetSlug = identity.DefaultSlug(cls.WorktreeRoot)
	}
	if reason := identity.ValidateSlug(targetSlug); reason != "" {
		e := New(ExitRefused,
			fmt.Sprintf("slug %q is not valid (%s)", targetSlug, reason),
			"give a slug matching ^[a-z0-9][a-z0-9-]*$ (at most 32 characters), then re-run")
		WriteError(stderr, e)
		return e.Code
	}

	// The spec comes from the caller's tree — rm must work when the target
	// directory is already gone, and the caller's cwd is the one tree that
	// still exists.
	sp, code := loadSpec(absDir, stderr)
	if code != ExitOK {
		return code
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()

	// Phase one: prepare — ask the coordinator for the entry (its path and
	// its resources) and the reap preview, changing nothing (the reap runs
	// in dry-run, so nothing is signalled).
	prep, perr := rmRequest(sess, sp, targetSlug, *keepProcesses, true, purgeFlags)
	if perr != nil {
		WriteError(stderr, perr)
		return perr.Code
	}
	if !prep.EntryFound {
		return rmNoEntry(stdout, stderr, *jsonOut, sp, targetSlug, absDir)
	}

	// Never delete the worktree the caller is standing in (B11.11): the
	// classification detects it directly.
	if prep.Path != "" {
		if identity.Contains(prep.Path, absDir) {
			e := New(ExitRefused,
				fmt.Sprintf("refusing to remove %s: the caller is standing inside it — remove a worktree from outside it", prep.Path),
				fmt.Sprintf("run 'wt rm --slug %s' from the main checkout or another directory", targetSlug))
			WriteError(stderr, e)
			return e.Code
		}
	}

	// The safety checks read the working tree; a tree whose directory is
	// already gone has nothing to protect, and the checks are skipped with
	// the bound stated. Otherwise any hit stops with exit 3.
	var notes []string
	targetRoot := prep.Path
	targetExists := false
	if targetRoot != "" {
		if fi, serr := os.Stat(targetRoot); serr == nil && fi.IsDir() {
			targetExists = true
		}
	}
	if !targetExists {
		notes = append(notes, fmt.Sprintf("the worktree directory %s is already gone; the tree-reading safety checks were skipped, and the teardown is registry-and-label-only", targetRoot))
	} else {
		if code := rmSafetyChecks(targetRoot, stderr); code != ExitOK {
			return code
		}
	}

	// Phase two: the real thing — reap and teardown in the coordinator.
	if *dryRun {
		// The prepare call was the preview; print it and stop.
		return rmPrintPreview(stdout, stderr, *jsonOut, sp, targetSlug, prep, targetExists, notes)
	}
	res, rerr := rmRequest(sess, sp, targetSlug, *keepProcesses, false, purgeFlags)
	if rerr != nil {
		WriteError(stderr, rerr)
		return rerr.Code
	}
	for _, n := range notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}

	// Teardown done: the git half — `git worktree remove`, never --force.
	// A standalone clone is not a git worktree and has no registration to
	// remove; its directory is left in place and the note says so.
	worktreeRemoved := false
	if res.Removed && targetExists {
		if isStandalone(targetRoot) {
			notes = append(notes, fmt.Sprintf("%s is a standalone clone, not a git worktree: its directory is left in place (remove it yourself if it was disposable)", targetRoot))
		} else if code := gitWorktreeRemove(targetRoot, stderr); code == ExitOK {
			worktreeRemoved = true
		} else {
			// git refusing is signal that a check missed something
			// (B11.11): the error is reported as itself, never forced.
			return code
		}
	}

	writeRmReport(stdout, stderr, *jsonOut, rmResult{
		App: sp.App, Slug: targetSlug, Removed: res.Removed,
		WorktreeRemoved: worktreeRemoved,
		Reap:            res.Reap,
		Notes:           notes,
	})
	return ExitOK
}

// rmRequest runs one rm call and decodes the result.
func rmRequest(sess *coordSession, sp *spec.Spec, slug string, keepProcesses, dryRun bool, purgeFlags []string) (*protocol.RmResult, *Error) {
	raw, err := sess.request("rm", &protocol.RmArgs{
		App: sp.App, Slug: slug, Spec: *sp,
		KeepProcesses: keepProcesses, DryRun: dryRun, PurgeFlags: purgeFlags,
	})
	if err != nil {
		return nil, err
	}
	var res protocol.RmResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return nil, New(ExitFailure, fmt.Sprintf("decoding the rm response: %v", err), "")
	}
	return &res, nil
}

// rmNoEntry handles the two no-entry paths of 04-lifecycle.md §7.3:
// directory present (a `git worktree remove` and nothing to deallocate) or
// neither (a message and a stop).
func rmNoEntry(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug, callerDir string) int {
	// Locate the tree by slug from the caller's repository: the slug is
	// the directory basename, and `git worktree list` names every tree.
	root := ""
	if out, err := runGitOutput(callerDir, "worktree", "list", "--porcelain"); err == nil {
		var path string
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "worktree "); ok {
				path = rest
				continue
			}
			// The next entry's first line starts a new record.
		}
		_ = path
		for _, line := range strings.Split(out, "\n") {
			if rest, ok := strings.CutPrefix(line, "worktree "); ok && filepath.Base(rest) == slug {
				root = rest
				break
			}
		}
	}
	if root == "" {
		e := New(ExitFailure,
			fmt.Sprintf("no registry entry for %s/%s and no git worktree named %q in this repository: nothing to remove", sp.App, slug, slug),
			"check the slug with 'wt list' (phase 6) or 'git worktree list', then re-run")
		WriteError(stderr, e)
		return e.Code
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		e := New(ExitFailure,
			fmt.Sprintf("no registry entry for %s/%s and the worktree directory %s is already gone: nothing to remove", sp.App, slug, root),
			"check the slug with 'wt list' (phase 6) or 'git worktree list', then re-run")
		WriteError(stderr, e)
		return e.Code
	}
	// Directory present, no entry: the safety checks still run — a tree is
	// about to be removed — then `git worktree remove`, nothing to
	// deallocate.
	if code := rmSafetyChecks(root, stderr); code != ExitOK {
		return code
	}
	if code := gitWorktreeRemove(root, stderr); code != ExitOK {
		return code
	}
	writeRmReport(stdout, stderr, jsonOut, rmResult{
		App: sp.App, Slug: slug, Removed: false,
		WorktreeRemoved: true,
		Notes:           []string{fmt.Sprintf("no registry entry for %s/%s: the git worktree was removed and nothing was deallocated", sp.App, slug)},
	})
	return ExitOK
}

// rmSafetyChecks runs the three tree-reading checks, in order, and returns
// the exit code: 0 when all pass, 3 on any hit, 4 when the PR check cannot
// run (gh missing or unauthenticated — a safety check that cannot run
// fails closed).
func rmSafetyChecks(root string, stderr io.Writer) int {
	// 1. Uncommitted changes.
	if out, err := runGitOutput(root, "status", "--porcelain"); err != nil {
		e := New(ExitUnavailable,
			fmt.Sprintf("the uncommitted-changes check could not run in %s: %v", root, err),
			"fix git, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	} else if strings.TrimSpace(out) != "" {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		shown := lines
		if len(shown) > 5 {
			shown = shown[:5]
		}
		e := New(ExitRefused,
			fmt.Sprintf("refusing to remove %s: it has uncommitted changes (%d changed path(s), first: %s)",
				root, len(lines), strings.Join(shown, "; ")),
			"commit or stash the changes, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}

	// 2. Unpushed commits: `git log @{u}..HEAD`. An absent upstream is
	// itself a stop, not a pass (B11.10).
	out, err := runGitOutput(root, "log", "@{u}..HEAD", "--oneline")
	if err != nil {
		msg := err.Error()
		if strings.Contains(msg, "no upstream") || strings.Contains(msg, "@{u}") || strings.Contains(msg, "unknown revision") {
			e := New(ExitRefused,
				fmt.Sprintf("refusing to remove %s: the branch has no upstream, and an absent upstream is itself a stop, not a pass", root),
				"push the branch (git push -u origin <branch>) or set an upstream, then re-run rm")
			WriteError(stderr, e)
			return e.Code
		}
		e := New(ExitUnavailable,
			fmt.Sprintf("the unpushed-commits check could not run in %s: %v", root, err),
			"fix git, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	if strings.TrimSpace(out) != "" {
		lines := strings.Split(strings.TrimSpace(out), "\n")
		shown := lines
		if len(shown) > 5 {
			shown = shown[:5]
		}
		e := New(ExitRefused,
			fmt.Sprintf("refusing to remove %s: it has %d unpushed commit(s), first: %s",
				root, len(lines), strings.Join(shown, "; ")),
			"push the commits, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}

	// 3. An open PR. gh missing or unauthenticated is exit 4: the check
	// cannot run, and rm fails closed.
	code, detail := checkOpenPR(root)
	if code != ExitOK {
		e := New(code, detail, ghRemedy(code, detail))
		WriteError(stderr, e)
		return e.Code
	}
	return ExitOK
}

// checkOpenPR asks gh whether the branch has an open PR. It returns
// (ExitOK, "") when there is none; otherwise the exit code and the message:
// 3 when a PR is open, 4 when the check could not run.
func checkOpenPR(root string) (int, string) {
	gh, err := exec.LookPath("gh")
	if err != nil {
		return ExitUnavailable, "the open-PR check cannot run: gh is not installed or not on PATH"
	}
	cmd := exec.Command(gh, "pr", "view", "--json", "number,state")
	cmd.Dir = root
	out, err := cmd.CombinedOutput()
	text := string(out)
	if err == nil {
		var pr struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		}
		if jerr := json.Unmarshal(out, &pr); jerr == nil && pr.Number > 0 {
			return ExitRefused, fmt.Sprintf("refusing to remove %s: branch has an open PR #%d (%s); the PR points at the branch, and the remote branch is never deleted", root, pr.Number, pr.State)
		}
		return ExitRefused, fmt.Sprintf("refusing to remove %s: gh reports an open PR for this branch", root)
	}
	lower := strings.ToLower(text)
	if strings.Contains(lower, "no pull requests found") {
		return ExitOK, "" // the check ran: no PR
	}
	// gh is present but could not answer: unauthenticated, not a GitHub
	// repo, or broken. A safety check that cannot run fails closed (B11.10).
	detail := fmt.Sprintf("the open-PR check could not run in %s: gh failed (%s)", root, strings.TrimSpace(text))
	if strings.Contains(lower, "auth") || strings.Contains(lower, "authenticate") || strings.Contains(lower, "login") {
		detail = fmt.Sprintf("the open-PR check could not run: gh is not authenticated (%s)", strings.TrimSpace(text))
	}
	return ExitUnavailable, detail
}

// ghRemedy names the fix for each gh failure.
func ghRemedy(code int, detail string) string {
	switch code {
	case ExitRefused:
		return "close or merge the PR, then re-run rm"
	case ExitUnavailable:
		if strings.Contains(detail, "not installed") {
			return "install the GitHub CLI (brew install gh / apt install gh), then re-run rm"
		}
		if strings.Contains(detail, "not authenticated") {
			return "run 'gh auth login', then re-run rm"
		}
		return "fix gh (the error above names the problem), then re-run rm"
	}
	return ""
}

// gitWorktreeRemove runs `git worktree remove <root>` from inside the tree,
// never with --force: git refusing is signal that a check missed something
// (B11.11), and the refusal is reported as itself.
func gitWorktreeRemove(root string, stderr io.Writer) int {
	cmd := exec.Command("git", "-C", root, "worktree", "remove", root)
	if out, err := cmd.CombinedOutput(); err != nil {
		e := New(ExitFailure,
			fmt.Sprintf("git worktree remove refused to remove %s: %s — git refusing indicates a safety check missed something; nothing was forced",
				root, strings.TrimSpace(string(out))),
			"resolve what git names, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	return ExitOK
}

// isStandalone reports whether the tree classifies as a standalone clone.
func isStandalone(root string) bool {
	cls, err := identity.Classify(root, identity.StandaloneDeclared())
	return err == nil && cls.Outcome == identity.StandaloneClone
}

// rmPrintPreview prints what the real rm would do, changing nothing.
func rmPrintPreview(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug string, prep *protocol.RmResult, targetExists bool, notes []string) int {
	note := "dry run: nothing was changed."
	for _, n := range notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	fmt.Fprintf(stderr, "would reap: %s\n", reapSummary(&prep.Reap))
	fmt.Fprintf(stderr, "would tear down the resources: %s\n", strings.Join(prep.Resources, ", "))
	if targetExists && !isStandalone(prep.Path) {
		fmt.Fprintf(stderr, "would run: git worktree remove %s\n", prep.Path)
	}
	if jsonOut {
		if err := WriteJSON(stdout, map[string]any{
			"dry_run": true, "app": sp.App, "slug": slug,
			"reap": prep.Reap, "resources": prep.Resources,
			"worktree": prep.Path,
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	fmt.Fprintln(stdout, note)
	return ExitOK
}

// reapSummary renders the reaper's report as one diagnostic line.
func reapSummary(r *protocol.ReapReport) string {
	switch {
	case r.KeptProcesses:
		return r.Note
	case !r.Available:
		return r.Note
	case len(r.Signalled) == 0 && len(r.Holders) == 0:
		return "no process bound to this worktree's ports"
	default:
		var parts []string
		for _, a := range r.Signalled {
			parts = append(parts, fmt.Sprintf("%s(%d, %s)", a.Signal, a.PID, a.Command))
		}
		for _, h := range r.Holders {
			parts = append(parts, fmt.Sprintf("reported %s(%d) — %s", h.Command, h.PID, h.Reason))
		}
		return strings.Join(parts, "; ")
	}
}

// writeRmReport prints the final rm result.
func writeRmReport(stdout, stderr io.Writer, jsonOut bool, r rmResult) int {
	for _, n := range r.Notes {
		fmt.Fprintf(stderr, "note: %s\n", n)
	}
	fmt.Fprintf(stderr, "reap: %s\n", reapSummary(&r.Reap))
	if jsonOut {
		if err := WriteJSON(stdout, r); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	if r.WorktreeRemoved {
		fmt.Fprintf(stdout, "removed %s/%s: the git worktree is gone", r.App, r.Slug)
		if r.Removed {
			fmt.Fprintf(stdout, " and the registry entry was dropped")
		}
		fmt.Fprintln(stdout)
	} else if r.Removed {
		fmt.Fprintf(stdout, "removed %s/%s: the registry entry was dropped\n", r.App, r.Slug)
	} else {
		fmt.Fprintf(stdout, "rm of %s/%s: nothing was removed (see the notes above)\n", r.App, r.Slug)
	}
	return ExitOK
}

// runGitOutput runs one git command with cwd and returns its stdout.
func runGitOutput(cwd string, args ...string) (string, error) {
	cmd := exec.Command("git", args...)
	cmd.Dir = cwd
	out, err := cmd.Output()
	if err != nil {
		var ee *exec.ExitError
		if errors.As(err, &ee) {
			return "", fmt.Errorf("git %s: %s", strings.Join(args, " "), strings.TrimSpace(string(ee.Stderr)))
		}
		return "", err
	}
	return string(out), nil
}

// stringListFlag collects repeated string flags.
type stringListFlag struct{ target *[]string }

func (f stringListFlag) String() string { return "" }
func (f stringListFlag) Set(v string) error {
	*f.target = append(*f.target, v)
	return nil
}
