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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// SweepIntervalDefault is how often the coordinator's own cleanup sweep
// runs: the 06-fleet.md §7.2 posture of the original hourly launchd agent,
// throttled on the one-minute sweeper the sweep hangs off.
const SweepIntervalDefault = time.Hour

// sweepChecksMaxLines caps how many changed/unpushed lines a skip reason
// names; the log line stays readable.
const sweepChecksMaxLines = 3

// gh returns the handler's gh seam, defaulting to the real runner.
func (h *Handler) gh() func(dir string, args ...string) ([]byte, error) {
	if h.Gh != nil {
		return h.Gh
	}
	return ghRun
}

// ghRun is the real gh runner: the binary must exist, and a missing one is
// an error the sweep maps to "clean nothing".
func ghRun(dir string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return nil, fmt.Errorf("gh is not installed or not on PATH")
	}
	cmd := exec.Command("gh", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	return cmd.CombinedOutput()
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
	if _, err := h.gh()("", "auth", "status"); err != nil {
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

	cleaned := 0
	for i := range reg.Entries {
		e := &reg.Entries[i]
		if e.OwnerKind != protocol.KindHost || e.Owner != uid {
			h.log.Info("scheduled cleanup skipped an entry", "app", e.App, "slug", e.Slug,
				"reason", "owned by another client; the sweep never adopts a view")
			continue
		}
		if !e.PathVisible {
			h.log.Info("scheduled cleanup skipped an entry", "app", e.App, "slug", e.Slug,
				"reason", "the worktree exists only inside a container; an unverifiable entry is never cleaned")
			continue
		}
		if _, serr := os.Stat(e.Path); serr != nil {
			h.log.Info("scheduled cleanup skipped an entry", "app", e.App, "slug", e.Slug,
				"reason", "the worktree directory is gone; reconcile handles a gone directory")
			continue
		}
		sp := h.specForEntry(e, specs)
		if sp == nil {
			h.log.Info("scheduled cleanup skipped an entry", "app", e.App, "slug", e.Slug,
				"reason", "no spec can be found for the entry; the teardown cannot compute dependent projects or purge refusals")
			continue
		}
		if ok, reason := h.sweepChecks(e, sp); !ok {
			h.log.Info("scheduled cleanup skipped an entry", "app", e.App, "slug", e.Slug, "reason", reason)
			continue
		}
		tresp := h.teardownEntry(nil, e, reg, sp, nil, nil)
		if tresp.Error != nil {
			h.log.Warn("scheduled cleanup teardown left resources behind", "app", e.App, "slug", e.Slug, "err", tresp.Error.Msg)
			continue
		}
		// The git half: `git worktree remove`, never --force — git
		// refusing is signal that a check missed something, and the sweep
		// reports it and leaves the tree alone.
		if out, rerr := exec.Command("git", "-C", e.Path, "worktree", "remove", e.Path).CombinedOutput(); rerr != nil {
			h.log.Warn("scheduled cleanup removed the entry but git refused to remove the worktree", "app", e.App, "slug", e.Slug,
				"git", strings.TrimSpace(string(out)))
			cleaned++
			continue
		}
		cleaned++
		h.log.Info("scheduled cleanup cleaned an entry", "app", e.App, "slug", e.Slug, "worktree", e.Path)
		// teardownEntry wrote the registry with this entry gone; reload so
		// the next entry's handle is fresh.
		reg, err = h.st.ReadRegistry()
		if err != nil {
			return cleaned, err
		}
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
	out, err := exec.Command("git", "-C", e.Path, "status", "--porcelain").Output()
	if err != nil {
		return false, fmt.Sprintf("the uncommitted-changes check could not run: %v", err)
	}
	if lines := nonEmptyLines(string(out)); len(lines) > 0 {
		return false, fmt.Sprintf("the tree has %d uncommitted change(s), first: %s", len(lines), firstLines(lines))
	}

	branch, berr := exec.Command("git", "-C", e.Path, "rev-parse", "--abbrev-ref", "HEAD").Output()
	if berr != nil || strings.TrimSpace(string(branch)) == "HEAD" {
		return false, "the worktree is not on a branch (detached HEAD)"
	}
	out, uerr := exec.Command("git", "-C", e.Path, "log", "@{u}..HEAD", "--oneline").Output()
	if uerr != nil {
		return false, fmt.Sprintf("the unpushed-commits check could not run (an absent upstream is itself a stop): %v", uerr)
	}
	if lines := nonEmptyLines(string(out)); len(lines) > 0 {
		return false, fmt.Sprintf("the branch has %d unpushed commit(s), first: %s", len(lines), firstLines(lines))
	}

	merged, detail := h.sweepMergedPR(e.Path)
	if !merged {
		return false, detail
	}
	return true, ""
}

// sweepMergedPR asks gh whether the branch's PR is merged, in the
// worktree. When gh cannot answer, the branch is skipped — never guessed
// at.
func (h *Handler) sweepMergedPR(root string) (bool, string) {
	out, err := h.gh()(root, "pr", "view", "--json", "state,number")
	if err == nil {
		var pr struct {
			Number int    `json:"number"`
			State  string `json:"state"`
		}
		if jerr := json.Unmarshal(out, &pr); jerr == nil && pr.Number > 0 {
			if pr.State == "MERGED" {
				return true, ""
			}
			return false, fmt.Sprintf("PR #%d is %s, not merged", pr.Number, pr.State)
		}
	}
	if strings.Contains(strings.ToLower(string(out)), "no pull requests found") {
		return false, "no pull request for this branch"
	}
	return false, fmt.Sprintf("gh could not answer whether this branch's PR is merged (%s)", strings.TrimSpace(string(out)))
}

// nonEmptyLines splits a command's output into its non-empty lines.
func nonEmptyLines(out string) []string {
	var lines []string
	for _, l := range strings.Split(strings.TrimSpace(out), "\n") {
		if l != "" {
			lines = append(lines, l)
		}
	}
	return lines
}

// firstLines renders the first few lines of a list, for a log line.
func firstLines(lines []string) string {
	shown := lines
	if len(shown) > sweepChecksMaxLines {
		shown = shown[:sweepChecksMaxLines]
	}
	return strings.Join(shown, "; ")
}
