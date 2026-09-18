package coord

// rm.go is the coordinator half of `wt rm` (ARCHITECTURE.md §9.2,
// 04-lifecycle.md §7): the reap, then the driver teardowns, then the entry
// drop. Order is teardown-first-then-`git worktree remove` — the git half
// runs in the client, after the entry is gone; here the entry, the reap and
// the teardown all run against the registry, which the removal would
// orphan.
//
// The entry lookup is a data outcome rather than an error: rm has paths for
// each partial state (04-lifecycle.md §7.3), and "directory present with no
// entry" is still a `git worktree remove` the client runs. Ownership is
// still enforced — a foreign entry is refused like every mutating call.
//
// --abandon (item 5) is a fourth path, checked before the reap and before
// the driver registry is required: it drops the entry directly, runs no
// driver and signals nothing. It exists for an entry a removed spec
// resource has stranded — TeardownAll cannot interpret a handle without its
// spec row, so it survives every ordinary teardown and holds its slot with
// no committed way to free it — and its notes always name exactly what was
// left behind, because dropping a record of real, still-running resources
// without ever saying so would be worse than the entry it replaces.

import (
	"encoding/json"
	"fmt"
	"maps"
	"slices"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// rm implements the rm verb: reap, then teardown, then the entry drop. A
// dry-run previews the reap and names the resources the teardown would
// touch, changing nothing.
func (h *Handler) rm(s *Session, req *api.Request) *api.Response {
	var args api.RmArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed rm request: %v", err), "upgrade wt: this coordinator expects an app, slug and spec")
	}
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the teardown is refused whole: %v", err),
			"fix the spec, then re-run")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	e, ok, err := h.st.GetEntry(args.App, args.Slug)
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	if !ok {
		// Not an error: the client's no-entry paths (04-lifecycle.md §7.3)
		// decide what the git half does.
		return &api.Response{Result: mustJSON(api.RmResult{EntryFound: false})}
	}
	if perr := h.checkOwner(s, e); perr != nil {
		return &api.Response{Error: perr}
	}

	if args.Abandon {
		// --abandon exists for exactly the entry no driver's teardown can
		// resolve any more — a resource removed from the spec after the
		// entry was allocated (item 5) — so it runs neither the reaper nor
		// any driver: there is nothing here it could safely signal or tear
		// down, only a registry row to drop. Ownership was already
		// checked above; nothing else about this entry needs to be true
		// for the drop itself to be safe, which is what lets it recover an
		// entry a driver registry failure (h.Drivers == nil) would
		// otherwise also strand.
		resources := slices.Sorted(maps.Keys(e.Resources))
		list := strings.Join(resources, ", ")
		if list == "" {
			list = "(none)"
		}
		if args.DryRun {
			return &api.Response{Result: mustJSON(api.RmResult{
				EntryFound: true, Path: e.Path, Resources: resources,
				Notes: []string{fmt.Sprintf(
					"--abandon: would drop the registry entry and free the slot without tearing down %d resource(s), left for hand cleanup: %s",
					len(resources), list)},
			})}
		}
		if err := h.st.DeleteEntry(args.App, args.Slug); err != nil {
			return h.storeErr("writing the registry", err)
		}
		return &api.Response{Result: mustJSON(api.RmResult{
			EntryFound: true, Path: e.Path, Resources: resources, Removed: true,
			Notes: []string{fmt.Sprintf(
				"--abandon: dropped the registry entry and freed the slot without tearing down %d resource(s), left for hand cleanup: %s",
				len(resources), list)},
		})}
	}

	// The drivers are needed only for the teardown, so the no-entry data
	// outcome above does not depend on the registry being installed.
	if h.Drivers == nil {
		return respErr(1, "the coordinator has no driver registry installed",
			"restart wtd, then re-run")
	}

	// The reaper discovers listeners and waits out the grace period, which
	// is seconds of wall-clock; it runs with the mutex released under the
	// entry's claim. It reads the band ledger first, which is store work.
	var reap api.ReapReport
	if cerr := h.runUnlocked(args.App, args.Slug, func() {
		reap = h.reap(e, &args.Spec, args.KeepProcesses, args.DryRun)
	}); cerr != nil {
		return &api.Response{Error: cerr}
	}

	if args.DryRun {
		// Preview: the reap lists what it would signal (nothing is
		// signalled under --dry-run), and the resources name what the
		// teardown would touch.
		return &api.Response{Result: mustJSON(api.RmResult{
			EntryFound: true,
			Reap:       reap,
			Path:       e.Path,
			Resources:  slices.Sorted(maps.Keys(e.Resources)),
		})}
	}

	// The real teardown: entry drop on a clean teardown, tearing-down with
	// the note and the slot held otherwise (B2.3).
	tresp := h.teardownEntry(s, e, &args.Spec, args.PurgeFlags, args.KeepFlags)
	if tresp.Error != nil {
		// The error carries the teardown's own exit code; the reap report
		// is folded into the message so the bounded-coverage statement
		// survives the error path.
		msg := tresp.Error.Msg
		if reap.Note != "" {
			msg = msg + "; reap: " + reap.Note
		}
		if n := len(reap.Signalled) + len(reap.Holders); n > 0 {
			msg = msg + fmt.Sprintf("; reap: %d process(es) signalled, %d reported", len(reap.Signalled), len(reap.Holders))
		}
		return &api.Response{Error: &api.Error{
			Code: tresp.Error.Code, Msg: msg, Remedy: tresp.Error.Remedy,
		}}
	}
	var release api.ReleaseResult
	if err := json.Unmarshal(tresp.Result, &release); err != nil {
		return respErr(1, fmt.Sprintf("decoding the teardown result: %v", err), "re-run the rm")
	}
	return &api.Response{Result: mustJSON(api.RmResult{
		EntryFound: true,
		Reap:       reap,
		Path:       e.Path,
		Resources:  slices.Sorted(maps.Keys(e.Resources)),
		Removed:    release.Removed,
		Notes:      release.Notes,
	})}
}
