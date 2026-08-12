//go:build darwin || linux

package platform

import (
	"net"
	"syscall"
)

// listenUnix creates the coordinator's unix listener under umask 077, so
// the socket file is born 0700 — restricted to the owning user without
// depending on a post-creation chmod (which fails with EINVAL on some
// filesystems). wtd is single-threaded at this point, so the process-wide
// umask flip is safe.
func listenUnix(path string) (net.Listener, error) {
	old := syscall.Umask(0o077)
	ln, err := net.Listen("unix", path)
	syscall.Umask(old)
	return ln, err
}
