// Package coord is the coordinator's request core and resident process:
// the handler that turns protocol messages into responses, the socket
// server that runs it per connection, and the in-process harness phases 3
// to 6 lean on (ARCHITECTURE.md §13.3).
//
// One writer serialises here: there is no lock file, no generation
// counter, no view identity and no view scoping (revision 2 deleted all
// four) — concurrent requests serialise in this one process, and the
// handler's mutex makes that true across its connection goroutines.
package coord

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"strconv"
	"sync"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Identity is the coordinator's identity for one client, assigned from
// kernel data or declared data — never from the caller's claim of being a
// host client (ARCHITECTURE.md §10.2). Key is the uid for a host client,
// the token for a named container, and the issued session id for an
// ephemeral one.
type Identity struct {
	Kind      string
	Key       string
	Ephemeral bool
}

// Session is one connected client: the negotiated protocol version and the
// identity the coordinator assigned.
type Session struct {
	Version  int
	Identity Identity
}

// Peer is what the kernel reports about a connection. Only host identity
// uses it; Known is false where the platform cannot report peer
// credentials, and a host claim is then refused.
type Peer struct {
	UID   int
	Known bool
}

// Handler is the coordinator's request core: protocol messages in,
// protocol responses out, a store path, and no socket, supervisor or
// container — exactly the in-process harness of ARCHITECTURE.md §13.3.
type Handler struct {
	// ProtocolMin/ProtocolMax are the coordinator's advertised version
	// range, the protocol package's constants by default. They are fields
	// so the refusal path is testable from both directions (a coordinator
	// ahead of its client) without rebuilding.
	ProtocolMin int
	ProtocolMax int

	// Probe is the phase-4 seam: the check that skips a slot whose derived
	// resources probe as held. InstallDrivers replaces the phase-3 no-probe
	// probe (noProbe) with the driver-backed probe at startup; a test can
	// install a fake.
	Probe Probe

	// Drivers is the driver registry the allocation probe and the teardown
	// path run against, installed by InstallDrivers. Nil until then: phase-3
	// tests never call the teardown path, and cmd/wtd installs the registry
	// before serving.
	Drivers driver.Registry

	// Docker is the docker seam the teardown path runs against. Nil means
	// the real CLI runner (driver.NewDocker) is used; tests install fakes.
	Docker driver.Docker

	// Machine is the VM runner seam the machine driver runs against. Nil
	// means the platform's real runner (platform.Machine) is used; tests
	// install fakes.
	Machine platform.MachineRunner

	// ReapBinaries is the reaper's allowlist seam: the binaries the spec
	// names, which are the only processes the reaper may signal
	// (04-lifecycle.md §6). The default reads reaper.binaries from the
	// spec; a test installs a fake to drive the signalling path without
	// touching the spec.
	ReapBinaries func(sp *spec.Spec) []string

	// InContainer is the reaper's container-detection seam. The default is
	// the platform probe; a test running inside a container installs a
	// fake so the reap's decision logic is testable on both sides.
	InContainer func() bool

	// ReclaimInterval is how long an ephemeral client may go unseen before
	// its entries become reclaimable. Zero means ReclaimIntervalDefault
	// (24 hours, the phase-6 choice — plan.md §9.2, R3); a test shortens
	// it to exercise reclamation without waiting a day.
	ReclaimInterval time.Duration

	// SweepInterval is how often the coordinator's own cleanup sweep runs.
	// Zero means SweepIntervalDefault (an hour, the phase-8 choice); a
	// test shortens it to exercise the sweep without waiting an hour.
	SweepInterval time.Duration

	// Gh is the scheduled sweep's gh seam: the coordinator shells out to
	// gh with the given working directory ("" for none), the way the
	// interactive verb does. Nil means the real gh runner; tests install
	// fakes.
	Gh func(dir string, args ...string) ([]byte, error)

	st  *store.Store
	log *slog.Logger

	// mu serialises every store-touching operation: connections arrive on
	// their own goroutines, and load-modify-save of the registry, the band
	// ledger and clients.json is coordinator-internal write work that must
	// not race itself. One writer serialises here — there is no lock file,
	// no generation counter, no compare-and-swap retry; concurrent requests
	// serialise in this one process (revision 2, plan.md §2).
	mu sync.Mutex
}

// NewHandler builds the request core over an opened store.
func NewHandler(st *store.Store, log *slog.Logger) (*Handler, error) {
	if st == nil {
		return nil, errors.New("coord: no store")
	}
	if log == nil {
		log = slog.Default()
	}
	return &Handler{
		ProtocolMin: protocol.VersionMin,
		ProtocolMax: protocol.VersionMax,
		Probe:       noProbe,
		st:          st,
		log:         log,
	}, nil
}

// Store returns the handler's store.
func (h *Handler) Store() *store.Store { return h.st }

// Begin handles a connection's first exchange: version negotiation, the
// identity assignment (peer credentials for a host client — the claim is
// ignored; the token for a named container; an issued session id for an
// ephemeral one), and the clients.json observation. The reply carries the
// agreed version and, for an ephemeral client, the session id; a refusal's
// error names the upgrade in whichever direction the ranges do not overlap
// (plan.md §3's refuse-rather-than-partially-honour rule).
func (h *Handler) Begin(peer Peer, hello *protocol.Hello) (*Session, *protocol.HelloReply) {
	agreed, err := protocol.Agree(hello.MinVer, hello.MaxVer, h.ProtocolMin, h.ProtocolMax)
	if err != nil {
		return nil, &protocol.HelloReply{Error: versionRefusal(err)}
	}
	id, herr := h.assignIdentity(peer, hello)
	if herr != nil {
		return nil, &protocol.HelloReply{Error: herr}
	}
	if err := h.observe(id); err != nil {
		h.log.Error("recording the client in clients.json", "kind", id.Kind, "identity", id.Key, "err", err)
		return nil, &protocol.HelloReply{Error: &protocol.Error{
			Code:   4, // required context unavailable: the coordinator's own store is broken
			Msg:    fmt.Sprintf("the coordinator cannot record the client: %v", err),
			Remedy: "check the coordinator's store (WT_HOME) is writable, then re-run",
		}}
	}
	sess := &Session{Version: agreed, Identity: id}
	reply := &protocol.HelloReply{Agreed: agreed}
	if id.Ephemeral {
		reply.SessionID = id.Key
	}
	return sess, reply
}

// Handle runs one request and returns its response. The response carries
// either a result or an error with the exit code the client should use, so
// codes 3, 4 and 5 originate here and reach the process exit status
// unchanged (ARCHITECTURE.md §11.3).
func (h *Handler) Handle(ctx context.Context, s *Session, req *protocol.Request) *protocol.Response {
	switch req.Verb {
	case "ping":
		// The protocol plumbing's one verb: a full request round trip with
		// nothing behind it. Not a wt command — later phases add the real
		// verbs on the same dispatch.
		return &protocol.Response{Result: json.RawMessage(`{"ok":true}`)}
	case verbAllocate:
		return h.allocate(s, req)
	case verbMaterialise:
		return h.materialise(s, req)
	case verbActivate:
		return h.activate(s, req)
	case verbRelease:
		return h.release(s, req)
	case verbRm:
		return h.rm(s, req)
	case verbBandsReserve:
		return h.reserveBand(s, req)
	case verbBandsList:
		return h.listBands(s, req)
	case verbBandsSuggest:
		return h.suggestBand(s, req)
	case verbPortsScan:
		return h.portsScan(s, req)
	case verbList:
		return h.list(s, req)
	case verbDoctor:
		return h.doctor(s, req)
	case verbReconcile:
		return h.reconcile(s, req)
	case verbClients:
		return h.clientsList(s, req)
	default:
		return &protocol.Response{Error: &protocol.Error{
			Code: 1,
			Msg:  fmt.Sprintf("unknown verb %q", req.Verb),
			// The system refuses rather than partially honouring: an
			// unknown verb is a newer client, and the remedy names the
			// upgrade (02-coordination.md §11).
			Remedy: fmt.Sprintf("upgrade wt: this coordinator speaks protocol %d and knows no verb %q", s.Version, req.Verb),
		}}
	}
}

// versionRefusal maps a non-overlapping pair to the wire refusal: exit
// code 3 (refused), a message naming the upgrade in the direction the
// ranges imply, and the install command for whichever binary must move.
func versionRefusal(err error) *protocol.Error {
	var ue *protocol.UpgradeError
	if !errors.As(err, &ue) {
		return &protocol.Error{Code: 3, Msg: err.Error(), Remedy: "check the client and coordinator builds, then re-run"}
	}
	remedy := fmt.Sprintf("upgrade wtd to speak protocol %d, then re-run", ue.Need)
	if ue.Side == "client" {
		remedy = fmt.Sprintf("upgrade wt to speak protocol %d, then re-run", ue.Need)
	}
	return &protocol.Error{Code: 3, Msg: ue.Error(), Remedy: remedy}
}

// assignIdentity decides who the client is. A host client's identity is
// the kernel's uid through peer credentials and any claim in the hello is
// ignored; only the named token and the ephemeral declaration are things a
// client asserts.
func (h *Handler) assignIdentity(peer Peer, hello *protocol.Hello) (Identity, *protocol.Error) {
	switch hello.Kind {
	case protocol.KindHost:
		if !peer.Known {
			return Identity{}, &protocol.Error{
				Code:   3,
				Msg:    "host identity cannot be verified: peer credentials are unavailable on this connection",
				Remedy: "run the client on the host, or declare itself ephemeral (WT_CLIENT_EPHEMERAL=1) or named (a token) instead",
			}
		}
		return Identity{Kind: protocol.KindHost, Key: strconv.Itoa(peer.UID)}, nil
	case protocol.KindNamed:
		if hello.Token == "" {
			return Identity{}, &protocol.Error{
				Code:   3,
				Msg:    "a named container client must present a token",
				Remedy: "configure the token into the image and present it in the hello",
			}
		}
		return Identity{Kind: protocol.KindNamed, Key: hello.Token}, nil
	case protocol.KindEphemeral:
		return Identity{Kind: protocol.KindEphemeral, Key: newSessionID(), Ephemeral: true}, nil
	default:
		return Identity{}, &protocol.Error{
			Code:   3,
			Msg:    fmt.Sprintf("unknown client kind %q", hello.Kind),
			Remedy: "upgrade wt: this coordinator knows the kinds host, named and ephemeral",
		}
	}
}

// observe records one connection in clients.json. Last-seen is the
// coordinator's own clock — a measured time, never something a client
// wrote (ARCHITECTURE.md §10.2).
func (h *Handler) observe(id Identity) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	f, err := h.st.ReadClients()
	if err != nil {
		return err
	}
	now := time.Now().UTC().Format(time.RFC3339Nano)
	replaced := false
	for i := range f.Clients {
		if f.Clients[i].Kind == id.Kind && f.Clients[i].Identity == id.Key {
			f.Clients[i].LastSeen = now
			replaced = true
			break
		}
	}
	if !replaced {
		f.Clients = append(f.Clients, store.ClientEntry{
			Identity:  id.Key,
			Kind:      id.Kind,
			LastSeen:  now,
			Ephemeral: id.Ephemeral,
		})
	}
	return h.st.WriteClients(f)
}

// newSessionID issues the session id the coordinator gives an ephemeral
// client — random, unforgeable, and the identity its entries are marked
// reclaimable under.
func newSessionID() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand cannot fail on the platforms this project targets;
		// a fixed fallback would make session ids guessable.
		panic(fmt.Sprintf("coord: crypto/rand failed: %v", err))
	}
	return "s" + hex.EncodeToString(b)
}
