package coord

import (
	"bufio"
	"context"
	"fmt"
	"log/slog"
	"net"
	"os"
	"sync"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

// Server is the resident coordinator: the socket listener and the
// per-connection protocol loop around a Handler. Its lifecycle is context
// cancellation with graceful shutdown — SIGINT and SIGTERM from the entry
// point, in-flight requests allowed to finish.
type Server struct {
	h   *Handler
	log *slog.Logger
	wg  sync.WaitGroup
}

// NewServer wraps a handler in the socket loop.
func NewServer(h *Handler, log *slog.Logger) *Server {
	if log == nil {
		log = slog.Default()
	}
	return &Server{h: h, log: log}
}

// Serve listens on socketPath and serves connections until ctx is
// cancelled, then lets in-flight requests finish and returns nil. The
// socket file is removed on exit, so a crashed coordinator never blocks
// the next one with a stale file (the stale check in ListenSocket covers
// the hard-crash case). The reserving-ageing sweeper runs for the life of
// the process — a resident process needs no scheduler for it
// (ARCHITECTURE.md §11.2).
func (s *Server) Serve(ctx context.Context, socketPath string) error {
	ln, err := platform.ListenSocket(socketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(socketPath)
	return s.ServeListener(ctx, ln)
}

// ServeWithTCP serves the platform listener at socketPath and, alongside
// it, the opt-in loopback TCP listener at tcpAddr — phase 9's transport
// for hosts where a socket cannot be shared into a container (Docker
// Desktop's virtiofs cannot carry a live unix socket). The TCP listener is
// the same protocol loop, marked TCP so the handler requires the
// configured token on every connection: peer credentials do not exist on a
// TCP connection, which is the whole reason the surface stays opt-in
// (docs/ARCHITECTURE.md §4.1, §12.2). The handler must carry the token in
// TCPToken; the entry point refuses to start the pair without one.
func (s *Server) ServeWithTCP(ctx context.Context, socketPath, tcpAddr string) error {
	ln, err := platform.ListenSocket(socketPath)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(socketPath)
	tln, err := net.Listen("tcp", tcpAddr)
	if err != nil {
		return fmt.Errorf("listening on loopback TCP %s: %w", tcpAddr, err)
	}
	defer tln.Close()
	s.log.Info("TCP listener open", "addr", tln.Addr().String())
	return s.serveListeners(ctx, serveListener{ln: ln}, serveListener{ln: tln, tcp: true})
}

// serveListener is one listener the server accepts on, with the flag that
// marks the opt-in TCP surface (the token-authenticated one).
type serveListener struct {
	ln  net.Listener
	tcp bool
}

// ServeListener serves an already-created listener — the unix socket
// `Serve` opens, or the descriptor systemd handed over under socket
// activation (platform.ActivatedListener, systemd_linux.go). Nothing is
// removed on exit: a listener this path does not own stays in place (the
// systemd socket unit keeps its socket file; Serve's own defer handles
// the path it created).
func (s *Server) ServeListener(ctx context.Context, ln net.Listener) error {
	return s.serveListeners(ctx, serveListener{ln: ln})
}

// serveListeners runs the accept loops for one or more listeners (the
// platform listener, and the opt-in TCP listener when configured) until
// ctx is cancelled, then lets in-flight requests finish. The sweeper
// starts once, for the life of the process.
func (s *Server) serveListeners(ctx context.Context, lns ...serveListener) error {
	s.startSweeper(ctx)

	// A non-cancellation accept error on any listener stops the whole
	// server: the coordinator either serves every surface it was asked to
	// or none (refuse rather than partially honour).
	stop := make(chan struct{})
	var once sync.Once
	closeAll := func() { once.Do(func() { close(stop) }) }
	go func() {
		select {
		case <-ctx.Done():
			closeAll()
		case <-stop:
		}
		for _, l := range lns {
			l.ln.Close()
		}
	}()

	errCh := make(chan error, len(lns))
	for _, l := range lns {
		s.wg.Add(1)
		go func(l serveListener) {
			defer s.wg.Done()
			for {
				conn, err := l.ln.Accept()
				if err != nil {
					if ctx.Err() != nil {
						return
					}
					select {
					case errCh <- fmt.Errorf("accepting on %s: %w", l.ln.Addr(), err):
					default:
					}
					closeAll()
					return
				}
				s.wg.Add(1)
				go func() {
					defer s.wg.Done()
					s.serveConn(ctx, conn, l.tcp)
				}()
			}
		}(l)
	}
	s.wg.Wait() // in-flight requests allowed to finish
	close(errCh)
	for err := range errCh {
		if err != nil {
			return err
		}
	}
	return nil
}

// sweepInterval is how often the resident coordinator ages reserving
// entries out. A stale reservation blocks a slot for at most one interval,
// which is the whole cost of not sweeping lazily inside allocate.
const sweepInterval = time.Minute

// startSweeper runs the coordinator's own timers for the life of the
// process: the reserving-ageing timer (a reserving entry past its timeout
// is dropped, which covers a client that died mid-sequence,
// ARCHITECTURE.md §11.2), reclamation — aged-out ephemeral clients'
// entries are torn down by handle on the reclamation interval
// (ARCHITECTURE.md §10.2, phase 6) — and the scheduled cleanup sweep, the
// resident process's replacement for an OS scheduler job (06-fleet.md
// §7.2, revision 2). All three stop when ctx is done and none is tracked
// by the wait group — they hold no in-flight request.
func (s *Server) startSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(sweepInterval)
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
				// connection, so last-seen is real, and the interval is
				// the phase-6 choice (ReclaimIntervalDefault).
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

// helloDeadline bounds how long a connection may sit without sending its
// hello. The client sends the hello immediately after dialing, so ten
// seconds is generous; the deadline is what keeps a stalled connection —
// a client that connected and sent nothing, the slowloris shape — from
// holding a goroutine and a buffer indefinitely, and what lets a shutdown
// finish promptly when one exists (the security pass, phase 9). It is
// cleared once the hello arrives: an authenticated client may legitimately
// idle between requests for minutes (init's build hooks run client-side
// between materialise and activate), so no per-request deadline is set
// after it.
const helloDeadline = 10 * time.Second

// serveConn runs one connection's protocol loop: hello, then requests until
// the client closes. tcp marks a connection from the opt-in loopback TCP
// listener, where the handler requires the configured token.
func (s *Server) serveConn(ctx context.Context, conn net.Conn, tcp bool) {
	defer conn.Close()
	peer := Peer{TCP: tcp}
	if uid, err := platform.PeerUID(conn); err == nil {
		peer = Peer{UID: uid, Known: true, TCP: tcp}
	} else {
		s.log.Debug("peer credentials unavailable on a connection", "tcp", tcp, "err", err)
	}

	if err := conn.SetReadDeadline(time.Now().Add(helloDeadline)); err != nil {
		s.log.Debug("setting the hello deadline", "err", err)
	}
	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	var hello protocol.Hello
	if err := protocol.ReadMessage(br, &hello); err != nil {
		s.log.Debug("reading the hello", "tcp", tcp, "err", err)
		return
	}
	if err := conn.SetReadDeadline(time.Time{}); err != nil {
		s.log.Debug("clearing the hello deadline", "err", err)
	}
	sess, reply := s.h.Begin(peer, &hello)
	if err := protocol.WriteMessage(bw, reply); err != nil {
		return
	}
	if err := bw.Flush(); err != nil {
		return
	}
	if reply.Error != nil {
		s.log.Info("connection refused", "kind", hello.Kind, "err", reply.Error.Msg)
		return
	}
	s.log.Debug("connection established",
		"kind", sess.Identity.Kind, "identity", sess.Identity.Key, "protocol", sess.Version)

	for {
		var req protocol.Request
		if err := protocol.ReadMessage(br, &req); err != nil {
			return // EOF: the client is done
		}
		resp := s.h.Handle(ctx, sess, &req)
		if err := protocol.WriteMessage(bw, resp); err != nil {
			return
		}
		if err := bw.Flush(); err != nil {
			return
		}
	}
}
