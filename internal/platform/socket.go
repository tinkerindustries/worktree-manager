package platform

import (
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strings"
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
//	Windows  \\.\pipe\wt
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

// ListenSocket creates the coordinator's listener at path: a unix socket
// restricted to the owning user on macOS and Linux, the named pipe
// \\.\pipe\wt with an owner-only ACL on Windows (08-platform.md §3, the
// hosting table of docs/ARCHITECTURE.md §4.1). The per-OS implementation
// lives in socket_unix.go and socket_windows.go.
func ListenSocket(path string) (net.Listener, error) {
	return listenSocket(path)
}

// DialSocket connects to the coordinator: the unix socket or the named
// pipe for a plain path, and the opt-in loopback TCP surface when WT_SOCKET
// (or the caller's dial path) carries the tcp:// form — `tcp://host:port`
// names the coordinator's location the same way a socket path does, which
// is the phase-9 transport for hosts where a socket cannot be shared into
// a container (Docker Desktop's virtiofs cannot carry a live unix socket;
// docs/ARCHITECTURE.md §4.1). The TCP surface requires the token the
// coordinator was configured with; the client presents it through
// WT_CLIENT_TOKEN exactly as it does over the socket — one identity path,
// not a second one.
func DialSocket(path string) (net.Conn, error) {
	if addr, ok := strings.CutPrefix(path, "tcp://"); ok {
		conn, err := net.Dial("tcp", addr)
		if err != nil {
			return nil, fmt.Errorf("connecting to the coordinator over TCP at %s: %w", addr, err)
		}
		return conn, nil
	}
	return dialSocket(path)
}
