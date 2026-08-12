//go:build darwin || linux

package platform

import (
	"fmt"
	"net"
)

// peerUIDConn extracts the unix connection and asks the kernel for the
// peer's uid.
func peerUIDConn(c net.Conn) (int, error) {
	uc, ok := c.(*net.UnixConn)
	if !ok {
		return 0, fmt.Errorf("peer credentials need a unix socket connection, got %T", c)
	}
	return peerUID(uc)
}
