//go:build !windows

package platform

import (
	"errors"
	"syscall"
)

// probeControl returns the ListenConfig control that sets SO_REUSEADDR on
// unix: it permits binding an address in TIME_WAIT while still refusing one
// another socket is actively listening on, which is exactly the distinction
// the port probe needs (08-platform.md §4.1).
func probeControl() func(network, address string, c syscall.RawConn) error {
	return func(network, address string, c syscall.RawConn) error {
		var serr error
		err := c.Control(func(fd uintptr) {
			serr = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
		})
		if err != nil {
			return err
		}
		return serr
	}
}

// isAddrInUse is EADDRINUSE on unix.
func isAddrInUse(err error) bool { return errors.Is(err, syscall.EADDRINUSE) }
