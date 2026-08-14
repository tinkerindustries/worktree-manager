package cli

// http_test.go is the client half of the HTTP layer's rails (plan.md §5,
// phase R1 exit criteria): endpoint resolution (WT_ENDPOINT, then
// endpoint.json under WT_HOME/$HOME/.wt, then the compiled default; a
// malformed file is an error naming the field, never a silent fallback),
// the body-authoritative exit code (the HTTP status is advisory and never
// consulted), the ephemeral session admission through the real client,
// and the Proxy: nil transport (the bearer must never reach a proxy).

import (
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
)

// TestEndpointResolutionOrder: WT_ENDPOINT wins; endpoint.json under
// WT_HOME carries both the location and the host token; a missing file
// leaves the client on the compiled default.
func TestEndpointResolutionOrder(t *testing.T) {
	// The compiled default: no WT_ENDPOINT, no endpoint.json, and nothing
	// listening — a coordinator verb exits 5 naming the default address.
	t.Setenv("WT_HOME", t.TempDir())
	t.Setenv("WT_ENDPOINT", "")
	base, token, err := resolveEndpoint()
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if base != api.DefaultEndpoint || token != "" {
		t.Errorf("default resolution = %q/%q, want %q/empty", base, token, api.DefaultEndpoint)
	}

	// endpoint.json under WT_HOME carries both fields.
	home := t.TempDir()
	ep := &api.Endpoint{SchemaVersion: 1, BaseURL: "http://127.0.0.1:7899", Token: strings.Repeat("11", 32)}
	if err := api.WriteEndpoint(api.EndpointPath(home), ep); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)
	base, token, err = resolveEndpoint()
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if base != ep.BaseURL || token != ep.Token {
		t.Errorf("endpoint.json resolution = %q/%q, want %q/%q", base, token, ep.BaseURL, ep.Token)
	}

	// WT_ENDPOINT overrides the location; the host token still comes from
	// endpoint.json.
	t.Setenv("WT_ENDPOINT", "http://127.0.0.1:7900/")
	base, token, err = resolveEndpoint()
	if err != nil {
		t.Fatalf("resolveEndpoint: %v", err)
	}
	if base != "http://127.0.0.1:7900" {
		t.Errorf("WT_ENDPOINT resolution = %q, want the override with the trailing slash stripped", base)
	}
	if token != ep.Token {
		t.Errorf("WT_ENDPOINT resolution lost the host token: %q", token)
	}
}

// TestMalformedEndpointFileNamesTheField: a malformed endpoint.json is an
// error naming the field — never a silent fallback to the compiled
// default, and never a dial of anything.
func TestMalformedEndpointFileNamesTheField(t *testing.T) {
	home := t.TempDir()
	// The missing-token shape, written by hand so it is malformed on
	// purpose.
	if err := os.WriteFile(api.EndpointPath(home),
		[]byte(`{"schema_version": 1, "base_url": "http://127.0.0.1:7899"}`), 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)
	os.Unsetenv("WT_ENDPOINT")

	code, _, stderr := runCLI(t, "list", "--json")
	if code != ExitUnreachable {
		t.Fatalf("exit = %d, want 5 (the endpoint cannot be trusted); stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "token") {
		t.Errorf("the refusal must name the field: %s", stderr)
	}
	if !strings.Contains(stderr, "endpoint.json") {
		t.Errorf("the refusal must name the file: %s", stderr)
	}
	// The compiled default must not have been dialed: the error is about
	// the file, not about 127.0.0.1:7833.
	if strings.Contains(stderr, "7833") {
		t.Errorf("a malformed endpoint file fell back to the compiled default: %s", stderr)
	}
}

// TestClientTransportSendsNoProxy: the client's transport is explicitly
// constructed with Proxy: nil — http.DefaultTransport honours
// HTTP_PROXY/HTTPS_PROXY, and the coordinator's bearer token must never
// be sent to a proxy (plan.md §7). The env is set so the assertion is
// meaningful: with the standard transport these proxies would be used.
func TestClientTransportSendsNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "http://proxy.example:8080")
	t.Setenv("HTTPS_PROXY", "http://proxy.example:8080")
	t.Setenv("NO_PROXY", "")

	tr := newHTTPClient().Transport.(*http.Transport)
	if tr.Proxy != nil {
		t.Fatalf("the transport carries a Proxy function; it must be nil so the bearer never reaches a proxy")
	}
	// The one request path goes through this client.
	if c := newHTTPClient(); c.Transport == nil {
		t.Error("the client has no transport")
	}
}

// TestResponseBodyIsAuthoritativeForExitCode: the client reads the exit
// code from the response body and never re-derives it from the HTTP
// status — a 500 carrying code 3 exits 3, and a 200 carrying code 4
// exits 4.
func TestResponseBodyIsAuthoritativeForExitCode(t *testing.T) {
	// A "500" that is really a code-3 refusal.
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.VersionPath, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.VersionInfo{Min: api.VersionMin, Max: api.VersionMax})
	})
	mux.HandleFunc("POST /v1/list", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(api.Response{Error: &api.Error{
			Code: 3, Msg: "refused by the coordinator", Remedy: "re-run: wt list",
		}})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)
	t.Setenv("WT_ENDPOINT", ts.URL)

	code, _, stderr := runCLI(t, "list", "--json")
	if code != ExitRefused {
		t.Fatalf("exit = %d, want 3 from the body (the status said 500); stderr: %s", code, stderr)
	}

	// A 200 that is really a code-4 refusal.
	mux2 := http.NewServeMux()
	mux2.HandleFunc("GET "+api.VersionPath, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.VersionInfo{Min: api.VersionMin, Max: api.VersionMax})
	})
	mux2.HandleFunc("POST /v1/ports/scan", func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.Response{Error: &api.Error{
			Code: 4, Msg: "discovery unavailable", Remedy: "install lsof, then re-run: wt ports scan",
		}})
	})
	ts2 := httptest.NewServer(mux2)
	t.Cleanup(ts2.Close)
	t.Setenv("WT_ENDPOINT", ts2.URL)

	code, _, stderr = runCLI(t, "ports", "scan")
	if code != ExitUnavailable {
		t.Fatalf("exit = %d, want 4 from the body (the status said 200); stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "lsof") {
		t.Errorf("the remedy from the body did not reach stderr: %s", stderr)
	}
}

// TestEphemeralClientGetsASessionThroughTheWire: WT_CLIENT_TOKEN plus
// WT_CLIENT_EPHEMERAL=1 compose — the token admits, the flag declares the
// lifecycle — and the client POSTs /v1/session once, then presents the
// issued session id as its bearer for the real request.
func TestEphemeralClientGetsASessionThroughTheWire(t *testing.T) {
	var gotBearer string
	var sessionCalls int
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.VersionPath, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.VersionInfo{Min: api.VersionMin, Max: api.VersionMax})
	})
	mux.HandleFunc("POST /v1/session", func(w http.ResponseWriter, r *http.Request) {
		sessionCalls++
		if got := r.Header.Get("Authorization"); got != "Bearer session-admit-token-0123456789" {
			t.Errorf("session admission bearer = %q, want the container token", got)
		}
		json.NewEncoder(w).Encode(api.Response{Result: mustJSONT(api.SessionResult{
			SessionID: strings.Repeat("ee", 32),
		})})
	})
	mux.HandleFunc("POST /v1/list", func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(api.Response{Result: mustJSONT(api.ListResult{})})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	t.Setenv("WT_ENDPOINT", ts.URL)
	t.Setenv("WT_CLIENT_TOKEN", "session-admit-token-0123456789")
	t.Setenv("WT_CLIENT_EPHEMERAL", "1")

	code, stdout, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if sessionCalls != 1 {
		t.Errorf("session calls = %d, want exactly 1", sessionCalls)
	}
	if gotBearer != "Bearer "+strings.Repeat("ee", 32) {
		t.Errorf("the list request carried %q, want the issued session id", gotBearer)
	}
	if !strings.Contains(stdout, "entries") {
		t.Errorf("list output = %s", stdout)
	}
}

// TestNamedClientPresentsItsTokenDirectly: WT_CLIENT_TOKEN alone is the
// named path — the token is the bearer on every request, no session
// exchange.
func TestNamedClientPresentsItsTokenDirectly(t *testing.T) {
	var gotBearer string
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.VersionPath, func(w http.ResponseWriter, r *http.Request) {
		json.NewEncoder(w).Encode(api.VersionInfo{Min: api.VersionMin, Max: api.VersionMax})
	})
	mux.HandleFunc("POST /v1/list", func(w http.ResponseWriter, r *http.Request) {
		gotBearer = r.Header.Get("Authorization")
		json.NewEncoder(w).Encode(api.Response{Result: mustJSONT(api.ListResult{})})
	})
	ts := httptest.NewServer(mux)
	t.Cleanup(ts.Close)

	t.Setenv("WT_ENDPOINT", ts.URL)
	t.Setenv("WT_CLIENT_TOKEN", "named-token-0123456789abcdef")
	t.Setenv("WT_CLIENT_EPHEMERAL", "")

	code, _, stderr := runCLI(t, "list", "--json")
	if code != ExitOK {
		t.Fatalf("exit = %d; stderr: %s", code, stderr)
	}
	if gotBearer != "Bearer named-token-0123456789abcdef" {
		t.Errorf("bearer = %q, want the named token itself", gotBearer)
	}
}

// TestHostClientPresentsTheEndpointToken: a host client's bearer is the
// token from endpoint.json under WT_HOME.
func TestHostClientPresentsTheEndpointToken(t *testing.T) {
	home := t.TempDir()
	hostToken := strings.Repeat("ab", 32)
	if err := api.WriteEndpoint(api.EndpointPath(home), &api.Endpoint{
		SchemaVersion: 1, BaseURL: "http://unused.invalid:1", Token: hostToken,
	}); err != nil {
		t.Fatal(err)
	}
	t.Setenv("WT_HOME", home)

	kind, token := clientCredentials()
	if kind != api.KindHost || token != "" {
		t.Errorf("clientCredentials = %q/%q, want host with no client token", kind, token)
	}
	// The dial resolves the host token from the endpoint file.
	base, resolved, err := resolveEndpoint()
	if err != nil {
		t.Fatal(err)
	}
	if base != "http://unused.invalid:1" || resolved != hostToken {
		t.Errorf("dial resolved %q/%q, want the endpoint file's base and token", base, resolved)
	}
}

// TestClientNeverRetries: a transport failure is exit 5 after exactly one
// request — a retried POST would double-allocate (PLAN-SCOPE.md non-goal
// 4). The server counts connections: it accepts, reads nothing and
// closes, so every request fails at the transport.
func TestClientNeverRetries(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ln.Close() })
	conns := make(chan struct{}, 16)
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			conns <- struct{}{}
			c.Close()
		}
	}()
	t.Setenv("WT_ENDPOINT", "http://"+ln.Addr().String())

	code, _, stderr := runCLI(t, "list", "--json")
	if code != ExitUnreachable {
		t.Fatalf("exit = %d, want 5; stderr: %s", code, stderr)
	}
	if !strings.Contains(stderr, "fix:") {
		t.Errorf("the exit-5 error must name the start command: %s", stderr)
	}
	select {
	case <-conns:
	default:
		t.Fatal("the client made no request at all")
	}
	select {
	case <-conns:
		t.Error("the client retried: more than one connection was made")
	default:
	}
}
