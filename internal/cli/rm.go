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
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/identity"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/treecheck"
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
	Reap api.ReapReport `json:"reap"`
	// Notes are the bounded-coverage statements: skipped checks, skipped
	// git removal, and what survived.
	Notes []string `json:"notes,omitempty"`
}

// runRm implements `wt rm [--json] [--dry-run] [--cwd <dir>] [--slug <s>]
// [--keep-processes] [--purge <flag>]... [--keep-vm]`. The keep flags are
// the machine resources' keep_flag names from the spec (B4.3), so they are
// registered after the spec loads; a spec that declares keep_flag:
// "--keep-vm" makes `wt rm --keep-vm` legal, and a spec without one
// refuses the flag as undefined.
func runRm(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("rm", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "preview what would be torn down and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	slug := fs.String("slug", "", "the worktree's slug (default: the cwd's basename when cwd is a worktree)")
	keepProcesses := fs.Bool("keep-processes", false, "do not signal processes bound to the worktree's ports")
	var purgeFlags []string
	fs.Var(stringList(&purgeFlags), "purge", "purge this state-path resource on teardown (repeatable)")

	// The spec must load before the keep flags can be registered, and the
	// spec's location depends on --cwd, so the flag's value is pre-scanned
	// from the raw args in both spellings the flag package accepts.
	dir := *cwd
	if v := preScanFlag(args, "cwd"); v != "" {
		dir = v
	}
	absDir, err := filepath.Abs(dir)
	if err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("resolving %s: %v", dir, err), ""))
		return ExitFailure
	}

	// The target: an explicit slug, or the cwd's worktree — never an
	// inference of "the most recent worktree" (04-lifecycle.md §7.1). The
	// slug's value is pre-scanned so the target check keeps its place
	// before the spec load, which the keep-flag registration needs.
	targetSlug := preScanFlag(args, "slug")
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

	sp, code := loadSpec(absDir, stderr)
	if code != ExitOK {
		return code
	}

	// The keep flags: one boolean flag per declared keep_flag, deduplicated
	// (two machine resources may declare the same flag name).
	keepFlags := []string{}
	seenKeep := map[string]bool{}
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type != "machine" || r.KeepFlag == nil || seenKeep[*r.KeepFlag] {
			continue
		}
		seenKeep[*r.KeepFlag] = true
		name := strings.TrimPrefix(*r.KeepFlag, "--")
		keepFlags = append(keepFlags, *r.KeepFlag)
		fs.BoolVar(new(bool), name, false, "keep the VM up: tear down the containers and the entry but leave the instance running")
	}

	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt rm --slug <slug>' with no positional arguments",
			"unexpected arguments: %v", fs.Args()))
		return ExitUsage
	}
	keepPassed := []string{}
	for _, kf := range keepFlags {
		if flagValue(fs, strings.TrimPrefix(kf, "--")) {
			keepPassed = append(keepPassed, kf)
		}
	}
	// The parsed --slug is authoritative once Parse has run; the pre-scan
	// only existed to place the target check before the spec load.
	if *slug != "" {
		targetSlug = *slug
	}

	if reason := identity.ValidateSlug(targetSlug); reason != "" {
		e := New(ExitRefused,
			fmt.Sprintf("slug %q is not valid (%s)", targetSlug, reason),
			"give a slug matching ^[a-z0-9][a-z0-9-]*$ (at most 32 characters), then re-run")
		WriteError(stderr, e)
		return e.Code
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
	prep, perr := rmRequest(sess, sp, targetSlug, *keepProcesses, true, purgeFlags, keepPassed)
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
	res, rerr := rmRequest(sess, sp, targetSlug, *keepProcesses, false, purgeFlags, keepPassed)
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
func rmRequest(sess *coordClient, sp *spec.Spec, slug string, keepProcesses, dryRun bool, purgeFlags, keepFlags []string) (*api.RmResult, *Error) {
	res, rerr := sess.client.Rm(&api.RmArgs{
		App: sp.App, Slug: slug, Spec: *sp,
		KeepProcesses: keepProcesses, DryRun: dryRun, PurgeFlags: purgeFlags, KeepFlags: keepFlags,
	})
	if rerr != nil {
		return nil, requestErr(sess.endpoint, api.VerbRm, rerr)
	}
	return res, nil
}

// rmNoEntry handles the two no-entry paths of 04-lifecycle.md §7.3:
// directory present (a `git worktree remove` and nothing to deallocate) or
// neither (a message and a stop).
func rmNoEntry(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug, callerDir string) int {
	// Locate the tree by slug from the caller's repository: the slug is
	// the directory basename, and `git worktree list` names every tree.
	root := ""
	if out, err := runGitOutput(callerDir, "worktree", "list", "--porcelain"); err == nil {
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
			"check the slug with 'wt list' or 'git worktree list', then re-run")
		WriteError(stderr, e)
		return e.Code
	}
	if fi, err := os.Stat(root); err != nil || !fi.IsDir() {
		e := New(ExitFailure,
			fmt.Sprintf("no registry entry for %s/%s and the worktree directory %s is already gone: nothing to remove", sp.App, slug, root),
			"check the slug with 'wt list' or 'git worktree list', then re-run")
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

// rmMaxLines caps how many changed paths or unpushed commits a refusal
// names.
const rmMaxLines = 5

// rmSafetyChecks runs the three tree-reading checks, in order, and returns
// the exit code: 0 when all pass, 3 on any hit, 4 when the PR check cannot
// run (gh missing or unauthenticated — a safety check that cannot run
// fails closed). The checks themselves live in internal/treecheck; what is
// here is rm's policy on each answer.
func rmSafetyChecks(root string, stderr io.Writer) int {
	// 1. Uncommitted changes.
	res, err := treecheck.Uncommitted(root, treecheck.Git)
	if err != nil {
		e := New(ExitUnavailable,
			fmt.Sprintf("the uncommitted-changes check could not run in %s: %v", root, err),
			"fix git, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	if !res.OK() {
		e := New(ExitRefused,
			fmt.Sprintf("refusing to remove %s: it has uncommitted changes (%d changed path(s), first: %s)",
				root, len(res.Lines), res.First(rmMaxLines)),
			"commit or stash the changes, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}

	// 2. Unpushed commits: `git log @{u}..HEAD`. An absent upstream is
	// itself a stop, not a pass (B11.10).
	res, err = treecheck.Unpushed(root, treecheck.Git)
	if errors.Is(err, treecheck.ErrNoUpstream) {
		e := New(ExitRefused,
			fmt.Sprintf("refusing to remove %s: the branch has no upstream, and an absent upstream is itself a stop, not a pass", root),
			"push the branch (git push -u origin <branch>) or set an upstream, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	if err != nil {
		e := New(ExitUnavailable,
			fmt.Sprintf("the unpushed-commits check could not run in %s: %v", root, err),
			"fix git, then re-run rm")
		WriteError(stderr, e)
		return e.Code
	}
	if !res.OK() {
		e := New(ExitRefused,
			fmt.Sprintf("refusing to remove %s: it has %d unpushed commit(s), first: %s",
				root, len(res.Lines), res.First(rmMaxLines)),
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
// (ExitOK, "") when there is none and when the PR has already merged or
// closed — that is the end of a branch's life, and rm is how the worktree
// goes with it. Otherwise the exit code and the message: 3 when a PR is
// open, 4 when the check could not run.
func checkOpenPR(root string) (int, string) {
	pr, err := treecheck.PRState(root, treecheck.Gh)
	if err != nil {
		var ue *treecheck.UnavailableError
		if errors.As(err, &ue) {
			switch {
			case ue.NotInstalled:
				return ExitUnavailable, "the open-PR check cannot run: gh is not installed or not on PATH"
			case ue.NotAuthenticated:
				return ExitUnavailable, fmt.Sprintf("the open-PR check could not run: gh is not authenticated (%s)", ue.Detail)
			}
			return ExitUnavailable, fmt.Sprintf("the open-PR check could not run in %s: gh failed (%s)", root, ue.Detail)
		}
		return ExitUnavailable, fmt.Sprintf("the open-PR check could not run in %s: %v", root, err)
	}
	if pr.Open() {
		return ExitRefused, fmt.Sprintf("refusing to remove %s: branch has an open PR #%d (%s); the PR points at the branch, and the remote branch is never deleted", root, pr.Number, pr.State)
	}
	return ExitOK, ""
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

// gitWorktreeRemove runs `git worktree remove <root>`, never with --force:
// git refusing is signal that a check missed something (B11.11), and the
// refusal is reported as itself.
func gitWorktreeRemove(root string, stderr io.Writer) int {
	if err := treecheck.WorktreeRemove(root, treecheck.Git); err != nil {
		e := New(ExitFailure,
			fmt.Sprintf("git worktree remove refused to remove %s: %s — git refusing indicates a safety check missed something; nothing was forced",
				root, err),
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
func rmPrintPreview(stdout, stderr io.Writer, jsonOut bool, sp *spec.Spec, slug string, prep *api.RmResult, targetExists bool, notes []string) int {
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
func reapSummary(r *api.ReapReport) string {
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
	out, err := treecheck.Git(cwd, args...)
	if err != nil {
		return "", err
	}
	return string(out), nil
}

// preScanFlag finds a flag's value in the raw args before the flag package
// parses them, in both spellings the flag package accepts ("--cwd dir" and
// "--cwd=dir", with one or two dashes). It exists so rm can load the spec
// — and with it the machine keep flags the spec declares — before parsing.
// An absent flag returns "".
func preScanFlag(args []string, name string) string {
	for i := 0; i < len(args); i++ {
		a := args[i]
		for _, prefix := range []string{"--" + name + "=", "-" + name + "="} {
			if v, ok := strings.CutPrefix(a, prefix); ok {
				return v
			}
		}
		if a == "--"+name || a == "-"+name {
			if i+1 < len(args) {
				return args[i+1]
			}
		}
	}
	return ""
}

// flagValue reads a boolean flag's value after Parse.
func flagValue(fs *flag.FlagSet, name string) bool {
	f := fs.Lookup(name)
	if f == nil {
		return false
	}
	b, _ := f.Value.(flag.Getter).Get().(bool)
	return b
}
