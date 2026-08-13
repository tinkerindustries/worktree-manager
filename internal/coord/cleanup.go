package coord

// cleanup.go is the coordinator's scheduled cleanup sweep (06-fleet.md
// §7.2, ARCHITECTURE.md §9.3): the resident process's own timer does what
// revision 1 wanted a launchd agent, a systemd timer or a Task Scheduler
// entry for — a resident process needs none of that, and `wt schedule
// install` was deleted in revision 2.
//
// The sweep is deliberately more conservative than the interactive `wt
// cleanup`:
//
//   - it touches only entries the coordinator's own host user owns —
//     never an ephemeral view's, never adopting a view;
//   - it needs gh, and when gh is unavailable it cleans nothing and logs
//     the skip (the interactive verb exits 4; a resident process logs);
//   - it applies the full rm safety checks even when the PR is merged —
//     a merged PR says the branch landed and says nothing about whether
//     the tree is clean;
//   - it only ever touches entries whose worktree it can stat (an
//     unverifiable entry is a container path and is skipped);
//   - it logs every decision, including every skip — an unattended
//     destructive sweep earns its keep by being boring.

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
	"github.com/mrgeoffrich/worktree-manager/internal/treecheck"
)

// SweepIntervalDefault is how often the coordinator's own cleanup sweep
// runs: the 06-fleet.md §7.2 posture of the original hourly launchd agent,
// throttled on the one-minute sweeper the sweep hangs off.
const SweepIntervalDefault = time.Hour

// sweepChecksMaxLines caps how many changed/unpushed lines a skip reason
// names; the log line stays readable.
const sweepChecksMaxLines = 3

// gh returns the handler's gh seam, defaulting to the real runner.
func (h *Handler) gh() treecheck.Runner {
	if h.Gh != nil {
		return h.Gh
	}
	return treecheck.Gh
}

// sweepInterval returns the handler's sweep interval, defaulting to the
// hourly choice.
func (h *Handler) sweepInterval() time.Duration {
	if h.SweepInterval > 0 {
		return h.SweepInterval
	}
	return SweepIntervalDefault
}

// SweepCleanup runs one scheduled sweep and returns how many entries were
// cleaned. The rails: gh gated (missing or unauthenticated gh cleans
// nothing and the skip is logged), only the coordinator's own host user's
// entries, only entries whose worktree can be statted, full rm safety
// checks even when the PR is merged, and every decision logged.
func (h *Handler) SweepCleanup() (int, error) {
	// The gh gate first: guessing at merge status is how a sweep deletes
	// work (06-fleet.md §7.1), so an unavailable gh stops the whole sweep.
	if err := treecheck.AuthStatus(h.gh()); err != nil {
		h.log.Warn("scheduled cleanup skipped: gh is unavailable; nothing was cleaned", "err", err)
		return 0, nil
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	uid := strconv.Itoa(os.Getuid())
	specs, err := h.st.ReadSpecs()
	if err != nil {
		return 0, err
	}
	reg, err := h.st.ReadRegistry()
	if err != nil {
		return 0, err
	}

	// The candidates are snapshotted by identity, and each is looked up
	// fresh: the loop releases the mutex per entry and the teardown
	// rewrites the registry, so an index into the slice read here would
	// not survive the first clean.
	type candidate struct{ app, slug string }
	var candidates []candidate
	for i := range reg.Entries {
		candidates = append(candidates, candidate{reg.Entries[i].App, reg.Entries[i].Slug})
	}

	cleaned := 0
	for _, c := range candidates {
		reg, err := h.st.ReadRegistry()
		if err != nil {
			return cleaned, err
		}
		e := registryEntry(reg, c.app, c.slug)
		if e == nil {
			continue // gone since the snapshot; nothing to clean
		}
		skip := func(reason string) {
			h.log.Info("scheduled cleanup skipped an entry", "app", c.app, "slug", c.slug, "reason", reason)
		}
		if e.OwnerKind != protocol.KindHost || e.Owner != uid {
			skip("owned by another client; the sweep never adopts a view")
			continue
		}
		if !e.PathVisible {
			skip("the worktree exists only inside a container; an unverifiable entry is never cleaned")
			continue
		}
		if _, serr := os.Stat(e.Path); serr != nil {
			skip("the worktree directory is gone; reconcile handles a gone directory")
			continue
		}
		sp := h.specForEntry(e, specs)
		if sp == nil {
			skip("no spec can be found for the entry; the teardown cannot compute dependent projects or purge refusals")
			continue
		}
		// The checks shell out to git and gh per entry. They run with the
		// mutex released, under the entry's claim, so a sweep over a dozen
		// worktrees does not block every client for the duration.
		ok, reason := false, ""
		if cerr := h.runUnlocked(c.app, c.slug, func() {
			ok, reason = h.sweepChecks(e, sp)
		}); cerr != nil {
			skip(cerr.Msg)
			continue
		}
		if !ok {
			skip(reason)
			continue
		}
		// The registry moved on while the checks ran; the teardown works
		// from the entry as it stands now.
		reg, err = h.st.ReadRegistry()
		if err != nil {
			return cleaned, err
		}
		e = registryEntry(reg, c.app, c.slug)
		if e == nil {
			skip("the entry went away while the checks ran")
			continue
		}
		path := e.Path
		tresp := h.teardownEntry(nil, e, sp, nil, nil)
		if tresp.Error != nil {
			h.log.Warn("scheduled cleanup teardown left resources behind", "app", c.app, "slug", c.slug, "err", tresp.Error.Msg)
			continue
		}
		cleaned++
		// The git half: `git worktree remove`, never --force — git
		// refusing is signal that a check missed something, and the sweep
		// reports it and leaves the tree alone.
		if rerr := treecheck.WorktreeRemove(path, treecheck.Git); rerr != nil {
			h.log.Warn("scheduled cleanup removed the entry but git refused to remove the worktree", "app", c.app, "slug", c.slug,
				"git", rerr)
			continue
		}
		h.log.Info("scheduled cleanup cleaned an entry", "app", c.app, "slug", c.slug, "worktree", path)
	}
	return cleaned, nil
}

// sweepChecks runs the tree-reading and merged-PR checks for one entry,
// in rm's order: uncommitted changes, unpushed commits (an absent
// upstream is itself a stop), then whether the branch's PR is merged. A
// merged PR says the branch landed and says nothing about whether the tree
// is clean — the full rm checks still apply (06-fleet.md §7.1). Returns
// whether the entry may be cleaned and the reason when it may not.
func (h *Handler) sweepChecks(e *store.Entry, sp *spec.Spec) (bool, string) {
	res, err := treecheck.Uncommitted(e.Path, treecheck.Git)
	if err != nil {
		return false, fmt.Sprintf("the uncommitted-changes check could not run: %v", err)
	}
	if !res.OK() {
		return false, fmt.Sprintf("the tree has %d uncommitted change(s), first: %s", len(res.Lines), res.First(sweepChecksMaxLines))
	}

	if _, ok := treecheck.OnBranch(e.Path, treecheck.Git); !ok {
		return false, "the worktree is not on a branch (detached HEAD)"
	}
	res, uerr := treecheck.Unpushed(e.Path, treecheck.Git)
	if uerr != nil {
		return false, fmt.Sprintf("the unpushed-commits check could not run (an absent upstream is itself a stop): %v", uerr)
	}
	if !res.OK() {
		return false, fmt.Sprintf("the branch has %d unpushed commit(s), first: %s", len(res.Lines), res.First(sweepChecksMaxLines))
	}

	pr, perr := treecheck.PRState(e.Path, h.gh())
	if perr != nil {
		return false, fmt.Sprintf("gh could not answer whether this branch's PR is merged (%v)", perr)
	}
	if !pr.Merged() {
		if pr.State == treecheck.StateNone {
			return false, pr.Describe()
		}
		return false, fmt.Sprintf("%s, not merged", pr.Describe())
	}
	return true, ""
}
