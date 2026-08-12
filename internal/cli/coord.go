package cli

import (
	"bufio"
	"encoding/json"
	"fmt"
	"net"
	"os"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

// coordSession is an open, hello-completed connection to the coordinator,
// carrying the negotiated protocol version and the connection's reader and
// writer (one per session, so buffered reads never lose bytes between
// requests).
type coordSession struct {
	conn    net.Conn
	br      *bufio.Reader
	bw      *bufio.Writer
	version int
}

// Close ends the connection.
func (s *coordSession) Close() { s.conn.Close() }

// Version is the protocol version the coordinator agreed to.
func (s *coordSession) Version() int { return s.version }

// dialCoordinator is the client's one dial-and-hello path. Every verb that
// reaches the coordinator goes through it, so exit code 5 — coordinator
// unreachable — is wired in exactly one place, with the platform's start
// command as the remedy (ARCHITECTURE.md §11.3: code 5 says start or mount
// the coordinator; code 4 says a capability the coordinator itself depends
// on is missing). There is no local fallback for any verb (ARCHITECTURE.md
// §4.4): a client that cannot reach the coordinator fails loudly.
//
// The hello carries the client's protocol range and identity declaration;
// a refusal (a version mismatch, in either direction) comes back with the
// upgrade named, and its exit code reaches the process exit status
// unchanged.
func dialCoordinator() (*coordSession, *Error) {
	path, err := platform.SocketPath()
	if err != nil {
		return nil, New(ExitUnreachable, err.Error(), platform.CoordinatorStartCommand(""))
	}
	conn, err := platform.DialSocket(path)
	if err != nil {
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator unreachable at %s: %v", path, err),
			platform.CoordinatorStartCommand(path))
	}
	bw := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	kind, token, kerr := clientIdentity()
	if kerr != nil {
		conn.Close()
		return nil, kerr
	}
	hello := &protocol.Hello{
		Kind:   kind,
		Token:  token,
		MinVer: protocol.VersionMin,
		MaxVer: protocol.VersionMax,
	}
	if err := protocol.WriteMessage(bw, hello); err != nil {
		conn.Close()
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator at %s closed during the hello: %v", path, err),
			platform.CoordinatorStartCommand(path))
	}
	if err := bw.Flush(); err != nil {
		conn.Close()
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator at %s closed during the hello: %v", path, err),
			platform.CoordinatorStartCommand(path))
	}
	var reply protocol.HelloReply
	if err := protocol.ReadMessage(br, &reply); err != nil {
		conn.Close()
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator at %s did not answer the hello: %v", path, err),
			platform.CoordinatorStartCommand(path))
	}
	if reply.Error != nil {
		conn.Close()
		// The coordinator's refusal carries its own exit code — 3 for a
		// refused hello — and its own remedy naming the upgrade.
		return nil, New(reply.Error.Code, reply.Error.Msg, reply.Error.Remedy)
	}
	if reply.Agreed < protocol.VersionMin || reply.Agreed > protocol.VersionMax {
		conn.Close()
		return nil, New(ExitUnreachable,
			fmt.Sprintf("coordinator at %s agreed an impossible protocol version %d", path, reply.Agreed),
			platform.CoordinatorStartCommand(path))
	}
	return &coordSession{conn: conn, br: br, bw: bw, version: reply.Agreed}, nil
}

// request sends one request and returns the decoded result, mapping a
// response error onto the client's exit-code error so the coordinator's
// codes 3, 4 and 5 reach the process exit status unchanged.
func (s *coordSession) request(verb string, args any) (json.RawMessage, *Error) {
	req := protocol.Request{Verb: verb}
	if args != nil {
		raw, err := json.Marshal(args)
		if err != nil {
			return nil, New(ExitFailure, fmt.Sprintf("encoding the %s request: %v", verb, err), "")
		}
		req.Args = raw
	}
	bw := s.bw
	br := s.br
	if err := protocol.WriteMessage(bw, &req); err != nil {
		return nil, New(ExitUnreachable, fmt.Sprintf("coordinator closed mid-request: %v", err), "")
	}
	if err := bw.Flush(); err != nil {
		return nil, New(ExitUnreachable, fmt.Sprintf("coordinator closed mid-request: %v", err), "")
	}
	var resp protocol.Response
	if err := protocol.ReadMessage(br, &resp); err != nil {
		return nil, New(ExitUnreachable, fmt.Sprintf("coordinator closed before the response: %v", err), "")
	}
	if resp.Error != nil {
		return nil, New(resp.Error.Code, resp.Error.Msg, resp.Error.Remedy)
	}
	return resp.Result, nil
}

// clientIdentity is the client's identity declaration: the kind and, for a
// named container, the token. WT_CLIENT_TOKEN (the fifth environment
// variable, added in phase 6) declares the named kind — a token the
// operator configures into the image, which names a capability rather than
// a policy, so it passes the ambient-value test of ARCHITECTURE.md §12.3:
// a child process inheriting it is still the same named container.
// WT_CLIENT_EPHEMERAL=1 declares the ephemeral kind — every process in a
// disposable-clone image is in a disposable clone, which is the same test
// passing for the same reason. Setting both is ambiguous — the two kinds
// grant different reclamation lives — and the system refuses rather than
// partially honouring, so the client stops with a usage error naming the
// choice. Otherwise the client is a host client, whose identity the
// coordinator reads from peer credentials; nothing a client writes can
// claim that.
func clientIdentity() (kind, token string, err *Error) {
	if t := os.Getenv("WT_CLIENT_TOKEN"); t != "" {
		if os.Getenv("WT_CLIENT_EPHEMERAL") == "1" {
			return "", "", New(ExitUsage,
				"WT_CLIENT_TOKEN and WT_CLIENT_EPHEMERAL=1 are both set: a token names a persistent client and the ephemeral declaration marks its entries reclaimable — the two cannot both be true",
				"unset one of WT_CLIENT_TOKEN or WT_CLIENT_EPHEMERAL, then re-run")
		}
		return protocol.KindNamed, t, nil
	}
	if os.Getenv("WT_CLIENT_EPHEMERAL") == "1" {
		return protocol.KindEphemeral, "", nil
	}
	return protocol.KindHost, "", nil
}
