package coord

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"strconv"
	"testing"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/store"
)

// Harness is the in-process coordinator of ARCHITECTURE.md §13.3: a full
// coordinator — store, handler, client table — whose inputs are API
// requests and a store path, run with no listener, no supervisor and no
// container. It is a deliverable rather than a test detail: phases 3 to 6
// run allocation, authorisation, entry lifecycle and migration against
// it, and it survives the transport rework untouched because it mints
// identities directly — the wire's identity resolution (the bearer
// checks) lives in the HTTP layer, which has its own tests
// (plan.md §5, phase R1).
type Harness struct {
	Store *store.Store
	H     *Handler
}

// NewHarness opens a store at root and builds the handler over it. The
// test handle makes a harness failure a test failure, and it is also what
// closes the store: the database handle is registered for cleanup here so
// no test has to remember, and so the store's directory is deletable when
// the test ends. (Windows refuses to unlink an open file, which is what
// made the leak visible; the leak was there on every platform.)
func NewHarness(t testing.TB, root string) *Harness {
	t.Helper()
	st, err := store.Open(root)
	if err != nil {
		t.Fatalf("harness: opening the store at %s: %v", root, err)
	}
	t.Cleanup(func() {
		if err := st.Close(); err != nil {
			t.Errorf("harness: closing the store at %s: %v", root, err)
		}
	})
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := NewHandler(st, log)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return &Harness{Store: st, H: h}
}

// Connect runs the identity resolution for one client, the way the HTTP
// layer would, with the authentication checks bypassed: the harness has
// no bearer tokens, and the real token comparisons are the HTTP layer's
// own tests. Kind is api.KindHost (identity key "host", the synthetic
// host — the real host resolution is the endpoint token's test),
// api.KindNamed with a token, or api.KindEphemeral (an issued session
// id). The error is the coordinator's refusal, nil on success.
func (h *Harness) Connect(kind, token string) (*Session, *api.Error) {
	return h.ConnectPeer(4242, kind, token)
}

// ConnectPeer runs the identity resolution with an explicit host key, so
// a test can act as a second host client with a different identity — the
// ownership check needs two distinct identities. (Over the wire every
// host client is the one "host" identity; the harness keeps the synthetic
// uid keys the tests assert on, so the ownership machinery stays
// testable on both sides without a wire rewrite.)
func (h *Harness) ConnectPeer(hostKey int, kind, token string) (*Session, *api.Error) {
	var id Identity
	switch kind {
	case api.KindHost:
		id = Identity{Kind: api.KindHost, Key: strconv.Itoa(hostKey)}
	case api.KindNamed:
		if token == "" {
			return nil, &api.Error{
				Code:   3,
				Msg:    "a named container client must present a token",
				Remedy: "configure the token into the image (WT_CLIENT_TOKEN) and re-run",
			}
		}
		id = Identity{Kind: api.KindNamed, Key: token}
	case api.KindEphemeral:
		id = Identity{Kind: api.KindEphemeral, Key: newSessionID(), Ephemeral: true}
	default:
		return nil, &api.Error{
			Code:   3,
			Msg:    "unknown client kind " + kind,
			Remedy: "upgrade wt: this coordinator knows the kinds host, named and ephemeral",
		}
	}
	if err := h.H.observe(id); err != nil {
		return nil, h.H.observeFailure(err)
	}
	return &Session{Version: h.H.ProtocolMax, Identity: id}, nil
}

// Request runs one full request for a session — encode, dispatch, decode —
// and returns the response.
func (h *Harness) Request(ctx context.Context, s *Session, verb string, args any) *api.Response {
	req := api.Request{Verb: verb}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return &api.Response{Error: &api.Error{Code: 1, Msg: err.Error(), Remedy: "fix the request arguments"}}
		}
		req.Args = raw
	}
	return h.H.Handle(ctx, s, &req)
}
