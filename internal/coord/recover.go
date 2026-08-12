package coord

// recover.go is phase 9's R1 answer (plan.md §5 "Phase 9", docs/ARCHITECTURE.md
// §14.1): what a coordinator restart does to an entry that was
// mid-materialisation. Protocol negotiation covers a client and coordinator
// holding different versions; it says nothing about a resident process that
// held a reserving entry and died.
//
// The rule the recovery pass implements: every reserving entry a starting
// coordinator finds is an operation the previous process died mid-way
// through — a restart (an upgrade, a crash, a reboot) makes the outcome of
// the in-flight operation unknowable, and the entry's materialised half
// (a VM the machine driver started, a state-path directory, a compose
// project a dependent run created) is real even though the entry says
// reserving. The safe direction is to roll the operation back to a known
// state rather than hope the client returns:
//
//   - the entry's resources are torn down by handle (the same teardown path
//     rm and reclamation run; the spec comes from the walk-up lookup, else
//     from the per-app cache, exactly as reclamation resolves it);
//   - a clean teardown drops the entry and frees the slot, so the next
//     `wt init` allocates fresh — recoverable;
//   - a teardown that cannot complete moves the entry to tearing-down with
//     the note, the resting state doctor, rm and reconcile already repair —
//     recoverable;
//   - an entry whose spec cannot be found at all is moved to tearing-down
//     with a note naming `wt rm` from the repository, never dropped blind
//     and never left to the ageing timer, which would drop it without
//     tearing anything down.
//
// The pass runs once at startup, before the coordinator accepts a single
// connection, so no client can observe a half-recovered entry and the
// sweeper's ageing timer never races it. The cost is stated: startup blocks
// on the teardown of interrupted allocations, which is the price of a
// restart being safe rather than merely quick.

import (
	"errors"
	"fmt"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// RecoverInterrupted resolves every reserving entry the registry holds.
// It returns how many entries it resolved (dropped, or moved to
// tearing-down — either way no longer reserving), and an error only for a
// store-level failure. Per-entry teardown failures are logged and are the
// entry's own outcome, not the pass's: the entry moved to tearing-down with
// a note, which is the recoverable resting state.
func (h *Handler) RecoverInterrupted() (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.Drivers == nil {
		return 0, errors.New("coord: restart recovery needs the driver registry")
	}
	specs, err := h.st.ReadSpecs()
	if err != nil {
		return 0, err
	}

	recovered := 0
	for {
		reg, err := h.st.ReadRegistry()
		if err != nil {
			return recovered, err
		}
		var target *store.Entry
		for i := range reg.Entries {
			if reg.Entries[i].State == store.StateReserving {
				target = &reg.Entries[i]
				break
			}
		}
		if target == nil {
			return recovered, nil
		}
		app, slug := target.App, target.Slug

		sp := h.specForEntry(target, specs)
		if sp == nil {
			// Without the spec the teardown cannot compute the dependent
			// projects or the purge refusal, so a teardown would be a partial
			// honour of a destructive operation (plan.md §3). The entry moves
			// to tearing-down with the note naming the command that supplies
			// the spec; the slot stays held — never dropped blind, and never
			// left for the ageing timer, which drops without tearing down.
			target.State = store.StateTearingDown
			target.TeardownNote = "the coordinator restarted while this entry was reserving and no spec could be found to tear it down; the slot stays held until the teardown is re-run with the spec"
			target.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)
			if err := h.st.WriteRegistry(reg); err != nil {
				return recovered, err
			}
			h.log.Info("restart recovery: reserving entry moved to tearing-down (no spec found)",
				"app", app, "slug", slug)
			recovered++
			continue
		}

		tresp := h.teardownEntry(nil, target, reg, sp, nil, nil)
		if tresp.Error != nil {
			h.log.Warn("restart recovery: teardown of an interrupted allocation left resources behind",
				"app", app, "slug", slug, "err", tresp.Error.Msg)
		} else {
			h.log.Info("restart recovery: interrupted allocation torn down and released",
				"app", app, "slug", slug)
		}
		recovered++
	}
}

// RecoveryReport renders the startup pass's outcome for the coordinator's
// log line.
func RecoveryReport(n int) string {
	switch n {
	case 0:
		return "no interrupted allocations to recover"
	case 1:
		return "recovered 1 interrupted allocation from the previous coordinator process"
	default:
		return fmt.Sprintf("recovered %d interrupted allocations from the previous coordinator process", n)
	}
}
