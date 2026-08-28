// Package coord is the coordinator's request core and resident process:
// the handler that turns API requests into responses, the HTTP server
// that serves them, and the in-process harness phases 3 to 6 lean on
// (ARCHITECTURE.md §13.3).
//
// One writer serialises here: there is no lock file, no generation
// counter, no view identity and no view scoping (revision 2 deleted all
// four) — concurrent requests serialise in this one process, and the
// handler's mutex makes that true across its connection goroutines.
package coord

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Identity is the coordinator's identity for one client, resolved per
// request from the bearer token — never from a claim in the request
// (ARCHITECTURE.md §10.2). Key is "host" for a host client, the token for
// a named container, and the issued session id for an ephemeral one.
type Identity struct {
	Kind      string
	Key       string
	Ephemeral bool
}

// hostIdentityKey is the identity key of every host client. Reading the
// 0600 endpoint.json in one's own home is the proof of host identity now
// that peer credentials are gone (plan.md §7, "Host identity is weaker
// than SO_PEERCRED"): the key is the same for every host process of the
// user, which is the whole of what the old uid check proved.
const hostIdentityKey = "host"

// Session is one request's identity context: the API version the
// coordinator speaks and the identity resolved from the request's bearer
// token. It is per request now — there is no connection to hold it.
type Session struct {
	Version  int
	Identity Identity
}

// Handler is the coordinator's request core: API requests in, API
// responses out, a store path, and no listener, supervisor or container —
// exactly the in-process harness of ARCHITECTURE.md §13.3.
type Handler struct {
	// ProtocolMin/ProtocolMax are the coordinator's advertised API version
	// range, the api package's constants by default. They are fields so
	// the refusal path is testable from both directions (a coordinator
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

	// LookHelper is how doctor asks whether the coordinator can reach a
	// helper binary. It is the same resolution every driver uses —
	// platform.LookHelper: this process's PATH, then the known install
	// directories — asked in the coordinator's own process, because the
	// coordinator's environment is the one that decides. Nil means
	// platform.LookHelper; a test installs a fake.
	LookHelper func(string) (string, error)

	// Gh is the scheduled sweep's gh seam: the coordinator shells out to
	// gh with the given working directory ("" for none), the way the
	// interactive verb does. Nil means the real gh runner; tests install
	// fakes.
	Gh func(dir string, args ...string) ([]byte, error)

	// Token is the host token, the bearer that authenticates a host
	// client: the token from the 0600 endpoint.json, generated on first
	// start and reused thereafter. The server loads or creates it before
	// serving; a handler with an empty token refuses every host bearer.
	Token string

	// ContainerToken is the token wtd was started with (--container-token,
	// 16+ characters), the admission credential for containers: a bearer
	// equal to it is a named client whose identity key is the token, and
	// it is what POST /v1/session accepts to issue an ephemeral session
	// id. Empty means containers are not admitted at all — the coordinator
	// accepts host clients only, exactly as the old TCP surface was
	// opt-in (plan.md §5, phase R1).
	ContainerToken string

	st  *store.Store
	log *slog.Logger

	// mu serialises every store-touching operation: connections arrive on
	// their own goroutines, and load-modify-save of the registry, the band
	// ledger and clients.json is coordinator-internal write work that must
	// not race itself. One writer serialises here — there is no lock file,
	// no generation counter, no compare-and-swap retry; concurrent requests
	// serialise in this one process (revision 2, plan.md §2).
	//
	// It is released across driver work, which can take minutes; the entry
	// is held by a claim instead (claim.go).
	mu sync.Mutex

	// claims are the entries with a long operation in flight, guarded by
	// mu. See claim.go.
	claims map[entryKey]bool
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
		ProtocolMin: api.VersionMin,
		ProtocolMax: api.VersionMax,
		Probe:       noProbe,
		claims:      map[entryKey]bool{},
		st:          st,
		log:         log,
	}, nil
}

// Store returns the handler's store.
func (h *Handler) Store() *store.Store { return h.st }

// ResolveIdentity is the per-request identity resolution, replacing the
// old hello's assignIdentity: the bearer token is the whole of identity
// over HTTP, because peer credentials do not exist on a TCP connection
// (plan.md §5, phase R1). A host bearer is the token from the 0600
// endpoint.json, a named bearer the coordinator's --container-token, and
// an ephemeral bearer an issued session id — a clients row, so the
// identity survives a wtd restart mid-init and reclamation deleting the
// row is what revokes it. Missing, wrong and wrong-kind are one identical
// refusal, compared in constant time: the API must not be an oracle that
// distinguishes them.
func (h *Handler) ResolveIdentity(bearer string) (Identity, *api.Error) {
	switch {
	case h.Token != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(h.Token)) == 1:
		return Identity{Kind: api.KindHost, Key: hostIdentityKey}, nil
	case h.ContainerToken != "" && subtle.ConstantTimeCompare([]byte(bearer), []byte(h.ContainerToken)) == 1:
		// The token is the identity key: it names the client in the
		// registry and is what the ownership check compares (the key is a
		// credential and is redacted everywhere it is served — redact.go).
		return Identity{Kind: api.KindNamed, Key: bearer}, nil
	case h.ephemeralSession(bearer):
		return Identity{Kind: api.KindEphemeral, Key: bearer, Ephemeral: true}, nil
	default:
		return Identity{}, authRefusal()
	}
}

// OpenSession issues an ephemeral session: a clients row whose identity
// is a 32-byte random value, returned to the client as its bearer for
// every later request. Session ids are rows, not memory — they survive a
// wtd restart, and reclamation deleting the row is what revokes them
// (plan.md §5, phase R1). The caller (the HTTP layer) has already
// verified the container token.
func (h *Handler) OpenSession() (Identity, *api.Error) {
	id := Identity{Kind: api.KindEphemeral, Key: newSessionID(), Ephemeral: true}
	if err := h.observe(id); err != nil {
		return Identity{}, h.observeFailure(err)
	}
	return id, nil
}

// observeFailure is the refusal when the coordinator's own store cannot
// record a client: required context unavailable, naming the store.
func (h *Handler) observeFailure(err error) *api.Error {
	h.log.Error("recording the client in the store", "err", err)
	return &api.Error{
		Code:   4, // required context unavailable: the coordinator's own store is broken
		Msg:    fmt.Sprintf("the coordinator cannot record the client: %v", err),
		Remedy: "check the coordinator's store (WT_HOME) is writable, then re-run",
	}
}

// ephemeralSession reports whether bearer is a live ephemeral session id:
// a clients row of kind ephemeral. The table is small — clients, not
// entries — and the lookup is the whole of the ephemeral identity check.
func (h *Handler) ephemeralSession(bearer string) bool {
	if bearer == "" {
		return false
	}
	clients, err := h.st.ReadClients()
	if err != nil {
		return false
	}
	for _, c := range clients.Clients {
		if c.Kind == api.KindEphemeral && c.Identity == bearer {
			return true
		}
	}
	return false
}

// authRefusal is the single refusal for a missing, wrong or wrong-kind
// bearer: one message, so the API cannot be used as an oracle to test
// tokens one at a time, and the remedy names the command that fixes it
// (plan.md §3's every-error-names-the-command rule).
func authRefusal() *api.Error {
	return &api.Error{
		Code: 3,
		Msg:  "authentication refused: this request must present the coordinator's bearer token",
		Remedy: "start wtd (wt daemon install, or wtd --addr 127.0.0.1:7833) so it writes endpoint.json, and re-run as the host user; " +
			"a container sets WT_CLIENT_TOKEN to the coordinator's --container-token instead",
	}
}

// Handle runs one request and returns its response. The response carries
// either a result or an error with the exit code the client should use, so
// codes 3, 4 and 5 originate here and reach the process exit status
// unchanged (ARCHITECTURE.md §11.3).
func (h *Handler) Handle(ctx context.Context, s *Session, req *api.Request) *api.Response {
	switch req.Verb {
	case "ping":
		// The protocol plumbing's one verb: a full request round trip with
		// nothing behind it. Not a wt command — later phases add the real
		// verbs on the same dispatch. Phase R2: the answer is a typed
		// result, so the described API derives it like every other result.
		return &api.Response{Result: mustJSON(api.PingResult{OK: true})}
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
		return &api.Response{Error: &api.Error{
			Code: 1,
			Msg:  fmt.Sprintf("unknown verb %q", req.Verb),
			// The system refuses rather than partially honouring: an
			// unknown verb is a newer client, and the remedy names the
			// upgrade (02-coordination.md §11).
			Remedy: fmt.Sprintf("upgrade wt: this coordinator speaks protocol %d and knows no verb %q", s.Version, req.Verb),
		}}
	}
}

// observe records one request in the client table. Last-seen is the
// coordinator's own clock — a measured time, never something a client
// wrote (ARCHITECTURE.md §10.2). The upsert is the whole operation: a
// returning client's row is refreshed in place, a new client's row is
// inserted, in one statement. It runs per request now that there is no
// connection to observe once.
func (h *Handler) observe(id Identity) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	return h.st.UpsertClient(store.ClientEntry{
		Identity:  id.Key,
		Kind:      id.Kind,
		LastSeen:  time.Now().UTC().Format(time.RFC3339Nano),
		Ephemeral: id.Ephemeral,
	})
}

// newSessionID issues the session id the coordinator gives an ephemeral
// client — 32 random bytes, unforgeable, and the identity its entries are
// marked reclaimable under (plan.md §5, phase R1: a 32-byte random
// identity).
func newSessionID() string {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand cannot fail on the platforms this project targets;
		// a fixed fallback would make session ids guessable.
		panic(fmt.Sprintf("coord: crypto/rand failed: %v", err))
	}
	return hex.EncodeToString(b)
}
