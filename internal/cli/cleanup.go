package cli

// cleanup.go is `wt cleanup` (06-fleet.md §7, ARCHITECTURE.md §9.3): the
// only verb that destroys a worktree unattended, and every rail on it
// matters.
//
//   - Gated on gh. When gh is unavailable — missing or unauthenticated —
//     it cleans nothing and exits 4. Guessing at merge status is how a
//     sweep deletes work.
//   - It asks gh whether each branch's PR is merged, and applies the full
//     rm safety checks even when the PR is merged — a merged PR says the
//     branch landed and says nothing about whether the tree is clean.
//   - It only ever touches entries whose worktree the coordinator can
//     stat: an unverifiable entry (a container path) is skipped, never
//     called stale and never cleaned (ARCHITECTURE.md §10.3).
//   - --dry-run previews exactly what the real run does, changing
//     nothing.
//
// The scheduled sweep on the coordinator's own timer is deliberately more
// conservative than this interactive verb and logs every skip
// (06-fleet.md §7.2); it lives coordinator-side in coord/cleanup.go.

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"maps"
	"os"
	"path/filepath"
	"slices"
	"sort"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/treecheck"
)

// cleanupRow is one entry's outcome; text and JSON share the same shape.
type cleanupRow struct {
	Slug   string `json:"slug"`
	Action string `json:"action"` // cleaned | would-clean | failed | skipped | worktree-removal-failed
	Detail string `json:"detail,omitempty"`
}

// runCleanup implements `wt cleanup [--json] [--dry-run] [--cwd <dir>]`:
// for every eligible entry whose branch's PR is merged and whose tree
// passes the rm safety checks, tear the resources down, remove the
// worktree and drop the entry. gh missing or unauthenticated cleans
// nothing and exits 4.
func runCleanup(args []string, stdout, stderr io.Writer) int {
	fs := flag.NewFlagSet("cleanup", flag.ContinueOnError)
	fs.SetOutput(stderr)
	jsonOut := fs.Bool("json", false, "print exactly one JSON object on stdout")
	dryRun := fs.Bool("dry-run", false, "preview exactly what would be cleaned and change nothing")
	cwd := fs.String("cwd", "", "classify this directory (default: the process cwd)")
	if err := fs.Parse(args); err != nil {
		return ExitUsage
	}
	if fs.NArg() > 0 {
		WriteError(stderr, UsageError(
			"run 'wt cleanup' with no positional arguments",
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
	sp, code := loadSpec(absDir, stderr)
	if code != ExitOK {
		return code
	}

	// The gh gate: missing or unauthenticated gh cleans nothing, exit 4
	// (06-fleet.md §7.1). The gate runs before any branch is even looked
	// at, so the verb can never half-run.
	if code, detail := ghGate(); code != ExitOK {
		remedy := "install the GitHub CLI (brew install gh / apt install gh), then re-run cleanup"
		if !strings.Contains(detail, "not installed") {
			remedy = "run 'gh auth login' (or fix gh — the error above names the problem), then re-run cleanup"
		}
		WriteError(stderr, New(code, detail, remedy))
		return code
	}

	sess, cerr := dialCoordinator()
	if cerr != nil {
		WriteError(stderr, cerr)
		return cerr.Code
	}
	defer sess.Close()

	raw, lerr := sess.request("list", &protocol.ListArgs{})
	if lerr != nil {
		WriteError(stderr, lerr)
		return lerr.Code
	}
	var list protocol.ListResult
	if err := json.Unmarshal(raw, &list); err != nil {
		WriteError(stderr, New(ExitFailure, fmt.Sprintf("decoding the list response: %v", err), ""))
		return ExitFailure
	}

	// The candidates: this app's entries, in slug order, that are the
	// caller's own or reclaimable (an aged-out ephemeral owner), and whose
	// worktree the coordinator can stat. Everything else is skipped with
	// the reason stated — a silent skip reads as success (plan.md §3).
	var entries []protocol.ListEntry
	var skipped []string
	for _, e := range list.Entries {
		if e.App != sp.App {
			skipped = append(skipped, fmt.Sprintf("%s/%s: belongs to another app; run 'wt cleanup' from that repo", e.App, e.Slug))
			continue
		}
		if containsFlag(e.Flags, "unverifiable") {
			skipped = append(skipped, fmt.Sprintf("%s/%s: the worktree exists only inside a container the coordinator cannot stat; cleanup never touches an unverifiable entry", e.App, e.Slug))
			continue
		}
		own := !containsFlag(e.Flags, "foreign")
		if !own && !containsFlag(e.Flags, "reclaimable") {
			skipped = append(skipped, fmt.Sprintf("%s/%s: owned by a live %s client; only its owner may clean it", e.App, e.Slug, e.OwnerKind))
			continue
		}
		if fi, serr := os.Stat(e.Path); serr != nil || !fi.IsDir() {
			skipped = append(skipped, fmt.Sprintf("%s/%s: the worktree directory %s cannot be statted from here; reconcile handles a gone directory, cleanup never removes one", e.App, e.Slug, e.Path))
			continue
		}
		entries = append(entries, e)
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].Slug < entries[j].Slug })

	// The checks, in rm's order, decide each entry: uncommitted changes,
	// unpushed commits (an absent upstream is itself a skip), then the
	// merged-PR question. A merged PR says the branch landed and says
	// nothing about whether the tree is clean — the full rm checks still
	// apply (06-fleet.md §7.1).
	var rows []cleanupRow
	for _, e := range entries {
		rows = append(rows, cleanupDecision(e))
	}

	if !*dryRun {
		// The real run: each would-clean row becomes its outcome. Own
		// entries go through the rm verb; aged-out ephemeral ones through
		// reconcile, which permits the reclaimable owner. The git half
		// runs after the entry is gone, exactly as rm sequences it.
		final := rows[:0]
		for _, r := range rows {
			if r.Action != "would-clean" {
				final = append(final, r)
				continue
			}
			var e protocol.ListEntry
			for _, cand := range entries {
				if cand.Slug == r.Slug {
					e = cand
					break
				}
			}
			row := cleanupRow{Slug: e.Slug, Action: "cleaned",
				Detail: "merged PR; resources torn down, the registry entry was dropped"}
			if containsFlag(e.Flags, "reclaimable") {
				raw, rerr := sess.request("reconcile", &protocol.ReconcileArgs{App: sp.App, Spec: *sp,
					Refs: []protocol.EntryRef{{App: sp.App, Slug: e.Slug}}})
				if rerr != nil {
					final = append(final, cleanupRow{Slug: e.Slug, Action: "failed", Detail: rerr.Msg})
					continue
				}
				var res protocol.ReconcileResult
				if err := json.Unmarshal(raw, &res); err != nil {
					WriteError(stderr, New(ExitFailure, fmt.Sprintf("decoding the reconcile response: %v", err), ""))
					return ExitFailure
				}
				oc := res.Outcomes[0]
				if oc.Action != "torn-down" {
					final = append(final, cleanupRow{Slug: oc.Slug, Action: "failed", Detail: oc.Note})
					continue
				}
				row.Detail += noteSuffix(oc.Note)
			} else {
				res, rerr := rmRequest(sess, sp, e.Slug, false, false, nil, nil)
				if rerr != nil {
					final = append(final, cleanupRow{Slug: e.Slug, Action: "failed", Detail: rerr.Msg})
					continue
				}
				if !res.Removed {
					final = append(final, cleanupRow{Slug: e.Slug, Action: "failed",
						Detail: "the teardown did not free the slot: " + res.TeardownNote})
					continue
				}
				row.Detail = fmt.Sprintf("merged PR; resources torn down (%s), the registry entry was dropped", strings.Join(res.Resources, ", "))
			}
			row.Detail += cleanupGitRemoveDetail(e)
			final = append(final, row)
		}
		rows = final
	}

	for _, s := range skipped {
		fmt.Fprintf(stderr, "note: skipped %s\n", s)
	}
	if *jsonOut {
		if err := WriteJSON(stdout, map[string]any{
			"app": sp.App, "dry_run": *dryRun,
			"cleanup": rows,
		}); err != nil {
			WriteError(stderr, New(ExitFailure, err.Error(), ""))
			return ExitFailure
		}
		return ExitOK
	}
	if len(rows) == 0 && len(skipped) == 0 {
		fmt.Fprintln(stdout, "cleanup: nothing to clean.")
		return ExitOK
	}
	for _, r := range rows {
		line := fmt.Sprintf("%s/%s: %s", sp.App, r.Slug, r.Action)
		if r.Detail != "" {
			line += " — " + r.Detail
		}
		fmt.Fprintln(stdout, line)
	}
	return ExitOK
}

// cleanupDecision runs the tree-reading and merged-PR checks for one
// candidate and returns its row: "would-clean" when every check passes, a
// "skipped" row naming the first failing check otherwise. The same
// function serves the dry-run preview and the real run, which is what
// makes the preview exactly what the real run does.
func cleanupDecision(e protocol.ListEntry) cleanupRow {
	skip := func(detail string) cleanupRow {
		return cleanupRow{Slug: e.Slug, Action: "skipped", Detail: detail}
	}

	res, gerr := treecheck.Uncommitted(e.Path, treecheck.Git)
	if gerr != nil {
		return skip(fmt.Sprintf("the uncommitted-changes check could not run: %v", gerr))
	}
	if !res.OK() {
		return skip(fmt.Sprintf("the tree has %d uncommitted change(s), first: %s", len(res.Lines), res.First(1)))
	}

	if _, ok := treecheck.OnBranch(e.Path, treecheck.Git); !ok {
		return skip("the worktree is not on a branch (detached HEAD); a branch is what a PR points at")
	}
	res, uerr := treecheck.Unpushed(e.Path, treecheck.Git)
	if uerr != nil {
		return skip(fmt.Sprintf("the unpushed-commits check could not run (an absent upstream is itself a stop): %v", uerr))
	}
	if !res.OK() {
		return skip(fmt.Sprintf("the branch has %d unpushed commit(s), first: %s", len(res.Lines), res.First(1)))
	}

	pr, perr := treecheck.PRState(e.Path, treecheck.Gh)
	if perr != nil {
		return skip(fmt.Sprintf("gh could not answer whether this branch's PR is merged (%v); the branch is skipped", perr))
	}
	if !pr.Merged() {
		if pr.State == treecheck.StateNone {
			return skip(pr.Describe())
		}
		return skip(fmt.Sprintf("%s, not merged", pr.Describe()))
	}
	return cleanupRow{Slug: e.Slug, Action: "would-clean",
		Detail: fmt.Sprintf("%s; would tear down %s and run git worktree remove %s",
			pr.Describe(), strings.Join(slices.Sorted(maps.Keys(e.Resources)), ", "), e.Path)}
}

// cleanupGitRemoveDetail runs the git half of a cleaned entry — `git
// worktree remove`, never --force (git refusing is signal that a check
// missed something) — and returns the bounded-coverage statement the
// cleaned row carries.
func cleanupGitRemoveDetail(e protocol.ListEntry) string {
	if _, err := os.Stat(e.Path); err != nil {
		return "" // the directory is already gone; nothing to remove
	}
	if isStandalone(e.Path) {
		return "; the tree is a standalone clone, not a git worktree; its directory is left in place"
	}
	if err := treecheck.WorktreeRemove(e.Path, treecheck.Git); err != nil {
		return fmt.Sprintf("; the entry is gone but git refused to remove the tree (%s); nothing was forced", err)
	}
	return "; git worktree remove ran"
}

// noteSuffix renders a reconcile outcome's note as a suffix, "" when
// empty.
func noteSuffix(note string) string {
	if note == "" {
		return ""
	}
	return "; " + note
}

// ghGate is the cleanup gate: gh installed and authenticated. It returns
// ExitUnavailable and the detail when the gate fails — cleanup then cleans
// nothing (06-fleet.md §7.1: gh missing or unauthenticated stops the whole
// verb rather than falling back to a heuristic).
func ghGate() (int, string) {
	err := treecheck.AuthStatus(treecheck.Gh)
	if err == nil {
		return ExitOK, ""
	}
	var ue *treecheck.UnavailableError
	if errors.As(err, &ue) && ue.NotInstalled {
		return ExitUnavailable, "cleanup is gated on gh: the GitHub CLI is not installed or not on PATH"
	}
	return ExitUnavailable, fmt.Sprintf("gh is not authenticated or cannot answer ('gh auth status' failed: %v)", err)
}
