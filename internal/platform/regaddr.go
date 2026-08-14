package platform

// regaddr.go finds a free port for a supervisor registration. `wt daemon
// install` pins a concrete address into the unit rather than leaving the
// unit to discover a clash when the supervisor first starts it, because a
// registration naming a port it can never take is a coordinator that never
// starts and says nothing about why.
//
// The case this exists for is two users on one machine. Each runs their own
// coordinator — the design is one wtd per user — and the second cannot bind
// the first's port. Before this, the second user had to notice the clash
// and pass --addr by hand.

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"syscall"
)

// registrationProbeSpan is how many consecutive ports from the default are
// tried before giving up. A machine with sixteen coordinators on
// consecutive ports is not a case worth searching further for; it is a case
// worth reporting.
const registrationProbeSpan = 16

// ChooseRegistrationAddr returns the address a registration should pin.
// It prefers defaultAddr and falls back to the next free port above it,
// reporting chosen=true when it had to move so the caller can say so — a
// port picked for the operator rather than by them is the one they most
// need told, because nothing else on the machine reveals it.
//
// The probe is ProbeBind, the same one the port driver uses, so this agrees
// with the rest of the system about what "in use" means on each platform
// (SO_REUSEADDR inverts between unix and Windows; see probe.go).
//
// A free port here is a port free now: nothing reserves it between this
// check and the coordinator's start. That race is accepted deliberately —
// the alternative is holding a listener open across the install, and the
// failure it would prevent is both unlikely and already reported clearly by
// wtd's own port-in-use refusal.
func ChooseRegistrationAddr(defaultAddr string) (addr string, chosen bool, err error) {
	host, portStr, err := net.SplitHostPort(defaultAddr)
	if err != nil {
		return "", false, fmt.Errorf("the default coordinator address %q is not host:port: %w", defaultAddr, err)
	}
	base, err := strconv.Atoi(portStr)
	if err != nil {
		return "", false, fmt.Errorf("the default coordinator address %q has a non-numeric port: %w", defaultAddr, err)
	}
	for p := base; p < base+registrationProbeSpan; p++ {
		berr := ProbeBind(p)
		if berr == nil {
			return net.JoinHostPort(host, strconv.Itoa(p)), p != base, nil
		}
		if !errors.Is(berr, syscall.EADDRINUSE) {
			// The probe itself could not run — a sandbox with no network,
			// a permissions problem. That is not "the port is taken", and
			// reporting it as such would send the operator hunting for a
			// process that does not exist.
			return "", false, fmt.Errorf("cannot probe %s for a free coordinator port: %w", net.JoinHostPort(host, strconv.Itoa(p)), berr)
		}
	}
	return "", false, fmt.Errorf(
		"no free port for the coordinator between %d and %d on %s — every one is in use",
		base, base+registrationProbeSpan-1, host)
}
