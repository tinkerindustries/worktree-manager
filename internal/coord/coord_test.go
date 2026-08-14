package coord

import (
	"context"
	"encoding/json"
	"net/http"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
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
// full request — identity resolution, one request, one response — with no
// listener, no supervisor and no container.
func TestHarnessFullRequest(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx := context.Background()

	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("connect refused: %+v", err)
	}
	if sess.Version != api.VersionMax {
		t.Errorf("session version = %d, want %d", sess.Version, api.VersionMax)
	}
	if sess.Identity.Kind != api.KindHost || sess.Identity.Key != "4242" {
		t.Errorf("host identity = %+v, want the synthetic host key 4242", sess.Identity)
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

	// The connection was observed: the client table holds the host client
	// with the coordinator's own last-seen time.
	f, rerr := h.Store.ReadClients()
	if rerr != nil {
		t.Fatalf("ReadClients: %v", rerr)
	}
	if len(f.Clients) != 1 {
		t.Fatalf("clients.json has %d entries, want 1", len(f.Clients))
	}
	c := f.Clients[0]
	if c.Identity != "4242" || c.Kind != api.KindHost || c.Ephemeral {
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

// TestVersionRefusalNamesUpgradeBothDirections is exit criterion 3: a
// client one API version ahead refuses to proceed, from both directions,
// naming the upgrade. The check lives in the api package (the client runs
// it against GET /version); this pins the coordinator's half — the range
// it advertises and the refusal the client would produce.
func TestVersionRefusalNamesUpgradeBothDirections(t *testing.T) {
	t.Run("client ahead of the coordinator", func(t *testing.T) {
		verr := api.CheckVersion(2, 2, api.VersionMin, api.VersionMax)
		if verr == nil {
			t.Fatal("non-overlapping ranges agreed, want a refusal")
		}
		if verr.Code != 3 {
			t.Errorf("refusal code = %d, want 3 (refused)", verr.Code)
		}
		if !strings.Contains(verr.Msg, "upgrade the coordinator") {
			t.Errorf("refusal does not name the coordinator upgrade: %s", verr.Msg)
		}
		if !strings.Contains(verr.Remedy, "upgrade wtd") {
			t.Errorf("remedy does not name the install: %s", verr.Remedy)
		}
	})

	t.Run("coordinator ahead of the client", func(t *testing.T) {
		verr := api.CheckVersion(api.VersionMin, api.VersionMax, 2, 2)
		if verr == nil {
			t.Fatal("non-overlapping ranges agreed, want a refusal")
		}
		if !strings.Contains(verr.Msg, "upgrade the client") {
			t.Errorf("refusal does not name the client upgrade: %s", verr.Msg)
		}
		if !strings.Contains(verr.Remedy, "upgrade wt") {
			t.Errorf("remedy does not name the install: %s", verr.Remedy)
		}
	})
}

func TestNamedClientRequiresToken(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	_, err := h.Connect(api.KindNamed, "")
	if err == nil {
		t.Fatal("a named client without a token succeeded, want a refusal")
	}
	sess, err := h.Connect(api.KindNamed, "tok-abc")
	if err != nil {
		t.Fatalf("named connect refused: %+v", err)
	}
	if sess.Identity.Kind != api.KindNamed || sess.Identity.Key != "tok-abc" {
		t.Errorf("named identity = %+v", sess.Identity)
	}
	if sess.Identity.Ephemeral {
		t.Error("a named client is not ephemeral")
	}
	f, rerr := h.Store.ReadClients()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(f.Clients) != 1 || f.Clients[0].Identity != "tok-abc" || f.Clients[0].Kind != api.KindNamed {
		t.Errorf("clients.json = %+v", f.Clients)
	}
}

// TestEphemeralClientGetsASessionID: the ephemeral declaration is the one
// thing a client asserts about itself; the coordinator issues the session
// id that becomes its identity and marks its entries reclaimable.
func TestEphemeralClientGetsASessionID(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, err := h.Connect(api.KindEphemeral, "")
	if err != nil {
		t.Fatalf("ephemeral connect refused: %+v", err)
	}
	if sess.Identity.Key == "" {
		t.Fatal("no session id issued to the ephemeral client")
	}
	if !sess.Identity.Ephemeral {
		t.Errorf("session identity = %+v, session id = %q", sess.Identity, sess.Identity.Key)
	}
	// Two ephemeral connections get distinct identities.
	sess2, _ := h.Connect(api.KindEphemeral, "")
	if sess2.Identity.Key == sess.Identity.Key {
		t.Errorf("two ephemeral clients share the session id %q", sess.Identity.Key)
	}
	f, rerr := h.Store.ReadClients()
	if rerr != nil {
		t.Fatal(rerr)
	}
	if len(f.Clients) != 2 {
		t.Fatalf("clients.json has %d entries, want 2", len(f.Clients))
	}
	for _, c := range f.Clients {
		if !c.Ephemeral || c.Kind != api.KindEphemeral {
			t.Errorf("ephemeral entry recorded wrong: %+v", c)
		}
	}
}

// TestUnknownVerbNamesTheUpgrade: a request this coordinator does not know
// is refused whole with the upgrade named — a newer client must not be
// partially honoured.
func TestUnknownVerbNamesTheUpgrade(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	sess, _ := h.Connect(api.KindHost, "")
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
	sess, _ := h.Connect(api.KindHost, "")
	_ = sess
	first := mustParse(t, mustTable(t, h).Clients[0].LastSeen)
	time.Sleep(5 * time.Millisecond)
	h.Connect(api.KindHost, "")
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
	f, rerr := h.Store.ReadClients()
	if rerr != nil {
		t.Fatalf("ReadClients: %v", rerr)
	}
	return f
}

// TestServerGracefulShutdown runs the real server — listener, HTTP loop —
// over a loopback address and pins the lifecycle: requests are served,
// cancellation shuts the server down gracefully, and Serve returns nil.
func TestServerGracefulShutdown(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ctx, cancel := context.WithCancel(context.Background())
	srv := NewServer(h.H, nil)
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, "127.0.0.1:0") }()

	// The endpoint file appears once the listener is bound; its base_url
	// is the server's actual address.
	deadline := time.Now().Add(5 * time.Second)
	epPath := api.EndpointPath(h.Store.Root())
	var base string
	for {
		ep, err := api.ReadEndpoint(epPath)
		if err == nil {
			base = ep.BaseURL
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("server never wrote the endpoint file")
		}
		time.Sleep(5 * time.Millisecond)
	}

	// One full exchange over HTTP: /version answers, and a request with
	// the host token and X-Wt-Client reaches the handler.
	client := &http.Client{Transport: &http.Transport{Proxy: nil}}
	verReq, err := http.NewRequest("GET", base+api.VersionPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	verReq.Header.Set("X-Wt-Client", "1")
	verResp, err := client.Do(verReq)
	if err != nil {
		t.Fatalf("GET /version: %v", err)
	}
	var info api.VersionInfo
	if err := json.NewDecoder(verResp.Body).Decode(&info); err != nil {
		t.Fatalf("decoding /version: %v", err)
	}
	verResp.Body.Close()
	if info.Min != api.VersionMin || info.Max != api.VersionMax {
		t.Errorf("/version = %+v", info)
	}

	pingPath, _ := api.PathForVerb(api.VerbPing)
	pingReq, err := http.NewRequest("GET", base+pingPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	pingReq.Header.Set("X-Wt-Client", "1")
	pingReq.Header.Set("Authorization", "Bearer "+mustToken(t, epPath))
	pingResp, err := client.Do(pingReq)
	if err != nil {
		t.Fatalf("ping: %v", err)
	}
	var resp api.Response
	if err := json.NewDecoder(pingResp.Body).Decode(&resp); err != nil {
		t.Fatalf("decoding ping: %v", err)
	}
	pingResp.Body.Close()
	if resp.Error != nil || !strings.Contains(string(resp.Result), `"ok":true`) {
		t.Fatalf("ping reply = %+v", resp)
	}

	// The server observed the request with the host identity.
	f := mustTable(t, h)
	if len(f.Clients) != 1 || f.Clients[0].Identity != hostIdentityKey {
		t.Errorf("server observed %+v, want the host identity", f.Clients)
	}

	// Cancel: in-flight requests finish and Serve returns cleanly.
	cancel()
	if err := <-done; err != nil {
		t.Fatalf("Serve returned %v, want nil", err)
	}
}

// mustToken reads the endpoint token for the HTTP-layer assertions.
func mustToken(t *testing.T, path string) string {
	t.Helper()
	ep, err := api.ReadEndpoint(path)
	if err != nil {
		t.Fatalf("reading the endpoint file: %v", err)
	}
	return ep.Token
}
