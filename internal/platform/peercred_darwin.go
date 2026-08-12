//go:build darwin

package platform

import (
	"fmt"
	"net"
	"syscall"
	"unsafe"
)

// The socket option constants LOCAL_PEERCRED (sys/un.h) and SOL_LOCAL
// (sys/socket.h) are not exposed by the standard library's darwin syscall
// package, so they are named here. The struct is the kernel's xucred
// (sys/ucred.h): version, real uid, group count, then XU_NGROUPS groups.
const (
	solLocal      = 0x0
	localPeerCred = 0x001
	xuNgroups     = 16

	// XUCRED_VERSION in sys/ucred.h is 0. The kernel stamps it into every
	// reply, so the check catches a future layout change rather than a
	// failed call — a call that failed reports through errno.
	xucredVersion = 0
)

type xucred struct {
	Version uint32
	UID     uint32
	Ngroups int16
	Groups  [xuNgroups]uint32
}

// peerUID reads LOCAL_PEERCRED, which the kernel fills with the real uid of
// the peer process on the same host. syscall exposes no typed helper for it,
// so the xucred struct is read with a raw getsockopt through Syscall6 inside
// RawConn.Control — the standard library's syscall package covers macOS
// without x/sys (the project's one-dependency rule).
func peerUID(uc *net.UnixConn) (int, error) {
	raw, err := uc.SyscallConn()
	if err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	var uid int
	var credErr error
	if err := raw.Control(func(fd uintptr) {
		var cred xucred
		size := uint32(unsafe.Sizeof(cred))
		_, _, errno := syscall.Syscall6(syscall.SYS_GETSOCKOPT, fd,
			uintptr(solLocal), uintptr(localPeerCred),
			uintptr(unsafe.Pointer(&cred)), uintptr(unsafe.Pointer(&size)), 0)
		if errno != 0 {
			credErr = errno
			return
		}
		if cred.Version != xucredVersion {
			credErr = fmt.Errorf("unexpected xucred version %d, want %d", cred.Version, xucredVersion)
			return
		}
		uid = int(cred.UID)
	}); err != nil {
		return 0, fmt.Errorf("peer credentials: %w", err)
	}
	if credErr != nil {
		return 0, fmt.Errorf("peer credentials: %w", credErr)
	}
	return uid, nil
}
