package coord

// tcp_test.go pins the opt-in loopback TCP surface's identity rules
// (phase 9, docs/ARCHITECTURE.md §4.1): the token is the whole of identity
// on a TCP connection, because peer credentials do not exist there. The
// listener accepts named clients presenting the configured token and
// refuses everything else; the refusal for a missing token and a wrong one
// is the same message, so the listener cannot be used as an oracle; and
// the socket path is untouched — a named client over the socket still
// self-asserts its token, exactly as before.

import (
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
)

const tcpTestToken = "tcp-test-token-0123456789"

// tcpConnect runs the hello as one TCP connection: peer credentials are
// absent there by construction (PeerUID fails on a TCP conn), so the peer
// is known-false with the TCP flag.
func tcpConnect(h *Handler, hello *protocol.Hello) (*Session, *protocol.HelloReply) {
	return h.Begin(Peer{TCP: true}, hello)
}

func TestTCPIdentityRules(t *testing.T) {
	h := NewHarness(t, tempRoot(t))
	h.H.TCPToken = tcpTestToken

	t.Run("the configured token authenticates", func(t *testing.T) {
		sess, reply := tcpConnect(h.H, &protocol.Hello{
			Kind: protocol.KindNamed, Token: tcpTestToken,
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		if reply.Error != nil {
			t.Fatalf("the configured token was refused: %+v", reply.Error)
		}
		if sess.Identity.Kind != protocol.KindNamed || sess.Identity.Key != tcpTestToken {
			t.Errorf("identity = %+v, want the named token", sess.Identity)
		}
	})

	t.Run("a wrong token and a missing token are the same refusal", func(t *testing.T) {
		var wrong, missing *protocol.HelloReply
		_, wrong = tcpConnect(h.H, &protocol.Hello{
			Kind: protocol.KindNamed, Token: "wrong-token-0000000000000",
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		_, missing = tcpConnect(h.H, &protocol.Hello{
			Kind: protocol.KindNamed,
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		if wrong.Error == nil || wrong.Error.Code != 3 {
			t.Fatalf("a wrong token = %+v, want a refusal", wrong.Error)
		}
		if missing.Error == nil || missing.Error.Code != 3 {
			t.Fatalf("a missing token = %+v, want the same refusal", missing.Error)
		}
		if wrong.Error.Msg != missing.Error.Msg || wrong.Error.Remedy != missing.Error.Remedy {
			t.Errorf("the refusals must not distinguish wrong from missing:\nwrong:   %+v\nmissing: %+v", wrong.Error, missing.Error)
		}
		if strings.Contains(wrong.Error.Msg, "wrong-token") {
			t.Errorf("the refusal echoes the attempted token: %s", wrong.Error.Msg)
		}
	})

	t.Run("a host claim over TCP is refused", func(t *testing.T) {
		_, reply := tcpConnect(h.H, &protocol.Hello{
			Kind: protocol.KindHost,
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		if reply.Error == nil {
			t.Fatal("a host claim over TCP succeeded, want a refusal (peer credentials do not exist there)")
		}
		if !strings.Contains(reply.Error.Msg, "TCP") {
			t.Errorf("the refusal must say why TCP cannot verify a host: %s", reply.Error.Msg)
		}
	})

	t.Run("an ephemeral claim over TCP is refused", func(t *testing.T) {
		_, reply := tcpConnect(h.H, &protocol.Hello{
			Kind: protocol.KindEphemeral,
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		if reply.Error == nil {
			t.Fatal("an ephemeral claim over TCP succeeded, want a refusal (the TCP surface is token-authenticated as a whole)")
		}
	})

	t.Run("the socket path still self-asserts the named token", func(t *testing.T) {
		// The socket is 0700 owner-only: a named client's token there is
		// the operator's configuration, accepted as before — the TCP rule
		// must not leak into the socket path.
		sess, reply := h.Connect(protocol.KindNamed, "any-token-works-over-the-socket")
		if reply.Error != nil {
			t.Fatalf("the socket refused a self-asserted token: %+v", reply.Error)
		}
		if sess.Identity.Key != "any-token-works-over-the-socket" {
			t.Errorf("identity = %+v", sess.Identity)
		}
	})

	t.Run("a handler with no token configured refuses every TCP hello", func(t *testing.T) {
		h2 := NewHarness(t, tempRoot(t))
		_, reply := tcpConnect(h2.H, &protocol.Hello{
			Kind: protocol.KindNamed, Token: tcpTestToken,
			MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
		})
		if reply.Error == nil {
			t.Fatal("a TCP hello against a handler with no token configured succeeded")
		}
	})
}
