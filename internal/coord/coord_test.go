package coord

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// tempRoot is t.TempDir() with symlinks resolved (on macOS the temp root
// sits under /var, a symlink to /private/var — a raw comparison fails there
// while passing on Linux).
func tempRoot(t *testing.T) string {
	t.Helper()
	dir, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolving the temp dir: %v", err)
	}
	return dir
}

// TestHarnessFullRequest is exit criterion 4: the in-process harness runs a
// full request — hello, negotiation, one request, one response — with no
// socket, no supervisor and no container.
func TestHarnessFullRequest(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()

	sess, reply := h.Connect(protocol.KindHost, "")
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	if reply.Agreed != protocol.VersionMin {
		t.Errorf("agreed version = %d, want %d", reply.Agreed, protocol.VersionMin)
	}
	if sess.Identity.Kind != protocol.KindHost || sess.Identity.Key != "4242" {
		t.Errorf("host identity = %+v, want uid 4242", sess.Identity)
	}

	resp := h.Request(ctx, sess, "ping", nil)
	if resp.Error != nil {
		t.Fatalf("ping refused: %+v", resp.Error)
	}
	var result struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(resp.Result, &result); err != nil || !result.OK {
		t.Errorf("ping result = %s, want ok:true", resp.Result)
	}

	// The connection was observed: clients.json holds the host client with
	// the coordinator's own last-seen time.
	f, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("ReadClients: %v", err)
	}
	if len(f.Clients) != 1 {
		t.Fatalf("clients.json has %d entries, want 1", len(f.Clients))
	}
	c := f.Clients[0]
	if c.Identity != "4242" || c.Kind != protocol.KindHost || c.Ephemeral {
		t.Errorf("recorded client = %+v", c)
	}
	if _, err := time.Parse(time.RFC3339, c.LastSeen); err != nil {
		t.Errorf("last_seen %q is not an RFC3339 time", c.LastSeen)
	}
	if time.Since(mustParse(t, c.LastSeen)) > time.Minute {
		t.Errorf("last_seen %q is not recent", c.LastSeen)
	}
}

func mustParse(t *testing.T, s string) time.Time {
	t.Helper()
	ts, err := time.Parse(time.RFC3339, s)
	if err != nil {
		t.Fatalf("parsing %q: %v", s, err)
	}
	return ts
}

// TestVersionRefusalNamesUpgradeBothDirections is exit criterion 3 at the
// coordinator: a client one protocol version ahead refuses to proceed, from
// both directions, naming the upgrade.
func TestVersionRefusalNamesUpgradeBothDirections(t *testing.T) {
	t.Run("client ahead of the coordinator", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		peer := Peer{UID: 4242, Known: true}
		_, reply := h.H.Begin(peer, &protocol.Hello{Kind: protocol.KindHost, MinVer: 2, MaxVer: 2})
		if reply.Error == nil {
			t.Fatal("hello with a non-overlapping range succeeded, want a refusal")
		}
		if reply.Error.Code != 3 {
			t.Errorf("refusal code = %d, want 3 (refused)", reply.Error.Code)
		}
		if !strings.Contains(reply.Error.Msg, "upgrade the coordinator") {
			t.Errorf("refusal does not name the coordinator upgrade: %s", reply.Error.Msg)
		}
		if !strings.Contains(reply.Error.Remedy, "upgrade wtd") {
			t.Errorf("remedy does not name the install: %s", reply.Error.Remedy)
		}
	})

	t.Run("coordinator ahead of the client", func(t *testing.T) {
		h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
		h.H.ProtocolMin, h.H.ProtocolMax = 2, 2
		peer := Peer{UID: 4242, Known: true}
		_, reply := h.H.Begin(peer, &protocol.Hello{Kind: protocol.KindHost, MinVer: 1, MaxVer: 1})
		if reply.Error == nil {
			t.Fatal("hello with a non-overlapping range succeeded, want a refusal")
		}
		if !strings.Contains(reply.Error.Msg, "upgrade the client") {
			t.Errorf("refusal does not name the client upgrade: %s", reply.Error.Msg)
		}
		if !strings.Contains(reply.Error.Remedy, "upgrade wt") {
			t.Errorf("remedy does not name the install: %s", reply.Error.Remedy)
		}
	})
}

// TestHostIdentityComesFromTheKernel: the coordinator enforces identity
// itself and never trusts the caller's claim of being a host client — a
// host hello's token field is ignored, and a host claim on a connection
// with no peer credentials is refused.
func TestHostIdentityComesFromTheKernel(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	peer := Peer{UID: 1000, Known: true}
	// The hello claims host and smuggles a token; the identity must still
	// be the kernel's uid.
	sess, reply := h.H.Begin(peer, &protocol.Hello{
		Kind: protocol.KindHost, Token: "smuggled", MinVer: 1, MaxVer: 1,
	})
	if reply.Error != nil {
		t.Fatalf("hello refused: %+v", reply.Error)
	}
	if sess.Identity.Key != "1000" {
		t.Errorf("host identity = %q, want the peer uid 1000 (claim ignored)", sess.Identity.Key)
	}

	// No peer credentials: a host claim is refused rather than trusted.
	_, reply = h.H.Begin(Peer{Known: false}, &protocol.Hello{Kind: protocol.KindHost, MinVer: 1, MaxVer: 1})
	if reply.Error == nil || reply.Error.Code != 3 {
		t.Fatalf("host hello without peer credentials = %+v, want a refusal", reply.Error)
	}
}

func TestNamedClientRequiresToken(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	_, reply := h.Connect(protocol.KindNamed, "")
	if reply.Error == nil {
		t.Fatal("a named client without a token succeeded, want a refusal")
	}
	sess, reply := h.Connect(protocol.KindNamed, "tok-abc")
	if reply.Error != nil {
		t.Fatalf("named hello refused: %+v", reply.Error)
	}
	if sess.Identity.Kind != protocol.KindNamed || sess.Identity.Key != "tok-abc" {
		t.Errorf("named identity = %+v", sess.Identity)
	}
	if sess.Identity.Ephemeral {
		t.Error("a named client is not ephemeral")
	}
	f, err := h.Store.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Clients) != 1 || f.Clients[0].Identity != "tok-abc" || f.Clients[0].Kind != protocol.KindNamed {
		t.Errorf("clients.json = %+v", f.Clients)
	}
}

// TestEphemeralClientGetsASessionID: the ephemeral declaration is the one
// thing a client asserts about itself; the coordinator issues the session
// id that becomes its identity and marks its entries reclaimable.
func TestEphemeralClientGetsASessionID(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, reply := h.Connect(protocol.KindEphemeral, "")
	if reply.Error != nil {
		t.Fatalf("ephemeral hello refused: %+v", reply.Error)
	}
	if reply.SessionID == "" {
		t.Fatal("no session id issued to the ephemeral client")
	}
	if sess.Identity.Key != reply.SessionID || !sess.Identity.Ephemeral {
		t.Errorf("session identity = %+v, session id = %q", sess.Identity, reply.SessionID)
	}
	// Two ephemeral connections get distinct identities.
	sess2, _ := h.Connect(protocol.KindEphemeral, "")
	if sess2.Identity.Key == sess.Identity.Key {
		t.Errorf("two ephemeral clients share the session id %q", sess.Identity.Key)
	}
	f, err := h.Store.ReadClients()
	if err != nil {
		t.Fatal(err)
	}
	if len(f.Clients) != 2 {
		t.Fatalf("clients.json has %d entries, want 2", len(f.Clients))
	}
	for _, c := range f.Clients {
		if !c.Ephemeral || c.Kind != protocol.KindEphemeral {
			t.Errorf("ephemeral entry recorded wrong: %+v", c)
		}
	}
}

// TestUnknownVerbNamesTheUpgrade: a request this coordinator does not know
// is refused whole with the upgrade named — a newer client must not be
// partially honoured.
func TestUnknownVerbNamesTheUpgrade(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	resp := h.Request(context.Background(), sess, "future-verb", nil)
	if resp.Error == nil {
		t.Fatal("unknown verb succeeded, want a refusal")
	}
	if resp.Error.Code != 1 {
		t.Errorf("unknown verb code = %d, want 1", resp.Error.Code)
	}
	if !strings.Contains(resp.Error.Remedy, "upgrade wt") {
		t.Errorf("unknown verb remedy does not name the upgrade: %s", resp.Error.Remedy)
	}
}

// TestLastSeenIsMeasuredNotWritten: a second connection from the same
// client moves its last_seen forward to the coordinator's clock.
func TestLastSeenIsMeasuredNotWritten(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(protocol.KindHost, "")
	_ = sess
	first := mustParse(t, mustTable(t, h).Clients[0].LastSeen)
	time.Sleep(5 * time.Millisecond)
	h.Connect(protocol.KindHost, "")
	f := mustTable(t, h)
	if len(f.Clients) != 1 {
		t.Fatalf("clients.json has %d entries, want 1 (upsert, not append)", len(f.Clients))
	}
	second := mustParse(t, f.Clients[0].LastSeen)
	if !second.After(first) {
		t.Errorf("last_seen did not advance: %s then %s", first, second)
	}
}

func mustTable(t *testing.T, h *Harness) store.ClientsFile {
	t.Helper()
	f, err := h.Store.ReadClients()
	if err != nil {
		t.Fatalf("ReadClients: %v", err)
	}
	return f
}

// TestServerGracefulShutdown runs the real server — listener, accept loop,
// protocol loop — over a temp socket and pins the lifecycle: in-flight
// requests finish after cancellation, Serve returns nil, and the socket
// file is removed.
func TestServerGracefulShutdown(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx, cancel := context.WithCancel(context.Background())
	sock := filepath.Join(tempRoot(t), "s")
	srv := NewServer(h.H, nil)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, sock) }()

	// Wait for the socket to exist, then run one full exchange over it.
	deadline := time.Now().Add(5 * time.Second)
	for {
		if _, err := os.Stat(sock); err == nil {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never bound the socket")
		}
		time.Sleep(5 * time.Millisecond)
	}
	conn, err := net.Dial("unix", sock)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	bw := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	hello := &protocol.Hello{Kind: protocol.KindHost, MinVer: 1, MaxVer: 1}
	if err := protocol.WriteMessage(bw, hello); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	var reply protocol.HelloReply
	if err := protocol.ReadMessage(br, &reply); err != nil {
		t.Fatalf("hello reply: %v", err)
	}
	if reply.Error != nil || reply.Agreed != 1 {
		t.Fatalf("hello reply = %+v", reply)
	}
	// The server observed the connection through real peer credentials.
	f := mustTable(t, h)
	if len(f.Clients) != 1 || f.Clients[0].Identity != strconv.Itoa(os.Getuid()) {
		t.Errorf("server observed %+v, want the real uid %d", f.Clients, os.Getuid())
	}
	if err := protocol.WriteMessage(bw, &protocol.Request{Verb: "ping"}); err != nil {
		t.Fatal(err)
	}
	bw.Flush()
	var resp protocol.Response
	if err := protocol.ReadMessage(br, &resp); err != nil {
		t.Fatalf("ping reply: %v", err)
	}
	if resp.Error != nil || !strings.Contains(string(resp.Result), `"ok":true`) {
		t.Fatalf("ping reply = %+v", resp)
	}

	// Cancel mid-connection: the server stops accepting, the in-flight
	// exchange above has already finished, and Serve returns cleanly.
	cancel()
	conn.Close()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
	if _, err := os.Stat(sock); !os.IsNotExist(err) {
		t.Errorf("socket file survives shutdown: %v", err)
	}
}
