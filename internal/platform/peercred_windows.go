//go:build windows

package platform

import (
	"net"
)

// WindowsPipeOwnerUID is the identity PeerUID reports for every host
// connection on Windows. It is not a uid — Windows has no unix uids. It
// is the statement that the pipe's ACL (sec_windows.go) admitted this
// connection, and the ACL admits exactly the owning user, so every host
// connection on Windows IS the owner. The coordinator keys host identity
// on this value, which makes "the store owner" the identity of every host
// client — the honest answer to "what identifies a host client on a
// named pipe": the pipe's ACL restricts it to the owning user, and that
// is the whole of the identity (ARCHITECTURE.md §10.2's host row,
// resolved for Windows in phase 8).
const WindowsPipeOwnerUID = 0

// peerUIDConn reports the pipe owner on Windows: the connection exists,
// therefore the ACL admitted it, therefore it is the owner's.
func peerUIDConn(c net.Conn) (int, error) {
	return WindowsPipeOwnerUID, nil
}
