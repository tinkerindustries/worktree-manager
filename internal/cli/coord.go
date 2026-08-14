package cli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// coordClient is the client's handle on the coordinator: the resolved
// endpoint, the bearer token the next request presents, and the one HTTP
// client everything goes through. There is no persistent connection —
// each request is its own POST — so the type carries no reader or writer.
type coordClient struct {
	endpoint string
	token    string
	http     *http.Client
}

// Close is retained so call sites read as before; there is no connection
// to close.
func (c *coordClient) Close() {}

// newHTTPClient is the client's one HTTP client. The transport is
// explicitly constructed with Proxy: nil — http.DefaultTransport honours
// HTTP_PROXY/HTTPS_PROXY, and the coordinator's bearer token must never
// be sent to a proxy (plan.md §7, "The bearer token leaks to a proxy").
// The client never retries: a retried allocate double-allocates, so every
// transport failure is exit 5, once (PLAN-SCOPE.md non-goal 4).
func newHTTPClient() *http.Client {
	return &http.Client{Transport: &http.Transport{Proxy: nil}}
}

// dialCoordinator is the client's one dial-and-request path. Every verb
// that reaches the coordinator goes through it, so exit code 5 —
// coordinator unreachable — is wired in exactly one place, with the
// platform's start command as the remedy (ARCHITECTURE.md §11.3: code 5
// says start or mount the coordinator; code 4 says a capability the
// coordinator itself depends on is missing). There is no local fallback
// for any verb (ARCHITECTURE.md §4.4): a client that cannot reach the
// coordinator fails loudly.
//
// The dial resolves the endpoint — WT_ENDPOINT, then endpoint.json under
// WT_HOME/$HOME/.wt, then the compiled default — reads the coordinator's
// version range from GET /version (the hello's replacement: a mismatch
// is a real upgrade message, not a bare 404), and establishes the bearer:
// the host token from endpoint.json, the WT_CLIENT_TOKEN for a named
// container, or a freshly issued session id for an ephemeral one. A
// refusal anywhere along the way carries its own exit code and reaches
// the process exit status unchanged.
func dialCoordinator() (*coordClient, *Error) {
	base, hostToken, rerr := resolveEndpoint()
	if rerr != nil {
		return nil, New(ExitUnreachable, rerr.Error(), "fix the endpoint configuration, then re-run")
	}
	client := newHTTPClient()

	// The version check replaces the hello negotiation: read the
	// coordinator's range and refuse locally when the pair does not
	// overlap, naming the binary that must move.
	if verr := checkVersion(client, base); verr != nil {
		return nil, verr
	}

	kind, token := clientCredentials()
	bearer := token
	if kind == api.KindHost {
		bearer = hostToken
	}
	if kind == api.KindEphemeral {
		// An ephemeral client presents the container token once to
		// POST /v1/session and receives a session id — a clients row —
		// which is its bearer for every later request. Session ids
		// survive a wtd restart mid-init, which is the point of them
		// being rows (plan.md §5, phase R1).
		sid, serr := requestSession(client, base, token)
		if serr != nil {
			return nil, serr
		}
		bearer = sid
	}
	return &coordClient{endpoint: base, token: bearer, http: client}, nil
}

// resolveEndpoint resolves the coordinator's base URL and the host token:
// WT_ENDPOINT overrides the location, endpoint.json under
// WT_HOME/$HOME/.wt carries both, and the compiled default is the
// fallback for a missing file. A missing endpoint file leaves the client
// on the compiled default and then exit 5 if nothing answers; a malformed
// one is an error naming the field, never a silent fallback
// (PLAN-SCOPE.md, "In scope").
func resolveEndpoint() (base, hostToken string, err error) {
	home, herr := api.Home()
	if herr != nil {
		return "", "", herr
	}
	ep, rerr := api.ReadEndpoint(api.EndpointPath(home))
	if rerr != nil && !os.IsNotExist(rerr) {
		return "", "", rerr
	}
	if env := os.Getenv("WT_ENDPOINT"); env != "" {
		base = strings.TrimRight(env, "/")
	} else if rerr == nil {
		base = ep.BaseURL
	} else {
		base = api.DefaultEndpoint
	}
	if rerr == nil {
		hostToken = ep.Token
	}
	return base, hostToken, nil
}

// checkVersion reads GET /version and refuses a pair of ranges that do
// not overlap, exactly as the hello used to: exit code 3 with the upgrade
// named in whichever direction the ranges imply.
func checkVersion(client *http.Client, base string) *Error {
	req, err := http.NewRequest("GET", base+api.VersionPath, nil)
	if err != nil {
		return New(ExitUnreachable, fmt.Sprintf("building the version request for %s: %v", base, err), platform.CoordinatorStartCommand(base))
	}
	// The version check is a wt-client request like any other: the server
	// requires X-Wt-Client on every request, version included.
	req.Header.Set("X-Wt-Client", "1")
	resp, err := client.Do(req)
	if err != nil {
		return New(ExitUnreachable,
			fmt.Sprintf("coordinator unreachable at %s: %v", base, err),
			platform.CoordinatorStartCommand(base))
	}
	defer resp.Body.Close()
	var info api.VersionInfo
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return New(ExitUnreachable,
			fmt.Sprintf("the coordinator at %s did not answer /version with a version range: %v", base, err),
			platform.CoordinatorStartCommand(base))
	}
	if verr := api.CheckVersion(api.VersionMin, api.VersionMax, info.Min, info.Max); verr != nil {
		return New(verr.Code, verr.Msg, verr.Remedy)
	}
	return nil
}

// requestSession runs the ephemeral admission: POST /v1/session with the
// container token, returning the issued session id.
func requestSession(client *http.Client, base, containerToken string) (string, *Error) {
	body, err := json.Marshal(map[string]any{})
	if err != nil {
		return "", New(ExitFailure, "encoding the session request: "+err.Error(), "")
	}
	resp, rerr := doRequest(client, base, api.VerbSession, containerToken, body)
	if rerr != nil {
		return "", rerr
	}
	defer resp.Body.Close()
	raw, derr := decodeResponse(base, resp)
	if derr != nil {
		return "", derr
	}
	var res api.SessionResult
	if err := json.Unmarshal(raw, &res); err != nil {
		return "", New(ExitFailure, fmt.Sprintf("decoding the session response: %v", err), "upgrade wt, then re-run")
	}
	if res.SessionID == "" {
		return "", New(ExitFailure, "the coordinator returned an empty session id", "upgrade wt, then re-run")
	}
	return res.SessionID, nil
}

// doRequest builds and sends one RPC request: POST <base>/<verb-path>
// with the JSON body, the bearer and X-Wt-Client: 1. The path comes from
// the route table, so a verb the client sends is a verb the server
// serves. A transport failure is exit 5, once, naming the start command
// — the client never retries.
func doRequest(client *http.Client, base, verb, bearer string, body []byte) (*http.Response, *Error) {
	path, ok := api.PathForVerb(verb)
	if !ok {
		return nil, New(ExitFailure, fmt.Sprintf("no route for verb %q", verb), "upgrade wt, then re-run")
	}
	req, err := http.NewRequest("POST", base+path, bytes.NewReader(body))
	if err != nil {
		return nil, New(ExitUnreachable, fmt.Sprintf("building the %s request for %s: %v", verb, base, err), platform.CoordinatorStartCommand(base))
	}
	req.Header.Set("Authorization", "Bearer "+bearer)
	req.Header.Set("X-Wt-Client", "1")
	resp, err := client.Do(req)
	if err != nil {
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator unreachable at %s: %v", base, err),
			platform.CoordinatorStartCommand(base))
	}
	return resp, nil
}

// decodeResponse turns one RPC response into the raw result, mapping a
// response error onto the client's exit-code error so the coordinator's
// codes 3 and 4 reach the process exit status unchanged. The response
// body is authoritative for the exit code; the HTTP status is advisory
// and never consulted here (plan.md §5, phase R1).
func decodeResponse(base string, resp *http.Response) (json.RawMessage, *Error) {
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator at %s closed before the response: %v", base, err),
			platform.CoordinatorStartCommand(base))
	}
	var envelope api.Response
	if err := json.Unmarshal(data, &envelope); err != nil {
		return nil, New(ExitUnreachable,
			fmt.Sprintf("the coordinator at %s did not answer with a JSON response: %v", base, err),
			platform.CoordinatorStartCommand(base))
	}
	if envelope.Error != nil {
		return nil, New(envelope.Error.Code, envelope.Error.Msg, envelope.Error.Remedy)
	}
	return envelope.Result, nil
}

// request sends one RPC and returns the decoded result. Every
// coordinator-backed verb reaches the wire through this one method, so
// the transport-failure exit code is wired in exactly one place.
func (c *coordClient) request(verb string, args any) (json.RawMessage, *Error) {
	var body []byte
	if args != nil {
		var err error
		body, err = json.Marshal(args)
		if err != nil {
			return nil, New(ExitFailure, fmt.Sprintf("encoding the %s request: %v", verb, err), "")
		}
	}
	resp, rerr := doRequest(c.http, c.endpoint, verb, c.token, body)
	if rerr != nil {
		return nil, rerr
	}
	defer resp.Body.Close()
	return decodeResponse(c.endpoint, resp)
}

// clientCredentials is the client's identity declaration: the kind and,
// for a container, the credential. WT_CLIENT_TOKEN (phase 6) is the
// admission credential — the token wtd was started with
// (--container-token), which names a capability rather than a policy, so
// it passes the ambient-value test of ARCHITECTURE.md §12.3: a child
// process inheriting it is still the same named container.
// WT_CLIENT_EPHEMERAL=1 declares the ephemeral lifecycle — every process
// in a disposable-clone image is in a disposable clone, which is the same
// test passing for the same reason. The two compose now: the token
// admits, the flag declares the lifecycle, so the old "both set is
// ambiguous" refusal is gone (plan.md §5, phase R1). Otherwise the
// client is a host client, whose token the coordinator never sees the
// client assert: it comes from the 0600 endpoint.json.
func clientCredentials() (kind, token string) {
	t := os.Getenv("WT_CLIENT_TOKEN")
	if os.Getenv("WT_CLIENT_EPHEMERAL") == "1" {
		return api.KindEphemeral, t
	}
	if t != "" {
		return api.KindNamed, t
	}
	return api.KindHost, ""
}
