//go:build darwin || linux

package platform

import (
	"fmt"
	"net"
	"os"
	"path/filepath"
	"syscall"
)

// listenSocket creates the coordinator's unix listener at path, creating
// the parent directory, removing a stale socket file (one nothing answers)
// and restricting the socket to the owning user (docs/ARCHITECTURE.md
// §12.2).
func listenSocket(path string) (net.Listener, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, fmt.Errorf("creating the socket directory for %s: %w", path, err)
	}
	if _, err := os.Stat(path); err == nil {
		// A socket file can outlive the process that made it. If nothing
		// answers, it is stale and may be removed; if something answers,
		// a second coordinator would silently split the client load.
		if conn, derr := net.Dial("unix", path); derr == nil {
			conn.Close()
			return nil, fmt.Errorf("another coordinator is already listening at %s", path)
		}
		if err := os.Remove(path); err != nil {
			return nil, fmt.Errorf("removing the stale socket at %s: %w", path, err)
		}
	}
	ln, err := listenUnix(path)
	if err != nil {
		return nil, fmt.Errorf("listening on %s: %w", path, err)
	}
	// Socket permissions restrict it to the owning user; the coordinator is
	// the machine's most privileged component and the socket is its entire
	// attack surface (docs/ARCHITECTURE.md §12.2). listenUnix creates the
	// socket under umask 077 so it is born 0700 on ordinary filesystems; a
	// chmod after the fact fails with EINVAL on some mounts (measured: a
	// bind-mounted workspace volume that also ignores the umask), so the
	// check below confirms the born mode and only then falls back to
	// chmod. Where neither mechanism works the coordinator refuses rather
	// than running with a socket anyone on the machine can reach.
	fi, err := os.Stat(path)
	if err != nil {
		ln.Close()
		os.Remove(path)
		return nil, fmt.Errorf("checking the socket at %s: %w", path, err)
	}
	if fi.Mode().Perm() != 0o700 {
		if err := os.Chmod(path, 0o700); err != nil {
			ln.Close()
			os.Remove(path)
			return nil, fmt.Errorf("restricting the socket at %s to the owning user: %w", path, err)
		}
	}
	return ln, nil
}

// dialSocket connects to the coordinator's unix socket at path.
func dialSocket(path string) (net.Conn, error) {
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("connecting to the coordinator at %s: %w", path, err)
	}
	return conn, nil
}

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
