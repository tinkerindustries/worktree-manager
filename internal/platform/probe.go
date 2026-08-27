package platform

import (
	"context"
	"fmt"
	"net"
)

// ProbeBind attempts to bind port on loopback with the platform's probe
// socket options and returns nil when the bind succeeded, or the bind error
// otherwise. The socket is released before returning — the probe changes
// nothing and holds nothing (03-drivers.md §4.1).
//
// The caller classifies the error: an address-in-use error means the port is
// held; any other error means the probe cannot run at all.
//
// SO_REUSEADDR inverts between platforms (08-platform.md §4.1,
// ARCHITECTURE.md §12.4): on Linux and macOS it permits binding an address
// in TIME_WAIT but not one another socket is actively listening on, so
// setting it makes the probe more accurate; on Windows it permits binding an
// address another socket already holds, so setting it would make the probe
// report free while the port is in use. The option is set on unix and left
// unset on Windows; the branch lives in the build-tagged half of this
// function and no caller sees it.
func ProbeBind(port int) error {
	lc := net.ListenConfig{Control: probeControl()}
	l, err := lc.Listen(context.Background(), "tcp", fmt.Sprintf("127.0.0.1:%d", port))
	if err != nil {
		return err
	}
	return l.Close()
}

// IsAddrInUse reports whether err is a bind's "the address is already in
// use" answer — the classification ProbeBind's callers make, and the one
// that separates "the port is held" from "the probe could not run".
//
// The errno differs by platform and the difference is not cosmetic: unix
// returns EADDRINUSE, Windows returns WSAEADDRINUSE, and
// errors.Is(err, syscall.EADDRINUSE) is false for the Windows one. Every
// caller comparing against the unix errno alone read every held port on
// Windows as unprobeable, which turned a port collision into "could not be
// probed" in the port driver and made `wt daemon install` refuse instead
// of stepping to the next free port. The branch lives here, where GOOS
// branches are allowed.
func IsAddrInUse(err error) bool { return isAddrInUse(err) }
