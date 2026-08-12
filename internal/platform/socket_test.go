package platform

import (
	"bufio"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
)

func TestSocketPathWTSocketOverride(t *testing.T) {
	t.Setenv("WT_SOCKET", "/tmp/custom.sock")
	got, err := SocketPath()
	if err != nil {
		t.Fatalf("SocketPath: %v", err)
	}
	if got != "/tmp/custom.sock" {
		t.Errorf("SocketPath = %q, want the WT_SOCKET override", got)
	}
}

func TestDefaultSocketPath(t *testing.T) {
	t.Setenv("WT_SOCKET", "")
	switch runtime.GOOS {
	case "darwin":
		home, err := os.UserHomeDir()
		if err != nil {
			t.Fatal(err)
		}
		got, err := DefaultSocketPath()
		if err != nil {
			t.Fatalf("DefaultSocketPath: %v", err)
		}
		want := filepath.Join(home, "Library", "Application Support", "wt", "sock")
		if got != want {
			t.Errorf("DefaultSocketPath = %q, want %q", got, want)
		}
	case "linux":
		dir := t.TempDir()
		t.Setenv("XDG_RUNTIME_DIR", dir)
		got, err := DefaultSocketPath()
		if err != nil {
			t.Fatalf("DefaultSocketPath: %v", err)
		}
		if got != filepath.Join(dir, "wt", "sock") {
			t.Errorf("DefaultSocketPath = %q, want %q", got, filepath.Join(dir, "wt", "sock"))
		}
		t.Setenv("XDG_RUNTIME_DIR", "")
		if _, err := DefaultSocketPath(); err == nil {
			t.Error("DefaultSocketPath without XDG_RUNTIME_DIR succeeded, want a refusal")
		}
	case "windows":
		got, err := DefaultSocketPath()
		if err != nil {
			t.Fatalf("DefaultSocketPath: %v", err)
		}
		if got != `\\.\pipe\wt` {
			t.Errorf("DefaultSocketPath = %q, want the named pipe", got)
		}
	}
}

// TestListenSocket is the socket-path exercise that runs on every platform:
// listen on a short path under the temp dir (unix socket paths are
// length-limited, 104 bytes on macOS), check the 0700 restriction, and
// verify a second coordinator on the same path is refused while a stale
// socket file is reclaimed.
func TestListenSocket(t *testing.T) {
	if runtime.GOOS == "windows" {
		if _, err := ListenSocket(`\\.\pipe\wt`); err == nil {
			t.Fatal("ListenSocket on Windows succeeded, want the phase-8 refusal")
		}
		return
	}
	dir := tempDir(t) // resolved: on macOS the temp root is under /var → /private/var
	sock := filepath.Join(dir, "s")
	ln, err := ListenSocket(sock)
	if err != nil {
		t.Fatalf("ListenSocket: %v", err)
	}
	defer ln.Close()
	defer os.Remove(sock)

	fi, err := os.Stat(sock)
	if err != nil {
		t.Fatalf("stat the socket: %v", err)
	}
	if fi.Mode().Perm() != 0o700 {
		t.Errorf("socket mode = %v, want 0700 (restricted to the owning user)", fi.Mode().Perm())
	}

	// A live coordinator must refuse a second listener on the same path.
	if _, err := ListenSocket(sock); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("second ListenSocket = %v, want the already-listening refusal", err)
	}
}

// TestListenSocketStaleReclaim: a socket file nothing answers is stale and
// must be reclaimed, so a crashed coordinator does not block the next one.
func TestListenSocketStaleReclaim(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix sockets on windows")
	}
	dir := tempDir(t)
	sock := filepath.Join(dir, "s")
	// Bind and close: the file remains, nothing answers it.
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	ln.Close()
	ln2, err := ListenSocket(sock)
	if err != nil {
		t.Fatalf("ListenSocket over a stale socket: %v", err)
	}
	ln2.Close()
	os.Remove(sock)
}

// TestPeerUID reports the kernel's own view of the connecting process — the
// host identity the coordinator trusts. The listener and dialer run in this
// same process, so the reported uid must be the test's own.
func TestPeerUID(t *testing.T) {
	if runtime.GOOS == "windows" {
		return // the windows file reports the phase-8 refusal; nothing to assert here
	}
	dir := tempDir(t)
	sock := filepath.Join(dir, "s")
	ln, err := net.Listen("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defer os.Remove(sock)

	got := make(chan int, 1)
	errCh := make(chan error, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			errCh <- err
			return
		}
		defer conn.Close()
		uid, err := PeerUID(conn)
		if err != nil {
			errCh <- err
			return
		}
		got <- uid
	}()
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatal(err)
	}
	conn.Close()
	select {
	case uid := <-got:
		if uid != os.Getuid() {
			t.Errorf("PeerUID = %d, want %d (the process's own uid)", uid, os.Getuid())
		}
	case err := <-errCh:
		t.Fatalf("PeerUID: %v", err)
	}
}

// TestListenSocketSpeaksHello exercises the wire end to end through the
// platform listener: a real hello message crosses a real socket and comes
// back, which is the exchange `wt daemon status`'s reachability probe uses.
func TestListenSocketSpeaksHello(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("no unix sockets on windows")
	}
	dir := tempDir(t)
	sock := filepath.Join(dir, "s")
	ln, err := ListenSocket(sock)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	defer os.Remove(sock)

	reply := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		line, err := bufio.NewReader(conn).ReadBytes('\n')
		if err != nil {
			return
		}
		var h struct {
			Kind   string `json:"kind"`
			MaxVer int    `json:"max_version"`
		}
		json.Unmarshal(line, &h)
		conn.Write([]byte(`{"agreed":` + strconv.Itoa(h.MaxVer) + `}` + "\n"))
		reply <- h.Kind
	}()

	conn, err := DialSocket(sock)
	if err != nil {
		t.Fatalf("DialSocket: %v", err)
	}
	defer conn.Close()
	conn.Write([]byte(`{"kind":"host","min_version":1,"max_version":1}` + "\n"))
	line, err := bufio.NewReader(conn).ReadBytes('\n')
	if err != nil {
		t.Fatalf("reading the reply: %v", err)
	}
	if !strings.Contains(string(line), `"agreed":1`) {
		t.Errorf("reply = %q, want an agreed version", line)
	}
	if kind := <-reply; kind != "host" {
		t.Errorf("server saw kind %q, want host", kind)
	}
}
