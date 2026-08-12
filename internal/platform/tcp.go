package platform

// tcp.go is the opt-in loopback TCP surface's configuration rails (phase 9,
// docs/ARCHITECTURE.md §4.1): the listener binds loopback only, and every
// connection must present the configured token, because peer credentials do
// not exist on a TCP connection — the whole reason the surface stays
// opt-in. The two rails are enforced at every configuration point (wtd's
// own flags and `wt daemon install`, which writes the supervisor units), so
// a unit that could never start, or a listener that could never
// authenticate, is refused at configuration time rather than at runtime.

import (
	"fmt"
	"net"
	"strings"
)

// MinTCPTokenLength is the minimum token length the TCP surface accepts.
// The token is the whole of identity on a TCP connection, and the listener
// is reachable by every local process (and, on Docker Desktop, by
// containers via the host's loopback), so it must not be brute-forceable:
// 16 characters is the smallest length at which an online guess over the
// wire is hopeless. Shorter tokens are refused at configuration time.
const MinTCPTokenLength = 16

// ValidateTCPConfig checks the pair that enables the TCP surface: the
// listen address must be a literal loopback address (the surface binds
// loopback only), and the token must be present, long enough to resist
// brute force, and free of whitespace (the Windows task XML carries it in
// an <Arguments> string, where whitespace would split it).
func ValidateTCPConfig(addr, token string) error {
	if addr == "" && token == "" {
		return nil
	}
	if addr == "" || token == "" {
		return fmt.Errorf("--tcp and --tcp-token must be given together: a TCP listener without a token would be unauthenticated, and a token without a listener configures nothing")
	}
	if err := ValidateLoopbackTCP(addr); err != nil {
		return err
	}
	if err := ValidateTCPToken(token); err != nil {
		return err
	}
	return nil
}

// ValidateLoopbackTCP refuses a listen address whose host is not a literal
// loopback address. The token-authenticated surface binds loopback only
// (docs/ARCHITECTURE.md §4.1: "Bind loopback only"); a coordinator that
// bound all interfaces would expose it to the LAN.
func ValidateLoopbackTCP(addr string) error {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return fmt.Errorf("the TCP listen address must be host:port on loopback (e.g. 127.0.0.1:7331): %v", err)
	}
	ip := net.ParseIP(host)
	if ip == nil || !ip.IsLoopback() {
		return fmt.Errorf("the TCP listen address %q is not on loopback; the token-authenticated surface binds loopback only — use 127.0.0.1:<port> or [::1]:<port>", addr)
	}
	return nil
}

// ValidateTCPToken checks the token's strength and shape.
func ValidateTCPToken(token string) error {
	if len(token) < MinTCPTokenLength {
		return fmt.Errorf("the TCP token must be at least %d characters; a shorter token is brute-forceable over the wire", MinTCPTokenLength)
	}
	if strings.ContainsAny(token, " \t\r\n") {
		return fmt.Errorf("the TCP token must not contain whitespace: it is carried in supervisor unit files and command arguments")
	}
	return nil
}
