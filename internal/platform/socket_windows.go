//go:build windows

package platform

import (
	"errors"
	"net"
)

// listenUnix reports the phase-8 refusal: the Windows transport is the
// named pipe, not a unix socket.
func listenUnix(path string) (net.Listener, error) {
	return nil, errors.New(`the Windows named pipe transport (\\.\pipe\wt) is phase 8; this build does not listen on it`)
}
