package cli

import (
	"net/http"
	"os"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	apiclient "github.com/tinkerindustries/worktree-manager/internal/api/client"
	"github.com/tinkerindustries/worktree-manager/internal/platform"
)

// coordClient is the client's handle on the coordinator: the resolved
// endpoint and the generated client every verb goes through. There is no
// persistent connection — each request is its own POST — so the type
// carries no reader or writer.
type coordClient struct {
	endpoint string
	client   *apiclient.Client
}

// Close is retained so call sites read as before; there is no connection
// to close.
func (c *coordClient) Close() {}

// newHTTPClient returns the one HTTP client everything goes through. The
// transport is explicitly constructed with Proxy: nil —
// http.DefaultTransport honours HTTP_PROXY/HTTPS_PROXY, and the
// coordinator's bearer token must never be sent to a proxy (plan.md §7,
// "The bearer token leaks to a proxy"). The decision lives in the
// generated client's New; this wrapper exists so the transport rail can
// be pinned without dialing (http_test.go).
func newHTTPClient() *http.Client {
	return apiclient.New("", "").HTTP()
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
	c := apiclient.New(base, "")

	// The version check replaces the hello negotiation: read the
	// coordinator's range and refuse locally when the pair does not
	// overlap, naming the binary that must move.
	info, verr := c.Version()
	if verr != nil {
		return nil, requestErr(base, api.VersionPath, verr)
	}
	if e := api.CheckVersion(api.VersionMin, api.VersionMax, info.Min, info.Max); e != nil {
		return nil, New(e.Code, e.Msg, e.Remedy)
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
		c.SetToken(token)
		sres, serr := c.Session()
		if serr != nil {
			return nil, requestErr(base, api.VerbSession, serr)
		}
		if sres.SessionID == "" {
			return nil, New(ExitFailure, "the coordinator returned an empty session id", "upgrade wt, then re-run")
		}
		bearer = sres.SessionID
	}
	c.SetToken(bearer)
	return &coordClient{endpoint: base, client: c}, nil
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

// requestErr maps the generated client's error onto the cli's exit-code
// error, in the one dial-and-request path. The response body is
// authoritative for the exit code: codes 3 and 4 from the envelope reach
// the process exit status unchanged, and code 5 — client-side only, a
// transport failure, never an HTTP status — carries the start command as
// its remedy (plan.md §5, phase R1).
func requestErr(base, verb string, err *apiclient.Error) *Error {
	if err == nil {
		return nil
	}
	if err.Code == ExitUnreachable {
		return New(ExitUnreachable, err.Msg, platform.CoordinatorStartCommand(base))
	}
	return New(err.Code, err.Msg, err.Remedy)
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
