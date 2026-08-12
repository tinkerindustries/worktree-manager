//go:build linux

package platform

import (
	"fmt"
	"net"
	"syscall"
)

// peerUID reads SO_PEERCRED, which the kernel fills with the real uid of
// the peer process. syscall.GetsockoptUcred is the standard library's typed
// helper — no x/sys dependency (the project's one-dependency rule covers
// peer credentials too).
func peerUID(uc *net.UnixConn) (int, error) {
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	var uid int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		cred, gerr := syscall.GetsockoptUcred(int(fd), syscall.SOL_SOCKET, syscall.SO_PEERCRED)
		if gerr != nil {
			credErr = gerr
			return
		}
		uid = int(cred.Uid)
	}); err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("peer credentials: %w", credErr)
	}
	return uid, nil
}
