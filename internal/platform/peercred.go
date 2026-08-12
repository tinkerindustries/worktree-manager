package platform

import (
	"fmt"
	"net"
)

// PeerUID returns the uid of the process at the other end of an AF_UNIX
// connection — the kernel's own report, which is what makes a host client's
// identity stable and unforgeable (ARCHITECTURE.md §10.2: the coordinator
// enforces identity itself and never trusts the caller's claim of being a
// host client).
//
// The mechanism is per-platform: SO_PEERCRED on Linux, LOCAL_PEERCRED on
// macOS (this is the one surface the design has no inventory row for —
// 08-platform.md §3 predates the coordinator model; the task brief fixes
// the mechanism). The per-OS implementation lives in peercred_*.go, this
// file only extracts the unix connection. Windows has no peer credentials
// on a pipe and reports the transport itself as phase 8.
func PeerUID(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peer credentials need a unix socket connection, got %T", c)
	}
	return peerUID(uc)
}
