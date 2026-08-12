//go:build windows

package coord

// pipe_integration_test.go is the named pipe carrying a full request, at
// the coordinator layer: a real Server listening on a temp pipe, a real
// client connection through platform.DialSocket, and a full hello +
// request + response round trip over the newline-delimited JSON protocol
// — the phase-8 exit criterion a hosted windows-latest runner can prove
// (plan.md §5). The protocol itself is unchanged, which is what makes the
// round trip a proof of the transport rather than of the protocol.

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// TestPipeServerCarriesAFullRequest runs Server.Serve on a temp pipe name
// and drives hello + ping through the wire — the exact arrangement a real
// `wt` client uses, minus the cli package.
func TestPipeServerCarriesAFullRequest(t *testing.T) {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		t.Fatal(err)
	}
	pipe := `\\.\pipe\wt-coord-test-` + hex.EncodeToString(b)

	st, err := store.Open(t.TempDir())
	if err != nil {
		t.Fatalf("opening the store: %v", err)
	}
	log := slog.New(slog.NewTextHandler(io.Discard, nil))
	h, err := NewHandler(st, log)
	if err != nil {
		t.Fatal(err)
	}
	srv := NewServer(h, log)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	done := make(chan error, 1)
	go func() { done <- srv.Serve(ctx, pipe) }()

	// The client half: dial, hello, one request, one response.
	conn, err := platform.DialSocket(pipe)
	if err != nil {
		t.Fatalf("dialing the pipe: %v", err)
	}
	defer conn.Close()
	bw := bufio.NewWriter(conn)
	br := bufio.NewReader(conn)
	if err := protocol.WriteMessage(bw, &protocol.Hello{
		Kind: protocol.KindHost, MinVer: protocol.VersionMin, MaxVer: protocol.VersionMax,
	}); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	var reply protocol.HelloReply
	if err := protocol.ReadMessage(br, &reply); err != nil {
		t.Fatalf("reading the hello reply: %v", err)
	}
	if reply.Error != nil {
		t.Fatalf("hello refused: %s", reply.Error.Msg)
	}
	if reply.Agreed != protocol.VersionMax {
		t.Errorf("agreed version = %d, want %d", reply.Agreed, protocol.VersionMax)
	}

	if err := protocol.WriteMessage(bw, &protocol.Request{Verb: "ping"}); err != nil {
		t.Fatal(err)
	}
	if err := bw.Flush(); err != nil {
		t.Fatal(err)
	}
	var resp protocol.Response
	if err := protocol.ReadMessage(br, &resp); err != nil {
		t.Fatalf("reading the response: %v", err)
	}
	if resp.Error != nil {
		t.Fatalf("ping refused: %s", resp.Error.Msg)
	}
	var ok struct {
		OK bool `json:"ok"`
	}
	if err := json.Unmarshal(resp.Result, &ok); err != nil || !ok.OK {
		t.Errorf("ping result = %s, want {\"ok\":true}", resp.Result)
	}

	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Errorf("server returned an error: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Error("server did not stop after cancellation")
	}
}
