//go:build windows

package platform

import (
	"errors"
	"net"
)

// peerUID reports that peer credentials do not exist on a Windows named
// pipe — the identity model's host row is unix-only (ARCHITECTURE.md
// §10.2). Phase 8 owns the pipe and with it the question of how a host
// client is identified there; this phase reports the absence.
func peerUID(uc *net.UnixConn) (int, error) {
	return 0, errors.New("peer credentials are unavailable on Windows (the named pipe transport is phase 8)")
}
