package coord

// ownport.go registers the coordinator's own listening port as a
// host-global band reservation.
//
// The tool allocates ports to worktrees, and one of the ports on the
// machine belongs to the tool itself. Without this, `bands suggest` could
// propose a base whose range covers the coordinator's own port, an app
// would be onboarded onto it, and the collision would surface later as a
// worktree that cannot start — or, worse, as a coordinator that cannot
// restart because something else took its port while it was down.
//
// The reservation is re-asserted at every start rather than added once:
// ReplaceReservationByNote keys on the note, so restarting does not
// accumulate rows and moving to a new --addr releases the old port instead
// of leaving it reserved with nothing behind it.

import (
	"fmt"
	"net"
	"strconv"

	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// OwnPortNote is the note the coordinator's own reservation carries. It is
// the reservation's identity, not decoration: ReplaceReservationByNote
// matches on it, so it must stay stable across releases.
const OwnPortNote = "worktree-manager coordinator"

// ReserveOwnPort records addr's port as a host-global reservation, so no
// app's band can be suggested over it and `ports scan` can name what holds
// it. Callers pass the address actually listened on, which under socket
// activation is the descriptor's address rather than any flag.
//
// A port that cannot be parsed out of addr is an error rather than a
// silent skip: the whole point is that this port is accounted for, and a
// coordinator that quietly failed to reserve its own port is the case the
// reservation exists to prevent.
func (h *Handler) ReserveOwnPort(addr string) error {
	_, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("the coordinator's own listen address %q is not host:port, so its port cannot be reserved: %w", addr, err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		return fmt.Errorf("the coordinator's own listen address %q has a non-numeric port: %w", addr, err)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st.ReplaceReservationByNote(store.Reservation{
		Ports: []int{port},
		Note:  OwnPortNote,
	})
}
