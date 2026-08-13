package coord

// claim.go is the rule that keeps one client's slow operation off every
// other client's back.
//
// h.mu serialises the store: load-modify-save of the registry, the band
// ledger and clients.json is coordinator-internal write work that must not
// race itself, and one writer serialises there. It is not the right lock to
// hold across a driver call. Starting a VM takes minutes, a docker teardown
// takes as long as docker takes, and the whole premise of the product is
// several worktrees running side by side — which means several clients, one
// of whom would be waiting on `wt list` behind another's VM start.
//
// So a long operation swaps the mutex for a claim on its own entry. The
// claim covers exactly what the mutex covered for that entry: no second
// operation may touch it while the drivers are running. Every other client
// reads and writes the store meanwhile.
//
// Two rules make the swap safe:
//
//   - the outcome is recorded against a registry re-read after the lock is
//     retaken, never against the copy read before it, because another
//     writer may have written in between;
//   - a second operation on a claimed entry is refused, not queued, because
//     two teardowns of one entry race on the same docker objects.

import (
	"fmt"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

// entryKey identifies one registry entry.
type entryKey struct{ app, slug string }

// runUnlocked runs fn with h.mu released and a claim held on (app, slug).
// The caller holds h.mu, and it is held again when runUnlocked returns.
//
// It returns the refusal when another operation already holds the entry;
// fn does not run in that case. Anything the caller read from the store
// before the call is stale afterwards and must be re-read.
func (h *Handler) runUnlocked(app, slug string, fn func()) *protocol.Error {
	key := entryKey{app, slug}
	if h.claims[key] {
		return &protocol.Error{
			Code: 3,
			Msg: fmt.Sprintf("another operation on %s/%s is already running (its drivers are still working)",
				app, slug),
			Remedy: "wait for it to finish, then re-run; 'wt list' shows the entry's state",
		}
	}
	if h.claims == nil {
		h.claims = map[entryKey]bool{}
	}
	h.claims[key] = true
	h.mu.Unlock()
	defer func() {
		h.mu.Lock()
		delete(h.claims, key)
	}()
	fn()
	return nil
}
