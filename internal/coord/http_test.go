package coord

// http_test.go is the HTTP layer's rails (plan.md §5, phase R1 exit
// criteria): the one auth refusal for missing, wrong and wrong-kind
// bearers (constant-time compared, never an oracle), the 1 MiB body cap,
// the Host check and X-Wt-Client requirement, the error-code-to-status
// mapping, the session admission, and the row-based ephemeral identity
// surviving a restart. All of it runs through httptest against the real
// Server — the coordinator's whole HTTP surface.

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
)

const (
	httpHostToken    = "host-token-0123456789abcdef0123456789abcdef"
	httpContainerTok = "container-token-0123456789abcdef"
)

// newHTTPTestServer builds the real HTTP surface over a harness handler
// with the two tokens installed and returns the test server.
func newHTTPTestServer(t *testing.T, h *Handler) *httptest.Server {
	t.Helper()
	h.Token = httpHostToken
	h.ContainerToken = httpContainerTok
	srv := NewServer(h, nil)
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)
	return ts
}

// doRPC runs one request against the server and returns the response and
// its body. bearer "" means no Authorization header.
func doRPC(t *testing.T, ts *httptest.Server, method, path, bearer string, body []byte, tweak ...func(*http.Request)) (*http.Response, []byte) {
	t.Helper()
	req, err := http.NewRequest(method, ts.URL+path, bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("X-Wt-Client", "1")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, fn := range tweak {
		if fn != nil {
			fn(req)
		}
	}
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("%s %s: %v", method, path, err)
	}
	defer resp.Body.Close()
	var buf bytes.Buffer
	if _, err := buf.ReadFrom(resp.Body); err != nil {
		t.Fatal(err)
	}
	return resp, buf.Bytes()
}

// errorBody decodes the one error body shape.
func errorBody(t *testing.T, body []byte) *api.Error {
	t.Helper()
	var env api.Response
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatalf("the body is not a response envelope: %v\n%s", err, body)
	}
	if env.Error == nil {
		t.Fatalf("no error in the body: %s", body)
	}
	return env.Error
}

// TestHTTPAuthOneRefusalForMissingWrongAndWrongKind: a request with no
// bearer, a wrong bearer or a bearer of the wrong kind (an ephemeral
// session id that is not a live row — revoked by reclamation, or never
// issued) is refused with one identical message, compared in constant
// time: the API is not an oracle (plan.md §5, phase R1).
func TestHTTPAuthOneRefusalForMissingWrongAndWrongKind(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)

	var msgs []string
	cases := []struct {
		name   string
		bearer string
	}{
		{"missing", ""},
		{"wrong", "wrong-token-0000000000000000"},
		{"wrong kind", strings.Repeat("ab", 32)}, // a session-id-shaped bearer that is no row
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			resp, body := doRPC(t, ts, "GET", "/v1/ping", tc.bearer, nil)
			if resp.StatusCode != http.StatusForbidden {
				t.Errorf("status = %d, want 403", resp.StatusCode)
			}
			e := errorBody(t, body)
			if e.Code != 3 {
				t.Errorf("code = %d, want 3", e.Code)
			}
			if e.Remedy == "" {
				t.Error("the refusal has no remedy")
			}
			msgs = append(msgs, e.Msg+"\x00"+e.Remedy)
		})
	}
	for i := 1; i < len(msgs); i++ {
		if msgs[i] != msgs[0] {
			t.Errorf("the refusals must not distinguish the cases:\nmissing:  %s\nwrong:    %s\nwrongkind: %s", msgs[0], msgs[1], msgs[2])
		}
	}
}

// TestHTTPPingRequiresAuth: GET /v1/ping with the host token and
// X-Wt-Client succeeds; the endpoint file token is the host credential.
func TestHTTPPingRequiresAuth(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)

	resp, body := doRPC(t, ts, "GET", "/v1/ping", httpHostToken, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200; body: %s", resp.StatusCode, body)
	}
	var env api.Response
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	if env.Error != nil || !strings.Contains(string(env.Result), `"ok":true`) {
		t.Errorf("ping body = %s, want {\"ok\":true}", body)
	}
}

// TestHTTPVersionIsUnauthenticated: GET /version reports the supported
// range with no credentials at all — a mismatched client must be able to
// read it to learn it must upgrade.
func TestHTTPVersionIsUnauthenticated(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	resp, body := doRPC(t, ts, "GET", "/version", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var info api.VersionInfo
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatalf("/version is not a VersionInfo: %v\n%s", err, body)
	}
	if info.Min != api.VersionMin || info.Max != api.VersionMax {
		t.Errorf("/version = %+v", info)
	}
}

// TestHTTPSessionAdmission: POST /v1/session with the container token
// issues an ephemeral session id (a clients row), which then
// authenticates every later request. A wrong token is the one refusal,
// and with no --container-token configured the coordinator admits host
// clients only — the session path is refused too.
func TestHTTPSessionAdmission(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	ts := newHTTPTestServer(t, h.H)

	// No container token configured: the session path is refused.
	h.H.ContainerToken = ""
	resp, body := doRPC(t, ts, "POST", "/v1/session", httpContainerTok, []byte(`{}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("session without --container-token: status = %d, want 403", resp.StatusCode)
	}
	if e := errorBody(t, body); e.Code != 3 {
		t.Errorf("session without --container-token: code = %d, want 3", e.Code)
	}

	h.H.ContainerToken = httpContainerTok
	resp, body = doRPC(t, ts, "POST", "/v1/session", httpContainerTok, []byte(`{}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session with the token: status = %d; body: %s", resp.StatusCode, body)
	}
	var env api.Response
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	var res api.SessionResult
	if err := json.Unmarshal(env.Result, &res); err != nil || len(res.SessionID) != 64 {
		t.Fatalf("session result = %q (err %v), want a 64-hex session id", res.SessionID, err)
	}

	// The issued session id authenticates a later request.
	resp, body = doRPC(t, ts, "GET", "/v1/ping", res.SessionID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping with the session id: status = %d; body: %s", resp.StatusCode, body)
	}

	// A wrong token on the session path is the one refusal.
	resp, body = doRPC(t, ts, "POST", "/v1/session", "wrong-token-0000000000000000", []byte(`{}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("session with a wrong token: status = %d, want 403", resp.StatusCode)
	}
	if e := errorBody(t, body); e.Code != 3 || !strings.Contains(e.Msg, "authentication refused") {
		t.Errorf("session with a wrong token = %+v", e)
	}
}

// TestHTTPSessionIdSurvivesRestart: session ids are rows, not memory —
// a wtd restart mid-init must not revoke them. A new handler over the
// same store resolves the old session id.
func TestHTTPSessionIdSurvivesRestart(t *testing.T) {
	root := filepath.Join(tempRoot(t), "wt")
	h1 := NewHarness(t, root)
	ts1 := newHTTPTestServer(t, h1.H)
	resp, body := doRPC(t, ts1, "POST", "/v1/session", httpContainerTok, []byte(`{}`))
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("session: status = %d; body: %s", resp.StatusCode, body)
	}
	var env api.Response
	if err := json.Unmarshal(body, &env); err != nil {
		t.Fatal(err)
	}
	var res api.SessionResult
	if err := json.Unmarshal(env.Result, &res); err != nil {
		t.Fatal(err)
	}

	// "Restart": a brand-new handler and server over the same store.
	h2 := NewHarness(t, root)
	ts2 := newHTTPTestServer(t, h2.H)
	resp, body = doRPC(t, ts2, "GET", "/v1/ping", res.SessionID, nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("ping after the restart: status = %d; body: %s", resp.StatusCode, body)
	}
}

// TestHTTPMaxBytesCap: a request body over the 1 MiB cap is refused with
// 400 and the cap named — the old wire's MaxMessageBytes preserved.
func TestHTTPMaxBytesCap(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	big := bytes.Repeat([]byte("x"), api.MaxMessageBytes+1)
	resp, body := doRPC(t, ts, "POST", "/v1/list", httpHostToken, big)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", resp.StatusCode)
	}
	e := errorBody(t, body)
	if e.Code != 2 {
		t.Errorf("code = %d, want 2", e.Code)
	}
	if !strings.Contains(e.Msg, "cap") {
		t.Errorf("the refusal must name the cap: %s", e.Msg)
	}
	if e.Remedy == "" {
		t.Error("the refusal has no remedy")
	}

	// Just under the cap is not refused for size: the body reaches the
	// verb and fails on its own decode, never on the cap.
	ok := bytes.Repeat([]byte(" "), api.MaxMessageBytes-1)
	resp, body = doRPC(t, ts, "POST", "/v1/list", httpHostToken, ok)
	if resp.StatusCode == http.StatusBadRequest && strings.Contains(string(body), "cap") {
		t.Errorf("an at-cap body must not trip the size cap: %s", body)
	}
	if strings.Contains(string(body), "cap") {
		t.Errorf("an at-cap body must fail on JSON, not on the cap: %s", body)
	}
}

// TestHTTPHostCheck: a Host header that is neither loopback nor the
// server's address is refused — the DNS-rebinding guard. A loopback Host
// passes.
func TestHTTPHostCheck(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)

	resp, body := doRPC(t, ts, "GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) {
		r.Host = "attacker.example.com"
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("foreign Host: status = %d, want 403", resp.StatusCode)
	}
	if e := errorBody(t, body); e.Code != 3 || !strings.Contains(e.Msg, "Host") {
		t.Errorf("foreign Host refusal = %+v", e)
	}

	resp, _ = doRPC(t, ts, "GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) {
		r.Host = "localhost:9999"
	})
	if resp.StatusCode != http.StatusOK {
		t.Errorf("localhost Host: status = %d, want 200", resp.StatusCode)
	}
}

// TestHTTPAllowedHosts: a Host named by --allow-host passes the guard,
// with or without the port the client dialled, and every Host that was
// not named is still refused. This is what lets a container reach the
// host by name — host.docker.internal on Docker Desktop — without the
// guard being opened to anything else.
func TestHTTPAllowedHosts(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt")).H
	h.Token = httpHostToken
	h.ContainerToken = httpContainerTok
	srv := NewServer(h, nil)
	srv.AllowedHosts = []string{"host.docker.internal"}
	ts := httptest.NewServer(srv)
	t.Cleanup(ts.Close)

	for _, host := range []string{"host.docker.internal:7833", "host.docker.internal", "HOST.DOCKER.INTERNAL:7833"} {
		resp, body := doRPC(t, ts, "GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) {
			r.Host = host
		})
		if resp.StatusCode != http.StatusOK {
			t.Errorf("allowed Host %q: status = %d, want 200 (%s)", host, resp.StatusCode, body)
		}
	}

	// The guard is opened to the named host, not to names in general.
	resp, body := doRPC(t, ts, "GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) {
		r.Host = "attacker.example.com"
	})
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("unnamed Host alongside an allow-host: status = %d, want 403", resp.StatusCode)
	}
	// The refusal names the flag that would admit it, and the host it
	// would have to name — a container operator reading this message has
	// the whole remedy in front of them.
	if e := errorBody(t, body); !strings.Contains(e.Remedy, "--allow-host attacker.example.com") {
		t.Errorf("refusal remedy does not name the flag and the host: %+v", e)
	}
}

// TestHTTPXWtClientCheck: a request without X-Wt-Client: 1 is refused —
// a browser cannot set a custom header on a simple-form POST, so this
// forces a preflight that is never answered. No CORS header is ever sent
// and an OPTIONS request is not answered.
func TestHTTPXWtClientCheck(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)

	req, err := http.NewRequest("GET", ts.URL+"/v1/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer "+httpHostToken)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("missing X-Wt-Client: status = %d, want 403", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" {
		t.Error("a CORS header was sent")
	}

	// A preflight is never answered: OPTIONS dies at the X-Wt-Client
	// check (a browser preflight cannot set the custom header), and no
	// CORS headers ride along.
	req, err = http.NewRequest("OPTIONS", ts.URL+"/v1/ping", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Access-Control-Request-Method", "POST")
	resp, err = ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode == http.StatusOK || resp.StatusCode == http.StatusNoContent {
		t.Errorf("preflight status = %d, want a refusal (the preflight must not be answered)", resp.StatusCode)
	}
	if resp.Header.Get("Access-Control-Allow-Origin") != "" || resp.Header.Get("Access-Control-Allow-Methods") != "" {
		t.Error("the preflight was answered with CORS headers")
	}
}

// TestHTTPStatusForCodeMapping pins the advisory status mapping: 400→code
// 2, 403→code 3, 424→code 4, 500→code 1. The body is authoritative for
// the exit code; the status is for anything reading the API that is not
// wt, and exit code 5 never appears.
func TestHTTPStatusForCodeMapping(t *testing.T) {
	cases := []struct {
		code int
		want int
	}{
		{1, http.StatusInternalServerError},
		{2, http.StatusBadRequest},
		{3, http.StatusForbidden},
		{4, http.StatusFailedDependency},
	}
	for _, tc := range cases {
		if got := statusForCode(tc.code); got != tc.want {
			t.Errorf("statusForCode(%d) = %d, want %d", tc.code, got, tc.want)
		}
	}
	// End to end: the two codes the wire produces naturally arrive with
	// the mapped status and the code in the body.
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)

	resp, body := doRPC(t, ts, "GET", "/v1/ping", "wrong-token", nil)
	if resp.StatusCode != http.StatusForbidden || errorBody(t, body).Code != 3 {
		t.Errorf("auth refusal = %d/%s, want 403 with code 3", resp.StatusCode, body)
	}
	resp, body = doRPC(t, ts, "POST", "/v1/list", httpHostToken, bytes.Repeat([]byte("x"), api.MaxMessageBytes+1))
	if resp.StatusCode != http.StatusBadRequest || errorBody(t, body).Code != 2 {
		t.Errorf("oversized request = %d/%s, want 400 with code 2", resp.StatusCode, body)
	}
}

// TestHTTPUnknownPathIsNotAnRPC: only the route table is served; a path
// outside it is a plain 404, not a coordinator error.
func TestHTTPUnknownPathIsNotAnRPC(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	resp, body := doRPC(t, ts, "POST", "/v1/bogus", httpHostToken, []byte(`{}`))
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("unknown path status = %d, want 404; body: %s", resp.StatusCode, body)
	}
}

// TestHTTPHostClientOnlyBandsReserve: bands.reserve stays host-client-
// only over the wire — a named client with a valid token is refused with
// the policy refusal, not the auth refusal.
func TestHTTPHostClientOnlyBandsReserve(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	resp, body := doRPC(t, ts, "POST", "/v1/bands/reserve", httpContainerTok, []byte(`{"host":true,"ports":[5319],"note":"x"}`))
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("named bands.reserve status = %d, want 403", resp.StatusCode)
	}
	e := errorBody(t, body)
	if e.Code != 3 || !strings.Contains(e.Msg, "host") {
		t.Errorf("named bands.reserve refusal = %+v, want the host-only policy refusal", e)
	}
}

// TestHTTPRouteTableServesEveryVerb: every route in the table answers
// with a coordinator response (not a mux 404) once authenticated. The
// verbs whose args are malformed answer 400 — the point is that the path
// is routed at all.
func TestHTTPRouteTableServesEveryVerb(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	// The session path is the one route with its own admission; it is
	// exercised in TestHTTPSessionAdmission. The empty-args bodies are
	// refused by the verbs' own validation (code 3) — the point is that
	// the path is routed to the coordinator at all, never a mux 404.
	for _, r := range api.Routes {
		if r.Verb == api.VerbSession {
			continue
		}
		resp, body := doRPC(t, ts, r.Method, r.Path, httpHostToken, []byte(`{}`))
		if resp.StatusCode == http.StatusNotFound {
			t.Errorf("route %s (%s) is not served: 404", r.Method, r.Path)
		}
		if resp.StatusCode == http.StatusForbidden {
			// The policy refusals (bands.reserve host-only) are legitimate
			// coordinator answers; a 403 with the auth refusal means the
			// host token did not authenticate.
			if strings.Contains(string(body), "authentication refused") {
				t.Errorf("route %s (%s) refused an authenticated host client: %s", r.Method, r.Path, body)
			}
		}
	}
	// And the version path is served unauthenticated.
	resp, _ := doRPC(t, ts, "GET", "/version", "", nil)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET /version status = %d, want 200", resp.StatusCode)
	}
}

// TestHTTPServerErrorsNameTheFix: every refusal the HTTP layer writes
// carries a remedy naming the command that fixes it (plan.md §3).
func TestHTTPServerErrorsNameTheFix(t *testing.T) {
	ts := newHTTPTestServer(t, NewHarness(t, filepath.Join(tempRoot(t), "wt")).H)
	cases := []struct {
		method, path, bearer string
		body                 []byte
		tweak                func(*http.Request)
	}{
		{"GET", "/v1/ping", "", nil, nil},
		{"GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) { r.Host = "evil.example.com" }},
		{"GET", "/v1/ping", httpHostToken, nil, func(r *http.Request) { r.Header.Del("X-Wt-Client") }},
		{"POST", "/v1/list", httpHostToken, bytes.Repeat([]byte("x"), api.MaxMessageBytes+1), nil},
	}
	for i, tc := range cases {
		_, body := doRPC(t, ts, tc.method, tc.path, tc.bearer, tc.body, tc.tweak)
		e := errorBody(t, body)
		if e.Remedy == "" {
			t.Errorf("case %d: a refusal with an empty remedy: %+v", i, e)
		}
	}
}
