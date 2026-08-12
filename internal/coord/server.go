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
	s.startSweeper(ctx)

	go func() {
		<-ctx.Done()
		ln.Close() // unblocks Accept; in-flight connections keep running
	}()

	for {
		conn, err := ln.Accept()
		if err != nil {
			if ctx.Err() != nil {
				break // shutting down: stop accepting, drain
			}
			return fmt.Errorf("accepting on %s: %w", socketPath, err)
		}
		s.wg.Add(1)
		go func() {
			defer s.wg.Done()
			s.serveConn(ctx, conn)
		}()
	}
	s.wg.Wait() // in-flight requests allowed to finish
	return nil
}

// sweepInterval is how often the resident coordinator ages reserving
// entries out. A stale reservation blocks a slot for at most one interval,
// which is the whole cost of not sweeping lazily inside allocate.
const sweepInterval = time.Minute

// startSweeper runs the reserving-ageing timer for the life of the process:
// a reserving entry past its timeout is dropped, which covers a client that
// died mid-sequence (ARCHITECTURE.md §11.2). It stops when ctx is done and
// is deliberately not tracked by the wait group — it holds no in-flight
// request.
func (s *Server) startSweeper(ctx context.Context) {
	go func() {
		t := time.NewTicker(sweepInterval)
		defer t.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-t.C:
				n, err := s.h.AgeReserving(time.Now(), ReservingTimeout)
				if err != nil {
					s.log.Warn("ageing reserving entries", "err", err)
					continue
				}
				if n > 0 {
					s.log.Info("aged out reserving entries", "count", n)
				}
			}
		}
	}()
}

// serveConn runs one connection's protocol loop: hello, then requests until
// the client closes.
func (s *Server) serveConn(ctx context.Context, conn net.Conn) {
	defer conn.Close()
	peer := Peer{}
	if uid, err := platform.PeerUID(conn); err == nil {
		peer = Peer{UID: uid, Known: true}
	} else {
		s.log.Warn("peer credentials unavailable on a connection", "err", err)
	}

	br := bufio.NewReader(conn)
	bw := bufio.NewWriter(conn)
	var hello protocol.Hello
	if err := protocol.ReadMessage(br, &hello); err != nil {
		s.log.Debug("reading the hello", "err", err)
		return
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
