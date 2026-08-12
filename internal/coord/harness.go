package coord

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Harness is the in-process coordinator of ARCHITECTURE.md §13.3: a full
// coordinator — store, handler, client table — whose inputs are protocol
// messages and a store path, run with no socket, no supervisor and no
// container. It is a deliverable rather than a test detail: phases 3 to 6
// run allocation, authorisation, entry lifecycle and migration against it.
type Harness struct {
	Store *store.Store
	H     *Handler
}

// NewHarness opens a store at root and builds the handler over it. The
// test handle makes a harness failure a test failure.
func NewHarness(t testing.TB, root string) *Harness {
	t.Helper()
	st, err := store.Open(root)
	if err != nil {
		t.Fatalf("harness: opening the store at %s: %v", root, err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := NewHandler(st, log)
	if err != nil {
		t.Fatalf("harness: %v", err)
	}
	return &Harness{Store: st, H: h}
}

// Connect runs the hello exchange for one client, the way the socket
// server would: version negotiation, identity assignment, clients.json
// observation. Kind is protocol.KindHost (a synthetic peer — the harness
// has no socket, and the real peer-credential path is the platform
// package's own test), protocol.KindNamed with a token, or
// protocol.KindEphemeral.
func (h *Harness) Connect(kind, token string) (*Session, *protocol.HelloReply) {
	peer := Peer{UID: 4242, Known: true} // synthetic: no kernel on a harness
	return h.H.Begin(peer, &protocol.Hello{
		Kind: kind, Token: token,
		MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
	})
}

// Request runs one full request for a session — encode, dispatch, decode —
// and returns the response.
func (h *Harness) Request(ctx context.Context, s *Session, verb string, args any) *protocol.Response {
	req := protocol.Request{Verb: verb}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return &protocol.Response{Error: &protocol.Error{Code: 1, Msg: err.Error(), Remedy: "fix the request arguments"}}
		}
		req.Args = raw
	}
	return h.H.Handle(ctx, s, &req)
}
