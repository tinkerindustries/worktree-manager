//go:build windows

package platform

import (
	"net"
)

// listenSocket creates the coordinator's named-pipe listener: the first
// instance of the pipe at path with the owner-only ACL (pipe_windows.go,
// sec_windows.go). A second listener on the same name is refused — the
// pipe analogue of the unix already-listening check.
func listenSocket(path string) (net.Listener, error) {
	return listenPipe(path)
}

// dialSocket connects to the coordinator's named pipe at path.
func dialSocket(path string) (net.Conn, error) {
	return dialPipe(path)
}
