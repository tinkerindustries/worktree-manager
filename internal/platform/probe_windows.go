//go:build windows

package platform

import (
	"errors"
	"syscall"
)

// probeControl returns a nil control on Windows: SO_REUSEADDR permits
// binding an address another socket already holds, so setting it would make
// the port probe report free while the port is in use — the exact failure
// the probe exists to prevent (08-platform.md §4.1).
func probeControl() func(network, address string, c syscall.RawConn) error {
	return nil
}

// winWSAEADDRINUSE is Winsock's "address already in use". Go's syscall
// package does not name it on Windows, so it is spelled out here rather
// than pulled in from x/sys — the module has two runtime dependencies and
// gains none for one constant.
const winWSAEADDRINUSE = syscall.Errno(10048)

// isAddrInUse is WSAEADDRINUSE on Windows. syscall.EADDRINUSE exists there
// too, as one of the placeholder errnos Go defines for portability, and
// the Winsock stack never returns it — so it is checked as well, for a
// caller that manufactured one, but WSAEADDRINUSE is the real answer.
func isAddrInUse(err error) bool {
	return errors.Is(err, winWSAEADDRINUSE) || errors.Is(err, syscall.EADDRINUSE)
}
