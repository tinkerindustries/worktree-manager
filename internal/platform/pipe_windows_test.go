//go:build windows

package platform

// pipe_windows_test.go is what a hosted windows-latest runner can prove
// about the named pipe (plan.md §5, phase 8's exit criteria): the pipe
// carrying a full request — hello and reply, newline-delimited JSON over
// the byte-mode pipe — plus the already-listening refusal and the
// ACL-building machinery. These tests run only on the Windows CI job; on
// every other platform they are not compiled. The interactive-desktop
// surfaces (service registration, the logon task, taskkill's escalation
// reporting against a real GUI process) remain not_run.

import (
	"bufio"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

// testPipeName is a unique pipe name per test, so parallel tests and
// parallel CI runs cannot collide on \\.\pipe\.
func testPipeName(t *testing.T) string {
	t.Helper()
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	return `\\.\pipe\wt-test-` + hex.EncodeToString(b)
}

// TestPipeCarriesAHello is the transport proof: a listener, a dialer and
// one newline-delimited JSON exchange — the exact shape of the protocol's
// hello — cross the pipe in both directions. This is the phase-8 exit
// criterion "the named pipe carrying a full request" at the platform
// layer.
func TestPipeCarriesAHello(t *testing.T) {
	name := testPipeName(t)
	ln, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe: %v", err)
	}
	defer ln.Close()

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

	conn, err := dialPipe(name)
	if err != nil {
		t.Fatalf("dialPipe: %v", err)
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

// TestPipeSecondListenerRefused: FILE_FLAG_FIRST_PIPE_INSTANCE makes a
// second listener on the same name fail — the pipe analogue of the unix
// already-listening refusal, so two coordinators cannot split the client
// load.
func TestPipeSecondListenerRefused(t *testing.T) {
	name := testPipeName(t)
	ln, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe: %v", err)
	}
	defer ln.Close()
	if _, err := listenPipe(name); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("second listenPipe = %v, want the already-listening refusal", err)
	}
}

// TestPipePeerUIDReportsTheOwner: on Windows the pipe's ACL is the whole
// of the identity — a connection that exists is the owning user's, and
// PeerUID reports the sentinel with success so host identity works.
func TestPipePeerUIDReportsTheOwner(t *testing.T) {
	name := testPipeName(t)
	ln, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe: %v", err)
	}
	defer ln.Close()
	uidCh := make(chan int, 1)
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
		uidCh <- uid
	}()
	conn, err := dialPipe(name)
	if err != nil {
		t.Fatalf("dialPipe: %v", err)
	}
	conn.Close()
	select {
	case uid := <-uidCh:
		if uid != WindowsPipeOwnerUID {
			t.Errorf("PeerUID = %d, want the pipe-owner sentinel %d", uid, WindowsPipeOwnerUID)
		}
	case err := <-errCh:
		t.Fatalf("PeerUID: %v", err)
	}
}

// TestUserSID: the ACL machinery starts from the current user's SID; on a
// runner the token must resolve to a real S-1-... string.
func TestUserSID(t *testing.T) {
	sid, err := userSID()
	if err != nil {
		t.Fatalf("userSID: %v", err)
	}
	if !strings.HasPrefix(sid, "S-1-") {
		t.Errorf("userSID = %q, want an S-1-... SID", sid)
	}
}

// TestSecureSecurityAttributes: the SDDL builds into a security
// descriptor with the user's SID in it.
func TestSecureSecurityAttributes(t *testing.T) {
	sa, err := secureSecurityAttributes()
	if err != nil {
		t.Fatalf("secureSecurityAttributes: %v", err)
	}
	defer freeSecurityDescriptor(sa)
	if sa.Length == 0 || sa.SecurityDescriptor == 0 {
		t.Error("security attributes are empty")
	}
}

// TestListenSocketOnWindows is the platform-layer analogue of the unix
// socket test: ListenSocket creates the pipe, DialSocket reaches it, and
// a second listener is refused. Mode bits do not exist on a named pipe —
// the ACL is the permission model, and its building is exercised above.
func TestListenSocketOnWindows(t *testing.T) {
	name := testPipeName(t)
	ln, err := ListenSocket(name)
	if err != nil {
		t.Fatalf("ListenSocket: %v", err)
	}
	defer ln.Close()
	conn, err := DialSocket(name)
	if err != nil {
		t.Fatalf("DialSocket: %v", err)
	}
	conn.Close()
	if _, err := ListenSocket(name); err == nil || !strings.Contains(err.Error(), "already listening") {
		t.Errorf("second ListenSocket = %v, want the already-listening refusal", err)
	}
}

// TestPipeConnDeadlinesRefused: nothing in the repository sets deadlines,
// and the pipe says so rather than silently ignoring them.
func TestPipeConnDeadlinesRefused(t *testing.T) {
	name := testPipeName(t)
	ln, err := listenPipe(name)
	if err != nil {
		t.Fatalf("listenPipe: %v", err)
	}
	defer ln.Close()
	done := make(chan struct{})
	go func() {
		defer close(done)
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if err := conn.SetDeadline(time.Time{}); err == nil {
			t.Error("SetDeadline succeeded, want a refusal")
		}
	}()
	conn, err := dialPipe(name)
	if err != nil {
		t.Fatalf("dialPipe: %v", err)
	}
	conn.Close()
	<-done
}

// TestRealPathWindowsTempRoot sanity-checks the Windows path realisation
// (added with the long-path work) against the temp root.
func TestRealPathWindowsTempRoot(t *testing.T) {
	dir := t.TempDir()
	got, err := RealPath(dir)
	if err != nil {
		t.Fatalf("RealPath: %v", err)
	}
	if got == "" {
		t.Error("RealPath returned an empty path")
	}
	// The resolved form must still name the same directory on disk.
	fi, err := os.Stat(got)
	if err != nil {
		t.Fatalf("statting the resolved path %s: %v", got, err)
	}
	if !fi.IsDir() {
		t.Errorf("%s is not a directory", got)
	}
}

// TestRealPathWindowsDeepPath: paths beyond MAX_PATH resolve instead of
// failing — Go's own file operations handle them via the \\?\ prefix, and
// the coordinator's path comparisons must too (08-platform.md §4.5).
func TestRealPathWindowsDeepPath(t *testing.T) {
	base := t.TempDir()
	deep := base
	for i := 0; i < 30; i++ {
		deep = filepath.Join(deep, "segment-"+strings.Repeat("x", 10))
	}
	if err := os.MkdirAll(deep, 0o755); err != nil {
		t.Fatalf("creating a deep path: %v", err)
	}
	got, err := RealPath(deep)
	if err != nil {
		t.Fatalf("RealPath over a %d-char path: %v", len(deep), err)
	}
	if got == "" {
		t.Error("RealPath returned an empty path")
	}
}
