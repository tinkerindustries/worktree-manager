package coord

import (
	"context"
	"crypto/rand"
	"crypto/subtle"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"syscall"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
)

// Server is the resident coordinator: the loopback HTTP listener and the
// request loop around a Handler. Its lifecycle is context cancellation
// with graceful shutdown — SIGINT and SIGTERM from the entry point,
// in-flight requests allowed to finish.
type Server struct {
	h   *Handler
	log *slog.Logger

	// addr is the address the server listens on, used by the Host-header
	// check: a Host that is neither loopback nor this address is refused
	// (the DNS-rebinding guard). It is the listener's real address, so
	// socket activation (--activate) gets the same guard as a foreground
	// listen.
	addr string
}

// NewServer wraps a handler in the HTTP loop.
func NewServer(h *Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{h: h, log: log}
}

// Serve listens on addr and serves requests until ctx is cancelled, then
// lets in-flight requests finish and returns nil. A port already in use
// is refused at startup, naming the port and --addr — the coordinator
// refuses a taken port rather than guessing (plan.md §7, "Two users
// collide on 7833"). The reserving-ageing sweeper runs for the life of
// the process (ARCHITECTURE.md §11.2).
func (s *Server) Serve(ctx context.Context, addr string) error {
	ln, err := net.Listen("tcp", addr)
	if err != nil {
		if errors.Is(err, syscall.EADDRINUSE) {
			return fmt.Errorf("cannot listen on --addr %s: the port is already in use — stop the process holding it, or pass --addr with a different port", addr)
		}
		return fmt.Errorf("listening on --addr %s: %w", addr, err)
	}
	return s.ServeListener(ctx, ln)
}

// ServeListener serves an already-created listener — the one `Serve`
// opened, or the descriptor systemd handed over under socket activation
// (platform.ActivatedListener, systemd_linux.go): systemd passes a TCP
// listener through LISTEN_FDS exactly as it passed a unix one, so on-
// demand start survives (plan.md §5, phase R1). Nothing is removed on
// exit: a listener this path does not own stays in place.
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	// The endpoint file must exist before the first request is served:
	// the host token lives in it and the client reads it to
	// authenticate. Load or create it (token reused across restarts),
	// then serve.
	if err := s.ensureEndpoint(ln.Addr()); err != nil {
		return err
	}
	s.addr = ln.Addr().String()
	s.startSweeper(ctx)

	srv := &http.Server{
		Handler:           s,
		ReadHeaderTimeout: api.ReadHeaderTimeout,
	}
	// Graceful shutdown: on cancellation, Shutdown lets in-flight
	// requests finish (plan.md §5, phase R1). The old wait-group accept
	// loop is gone.
	shutdownDone := make(chan struct{})
	go func() {
		defer close(shutdownDone)
		select {
		case <-ctx.Done():
		case <-shutdownDone:
			return
		}
		shutCtx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		if err := srv.Shutdown(shutCtx); err != nil {
			s.log.Warn("graceful shutdown", "err", err)
		}
	}()
	defer func() { <-shutdownDone }()

	err := srv.Serve(ln)
	if err == http.ErrServerClosed {
		return nil
	}
	return err
}

// ensureEndpoint loads or creates the endpoint file at <store>/endpoint.json
// and installs the host token on the handler. The token is generated with
// crypto/rand on first start and reused thereafter; the base URL is
// rewritten to the actual bound address, so --addr and the endpoint file
// cannot drift. A malformed or newer-schema file is refused at startup,
// naming the field.
func (s *Server) ensureEndpoint(addr net.Addr) error {
	path := api.EndpointPath(s.h.Store().Root())
	ep, err := api.ReadEndpoint(path)
	switch {
	case err == nil:
		// Reuse the existing token.
	case os.IsNotExist(err):
		tok, terr := newHostToken()
		if terr != nil {
			return terr
		}
		ep = &api.Endpoint{SchemaVersion: api.EndpointSchemaVersion, Token: tok}
	default:
		return fmt.Errorf("the endpoint file %s is unusable: %w", path, err)
	}
	// A wildcard bind (--allow-remote 0.0.0.0) is not something a local
	// client can dial as written; the file always names the loopback
	// form, which the host user's own client uses.
	host, port, perr := net.SplitHostPort(addr.String())
	if perr == nil && (host == "" || host == "0.0.0.0" || host == "::") {
		host = "127.0.0.1"
		addr = &net.TCPAddr{IP: net.ParseIP(host), Port: mustAtoi(port)}
	}
	ep.BaseURL = "http://" + addr.String()
	if err := api.WriteEndpoint(path, ep); err != nil {
		return err
	}
	s.h.Token = ep.Token
	s.log.Info("serving endpoint", "file", path, "base_url", ep.BaseURL, "token", "set")
	return nil
}

// newHostToken is the endpoint token: 32 random bytes, hex — the 64-hex
// token the endpoint file carries.
func newHostToken() (string, error) {
	b := make([]byte, 32)
	if _, err := rand.Read(b); err != nil {
		return "", fmt.Errorf("generating the endpoint token: %w", err)
	}
	return hex.EncodeToString(b), nil
}

func mustAtoi(s string) int {
	var n int
	fmt.Sscanf(s, "%d", &n)
	return n
}

// sweeperTick is how often the resident coordinator's timer fires. It is
// the ageing timer's own interval — a stale reservation blocks a slot for
// at most one tick, which is the whole cost of not sweeping lazily inside
// allocate — and the throttle the slower timers hang off. Handler's
// sweepInterval method is a different period: how often the cleanup sweep
// runs.
const sweeperTick = time.Minute

// startSweeper runs the coordinator's own timers for the life of the
// process: the reserving-ageing timer (a reserving entry past its timeout
// is dropped, which covers a client that died mid-sequence,
// ARCHITECTURE.md §11.2), reclamation — aged-out ephemeral clients'
// entries are torn down by handle on the reclamation interval
// (ARCHITECTURE.md §10.2, phase 6) — and the scheduled cleanup sweep, the
// resident process's replacement for an OS scheduler job (06-fleet.md
// §7.2, revision 2). All three stop when ctx is done and none is tracked
// by a wait group — they hold no in-flight request.
func (s *Server) startSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(sweeperTick)
		defer t.Stop()
		lastReclaim := time.Now()
		lastSweep := time.Now()
		for {
			select {
			case <-ctx.Done():
				return
			case now := <-t.C:
				n, err := s.h.AgeReserving(now, ReservingTimeout)
				if err != nil {
					s.log.Warn("ageing reserving entries", "err", err)
					continue
				}
				if n > 0 {
					s.log.Info("aged out reserving entries", "count", n)
				}
				// Reclamation runs when a full interval has passed since
				// the previous run; the coordinator measures every
				// request, so last-seen is real, and the interval is the
				// phase-6 choice (ReclaimIntervalDefault).
				if now.Sub(lastReclaim) >= s.h.reclaimInterval() {
					reclaimed, rerr := s.h.ReclaimEphemeral(now)
					if rerr != nil {
						s.log.Warn("reclaiming aged-out ephemeral clients", "err", rerr)
					} else if reclaimed > 0 {
						s.log.Info("reclaimed aged-out ephemeral clients' entries", "count", reclaimed)
					}
					lastReclaim = now
				}
				// The cleanup sweep runs on its own interval, more
				// conservatively than the interactive verb: it never
				// touches another client's entries, and it logs every
				// skip (06-fleet.md §7.2).
				if now.Sub(lastSweep) >= s.h.sweepInterval() {
					cleaned, serr := s.h.SweepCleanup()
					if serr != nil {
						s.log.Warn("scheduled cleanup", "err", serr)
					} else if cleaned > 0 {
						s.log.Info("scheduled cleanup cleaned entries", "count", cleaned)
					}
					lastSweep = now
				}
			}
		}
	}()
}

// ServeHTTP is the coordinator's whole HTTP surface, gated in order by
// the Host check (the DNS-rebinding guard), the X-Wt-Client requirement
// (a browser cannot set a custom header on a simple-form POST, so this
// forces a preflight that is never answered) and the bearer
// authentication. No CORS header is ever sent and no preflight is
// answered: an OPTIONS request hits the mux's method mismatch and dies
// there. The route table owns the paths; the mux is built from it.
func (s *Server) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if !s.hostAllowed(r.Host) {
		writeError(w, http.StatusForbidden, &api.Error{
			Code:   3,
			Msg:    fmt.Sprintf("refused: the Host header %q is neither loopback nor the coordinator's address %s", r.Host, s.addr),
			Remedy: "run the request against 127.0.0.1:7833 (or the address wtd is listening on), then re-run",
		})
		return
	}
	if r.Header.Get("X-Wt-Client") != "1" {
		writeError(w, http.StatusForbidden, &api.Error{
			Code:   3,
			Msg:    "refused: this API is for the wt client only, and every request must carry X-Wt-Client: 1",
			Remedy: "use the wt client, or send the X-Wt-Client: 1 header with the request, then re-run",
		})
		return
	}
	s.mux().ServeHTTP(w, r)
}

// mux is the route table as a ServeMux: GET /version (unauthenticated —
// it exists so a mismatched client learns it must upgrade) and the
// table's RPCs. Nothing else is routed: an unknown path or method is a
// plain 404/405, which a route-table-derived client cannot produce.
func (s *Server) mux() *http.ServeMux {
	mux := http.NewServeMux()
	mux.HandleFunc("GET "+api.VersionPath, s.handleVersion)
	for _, r := range api.Routes {
		r := r
		if r.Verb == api.VerbSession {
			mux.HandleFunc(r.Method+" "+r.Path, s.handleSession)
			continue
		}
		mux.HandleFunc(r.Method+" "+r.Path, s.handleRPC)
	}
	return mux
}

// handleVersion serves GET /version: the coordinator's supported version
// range, unauthenticated. A mismatched client reads it and refuses with a
// real upgrade message instead of a bare 404 (plan.md §5, phase R1).
func (s *Server) handleVersion(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.VersionInfo{Min: s.h.ProtocolMin, Max: s.h.ProtocolMax})
}

// handleSession serves POST /v1/session: the ephemeral admission. The
// bearer must be the container token — the same constant-time compare and
// the same refusal as every other endpoint, so the session path is not an
// oracle either — and the answer is a freshly issued session id, a
// clients row the client presents as its bearer from then on.
func (s *Server) handleSession(w http.ResponseWriter, r *http.Request) {
	bearer := bearerFrom(r)
	if s.h.ContainerToken == "" || subtleConstantTimeEqual(bearer, s.h.ContainerToken) != 1 {
		writeError(w, http.StatusForbidden, authRefusal())
		return
	}
	id, aerr := s.h.OpenSession()
	if aerr != nil {
		writeError(w, statusForCode(aerr.Code), aerr)
		return
	}
	s.log.Debug("issued an ephemeral session", "identity", redactKey(id.Kind, id.Key))
	writeResult(w, mustJSON(api.SessionResult{SessionID: id.Key}))
}

// handleRPC serves one route-table RPC: authenticate, decode the body
// into the Request envelope (the verb comes from the route table, never
// from the body), dispatch through Handle and write the Response. The
// fourteen verb methods behind Handle are untouched by the transport.
func (s *Server) handleRPC(w http.ResponseWriter, r *http.Request) {
	verb, _ := api.VerbForPath(r.URL.Path)
	// The body cap, preserving the old wire's MaxMessageBytes: an
	// oversized request fails fast instead of growing memory without
	// bound. The body is the verb's own args object — the wire is
	// POST /v1/<verb> with the args as the body, not an envelope — and
	// the verb comes from the route table, never from the body.
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, api.MaxMessageBytes))
	if err != nil {
		var mbe *http.MaxBytesError
		if errors.As(err, &mbe) {
			writeError(w, http.StatusBadRequest, &api.Error{
				Code:   2,
				Msg:    fmt.Sprintf("the request body exceeds the %d-byte cap", api.MaxMessageBytes),
				Remedy: "re-run the wt command that failed (the request is too large; if it keeps failing, report the verb)",
			})
			return
		}
		writeError(w, http.StatusBadRequest, &api.Error{
			Code:   2,
			Msg:    "reading the request body: " + err.Error(),
			Remedy: "re-run the wt command that failed",
		})
		return
	}
	var req api.Request
	req.Args = body
	req.Verb = verb
	sess, aerr := s.authenticate(w, r)
	if aerr != nil {
		writeError(w, http.StatusForbidden, aerr)
		return
	}
	resp := s.h.Handle(r.Context(), sess, &req)
	if resp.Error != nil {
		writeError(w, statusForCode(resp.Error.Code), resp.Error)
		return
	}
	writeResult(w, resp.Result)
}

// authenticate resolves the request's identity from its bearer token and
// records the observation. The session endpoint handles its own admission
// (it must accept the container token, not a session id).
func (s *Server) authenticate(w http.ResponseWriter, r *http.Request) (*Session, *api.Error) {
	bearer := bearerFrom(r)
	id, aerr := s.h.ResolveIdentity(bearer)
	if aerr != nil {
		return nil, aerr
	}
	if err := s.h.observe(id); err != nil {
		return nil, s.h.observeFailure(err)
	}
	// The identity key is logged redacted: for a named client it is the
	// token and for an ephemeral one a session id — a log line must not
	// carry a credential (the security pass, phase 9).
	s.log.Debug("request authenticated", "kind", id.Kind, "identity", redactKey(id.Kind, id.Key))
	return &Session{Version: s.h.ProtocolMax, Identity: id}, nil
}

// bearerFrom extracts the bearer token from the Authorization header.
// Anything but exactly "Bearer <token>" is an empty token, which fails
// the constant-time compare and gets the one refusal.
func bearerFrom(r *http.Request) string {
	const prefix = "Bearer "
	h := r.Header.Get("Authorization")
	if len(h) > len(prefix) && strings.EqualFold(h[:len(prefix)], prefix) {
		return h[len(prefix):]
	}
	return ""
}

// subtleConstantTimeEqual is the one token-comparison primitive, named so
// the call sites read as what they are.
func subtleConstantTimeEqual(a, b string) int {
	return subtle.ConstantTimeCompare([]byte(a), []byte(b))
}

// statusForCode maps a response error's exit code onto the advisory HTTP
// status: 400→code 2, 403→code 3, 424→code 4, 500→code 1. The response
// body is authoritative for the exit code; the status is for anything
// reading the API that is not wt. Exit code 5 is client-side only and
// never appears here.
func statusForCode(code int) int {
	switch code {
	case 2:
		return http.StatusBadRequest
	case 3:
		return http.StatusForbidden
	case 4:
		return http.StatusFailedDependency // 424
	case 1:
		return http.StatusInternalServerError
	default:
		return http.StatusInternalServerError
	}
}

// writeError writes the one error body shape with the advisory status:
// {"error": {"code": N, "msg": "...", "remedy": "..."}}.
func writeError(w http.ResponseWriter, status int, e *api.Error) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(api.Response{Error: e})
}

// writeResult writes a successful Response envelope carrying the raw
// result.
func writeResult(w http.ResponseWriter, result json.RawMessage) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(api.Response{Result: result})
}

// hostAllowed is the DNS-rebinding guard: the Host header must be a
// loopback host or the server's own address. A browser page can send
// requests to 127.0.0.1 but cannot set the Host header to match — the
// guard, the bearer, X-Wt-Client and the unanswered preflight together
// keep a browser page out of the API (plan.md §7, "A browser page
// reaches the API").
func (s *Server) hostAllowed(host string) bool {
	hostOnly := host
	if h, _, err := net.SplitHostPort(host); err == nil {
		hostOnly = h
	}
	hostOnly = strings.ToLower(strings.Trim(hostOnly, "[]"))
	if isLoopbackHost(hostOnly) {
		return true
	}
	if s.addr != "" {
		addrHost := s.addr
		if h, _, err := net.SplitHostPort(s.addr); err == nil {
			addrHost = h
		}
		if strings.ToLower(strings.Trim(addrHost, "[]")) == hostOnly {
			return true
		}
	}
	return false
}

// isLoopbackHost reports whether host names a loopback address: the
// literal 127.0.0.0/8, ::1, or the name "localhost".
func isLoopbackHost(host string) bool {
	if host == "localhost" || host == "::1" {
		return true
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return ip.IsLoopback()
}
