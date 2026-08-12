package platform

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
)

// SocketPath resolves the coordinator socket path: WT_SOCKET when set, else
// the platform default (docs/ARCHITECTURE.md §8.2). WT_SOCKET overrides
// everywhere, for client and coordinator alike. The socket sits outside the
// store on purpose: a container mounts the socket alone and never sees the
// store's files.
func SocketPath() (string, error) {
	if p := os.Getenv("WT_SOCKET"); p != "" {
		return p, nil
	}
	return DefaultSocketPath()
}

// DefaultSocketPath is the platform's socket location:
//
//	macOS    ~/Library/Application Support/wt/sock
//	Linux    $XDG_RUNTIME_DIR/wt/sock
//	Windows  \\.\pipe\wt        (phase 8; this phase reports it unimplemented)
//
// Linux refuses rather than inventing a location when XDG_RUNTIME_DIR is
// unset — a hosted runner without one must set WT_SOCKET explicitly.
func DefaultSocketPath() (string, error) {
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			return "", fmt.Errorf("resolving the coordinator socket path: %w", err)
		}
		return filepath.Join(home, "Library", "Application Support", "wt", "sock"), nil
	case "linux":
		xdg := os.Getenv("XDG_RUNTIME_DIR")
		if xdg == "" {
			return "", errors.New("XDG_RUNTIME_DIR is not set; set WT_SOCKET to a socket path")
		}
		return filepath.Join(xdg, "wt", "sock"), nil
	case "windows":
		return `\\.\pipe\wt`, nil
	default:
		return "", fmt.Errorf("no socket location for %s; set WT_SOCKET", runtime.GOOS)
	}
}

// ListenSocket creates the coordinator's unix listener at path, creating the
// parent directory, removing a stale socket file (one nothing answers) and
// restricting the socket to the owning user (docs/ARCHITECTURE.md §12.2).
// The Windows named pipe is phase 8; this build reports it unimplemented.
func ListenSocket(path string) (net.Listener, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New(`the Windows named pipe transport (\\.\pipe\wt) is phase 8; this build does not listen on it`)
	}
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

// DialSocket connects to the coordinator's unix socket at path. The Windows
// named pipe is phase 8; this build reports it unimplemented.
func DialSocket(path string) (net.Conn, error) {
	if runtime.GOOS == "windows" {
		return nil, errors.New(`the Windows named pipe transport (\\.\pipe\wt) is phase 8; this build does not dial it`)
	}
	conn, err := net.Dial("unix", path)
	if err != nil {
		return nil, fmt.Errorf("connecting to the coordinator at %s: %w", path, err)
	}
	return conn, nil
}
