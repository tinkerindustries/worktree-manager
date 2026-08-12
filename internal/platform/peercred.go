package platform

import (
	"net"
)

// PeerUID returns the identity of the process at the other end of a
// connection — on unix, the kernel's own report of the peer's uid, which
// is what makes a host client's identity stable and unforgeable
// (ARCHITECTURE.md §10.2: the coordinator enforces identity itself and
// never trusts the caller's claim of being a host client).
//
// On Windows there are no peer credentials on a named pipe. The pipe's
// ACL — set at creation, granting the current user and denying everyone
// else (sec_windows.go) — is the whole of the identity: every connection
// that got through the ACL is the owning user, and PeerUID reports that
// as WindowsPipeOwnerUID with success, so the coordinator's host identity
// works there too (peercred_windows.go).
//
// The mechanism is per-platform: SO_PEERCRED on Linux, LOCAL_PEERCRED on
// macOS (this is the one surface the design has no inventory row for —
// 08-platform.md §3 predates the coordinator model; the phase 2 brief
// fixed the mechanism).
func PeerUID(c net.Conn) (int, error) {
	return peerUIDConn(c)
}
