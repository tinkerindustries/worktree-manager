package platform

// listen.go is the coordinator's listen-address and container-token rails,
// enforced at every configuration point — wtd's own flags and
// `wt daemon install`, which writes the supervisor units — so a unit that
// could never start, or a token that could never authenticate, is refused
// at configuration time rather than at the coordinator's first start.
//
// Phase R1 made HTTP on a loopback port the only surface, so the two
// settings are no longer one decision. Before R1 a TCP listener and its
// token were opt-in together and validated as a pair; now the address is
// always configured (it has a default) and the token is separately
// optional — absent it, the coordinator admits host clients only. Holding
// them to the old both-or-neither rule made a custom --addr impossible
// without also inventing a container token, which is why they validate
// independently here.

import (
	"fmt"
	"net"
	"strings"
)

// MinContainerTokenLength is the minimum length of the token that admits
// container clients. The token is the whole of a container's identity —
// there are no peer credentials over TCP — and the listener is reachable
// by every local process, so it must not be brute-forceable. Sixteen
// characters is the smallest length at which an online guess over the wire
// is hopeless. Shorter tokens are refused at configuration time.
const MinContainerTokenLength = 16

// ValidateListenAddr checks a coordinator listen address. An empty address
// is valid: the caller falls back to the default. A non-loopback address is
// refused unless allowRemote is set, because the coordinator performs
// privileged operations on its clients' behalf and a LAN-reachable
// coordinator hands that reach to the network.
//
// "localhost" is accepted when every address it resolves to is loopback,
// which is the case that a literal-IP-only check would wrongly refuse.
func ValidateListenAddr(addr string, allowRemote bool) error {
	if addr == "" {
		return nil
	}
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("the listen address must be host:port (e.g. 127.0.0.1:7833): %v", err)
	}
	if allowRemote {
		return nil
	}
	if host == "" {
		return fmt.Errorf("the listen address %q has no host, which binds every interface rather than loopback; use 127.0.0.1:<port>, or pass --allow-remote deliberately", addr)
	}
	if ip := net.ParseIP(host); ip != nil {
		if ip.IsLoopback() {
			return nil
		}
		return remoteBindRefusal(addr)
	}
	// A name rather than a literal. Accept it only when everything it
	// resolves to is loopback, so "localhost" works and a name that also
	// answers with a routable address does not slip through.
	ips, rerr := net.LookupIP(host)
	if rerr != nil {
		return fmt.Errorf("the listen address %q cannot be resolved: %v; use a literal address such as 127.0.0.1:<port>", addr, rerr)
	}
	for _, ip := range ips {
		if !ip.IsLoopback() {
			return remoteBindRefusal(addr)
		}
	}
	return nil
}

func remoteBindRefusal(addr string) error {
	return fmt.Errorf("the listen address %q is not on loopback; the coordinator performs privileged operations and binds loopback only — use 127.0.0.1:<port>, or pass --allow-remote deliberately", addr)
}

// ValidateContainerToken checks the container token's strength and shape.
// An empty token is valid and means containers are not admitted at all.
func ValidateContainerToken(token string) error {
	if token == "" {
		return nil
	}
	if len(token) < MinContainerTokenLength {
		return fmt.Errorf("the container token must be at least %d characters; a shorter token is brute-forceable over the wire", MinContainerTokenLength)
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("the container token must not contain whitespace: it is carried in supervisor unit files and command arguments")
	}
	return nil
}

// ValidateCoordinatorConfig validates the pair a registration carries, each
// on its own terms. It is the one check both wtd's flag parsing and
// `wt daemon install` run, so the two cannot disagree about what a valid
// configuration is.
func ValidateCoordinatorConfig(addr, token string, allowRemote bool) error {
	if err := ValidateListenAddr(addr, allowRemote); err != nil {
		return err
	}
	return ValidateContainerToken(token)
}
