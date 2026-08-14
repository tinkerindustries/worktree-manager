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
	return h.resolveReserving(
		func(*store.Entry) bool { return true },
		"the coordinator restarted while this entry was reserving and no spec could be found to tear it down; the slot stays held until the teardown is re-run with the spec",
		"restart recovery")
}

// resolveReserving rolls interrupted reserving entries back to a known
// state, one at a time, and returns how many it resolved. match picks which
// reserving entries to resolve: every one for the restart pass, the
// timed-out ones for the ageing timer. noSpecNote is the entry's note when
// its spec cannot be found, and logPrefix names the caller in the log.
//
// Both callers need the same three outcomes — torn down and dropped, torn
// down partially and left tearing-down, or no spec and left tearing-down —
// because both are looking at the same thing: an allocation whose
// materialised half is real and whose only handle is the entry.
//
// The caller holds h.mu.
func (h *Handler) resolveReserving(match func(*store.Entry) bool, noSpecNote, logPrefix string) (int, error) {
	if h.Drivers == nil {
		return 0, errors.New("coord: resolving an interrupted allocation needs the driver registry")
	}
	specs, err := h.st.ReadSpecs()
	if err != nil {
		return 0, err
	}

	resolved := 0
	attempted := map[[2]string]bool{}
	for {
		reg, err := h.st.ReadRegistry()
		if err != nil {
			return resolved, err
		}
		var target *store.Entry
		for i := range reg.Entries {
			e := &reg.Entries[i]
			if e.State != store.StateReserving || !match(e) {
				continue
			}
			if attempted[[2]string{e.App, e.Slug}] {
				// The entry was resolved and is still reserving on disk, so
				// the registry write did not land. Re-reading it would spin
				// forever, and the restart pass runs before the coordinator
				// accepts a connection, where nothing could observe it.
				return resolved, fmt.Errorf("coord: %s/%s is still reserving after its teardown; the registry write is not landing", e.App, e.Slug)
			}
			target = e
			break
		}
		if target == nil {
			return resolved, nil
		}
		app, slug := target.App, target.Slug
		attempted[[2]string{app, slug}] = true

		sp := h.specForEntry(target, specs)
		if sp == nil {
			// Without the spec the teardown cannot compute the dependent
			// projects or the purge refusal, so a teardown would be a partial
			// honour of a destructive operation (plan.md §3). The entry moves
			// to tearing-down with the note naming the command that supplies
			// the spec; the slot stays held — never dropped blind. The state
			// change, the note and the last-seen move land as one
			// transaction.
			target.State = store.StateTearingDown
			target.TeardownNote = noSpecNote
			if err := h.st.WithTx(func(tx *store.Tx) error {
				if err := tx.UpdateEntryState(app, slug, store.StateTearingDown, noSpecNote); err != nil {
					return err
				}
				return tx.TouchEntry(app, slug, time.Now())
			}); err != nil {
				return resolved, err
			}
			h.log.Info(logPrefix+": reserving entry moved to tearing-down (no spec found)",
				"app", app, "slug", slug)
			resolved++
			continue
		}

		tresp := h.teardownEntry(nil, target, sp, nil, nil)
		if tresp.Error != nil {
			h.log.Warn(logPrefix+": teardown of an interrupted allocation left resources behind",
				"app", app, "slug", slug, "err", tresp.Error.Msg)
		} else {
			h.log.Info(logPrefix+": interrupted allocation torn down and released",
				"app", app, "slug", slug)
		}
		resolved++
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
