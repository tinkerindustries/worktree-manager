package protocol

import (
	"bufio"
	"bytes"
	"errors"
	"strings"
	"testing"
)

func TestAgreeHighestCommon(t *testing.T) {
	cases := []struct {
		name                   string
		cmin, cmax, smin, smax int
		want                   int
	}{
		{"identical ranges", 1, 1, 1, 1, 1},
		{"client narrower", 1, 1, 1, 3, 1},
		{"coordinator narrower", 1, 3, 1, 1, 1},
		{"client ahead within overlap", 2, 3, 1, 2, 2},
		{"coordinator ahead within overlap", 1, 2, 2, 3, 2},
		{"single-point overlap at the top", 1, 2, 2, 4, 2},
		{"single-point overlap at the bottom", 2, 4, 1, 2, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := Agree(tc.cmin, tc.cmax, tc.smin, tc.smax)
			if err != nil {
				t.Fatalf("Agree(%d,%d,%d,%d) refused: %v", tc.cmin, tc.cmax, tc.smin, tc.smax, err)
			}
			if got != tc.want {
				t.Errorf("Agree = %d, want %d", got, tc.want)
			}
		})
	}
}

// TestAgreeRefusalsNameTheUpgradeBothDirections is exit criterion 3: a
// client one protocol version ahead of the coordinator refuses to proceed,
// from both directions, naming the upgrade.
func TestAgreeRefusalsNameTheUpgradeBothDirections(t *testing.T) {
	cases := []struct {
		name                   string
		cmin, cmax, smin, smax int
		wantSide               string
		wantNeed               int
	}{
		{"client one ahead", 2, 2, 1, 1, "coordinator", 2},
		{"client far ahead", 4, 5, 1, 3, "coordinator", 4},
		{"coordinator one ahead", 1, 1, 2, 2, "client", 2},
		{"coordinator far ahead", 1, 3, 4, 5, "client", 4},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Agree(tc.cmin, tc.cmax, tc.smin, tc.smax)
			if err == nil {
				t.Fatal("Agree succeeded, want a refusal")
			}
			var ue *UpgradeError
			if !errors.As(err, &ue) {
				t.Fatalf("refusal is %T, want *UpgradeError: %v", err, err)
			}
			if ue.Side != tc.wantSide {
				t.Errorf("side = %q, want %q", ue.Side, tc.wantSide)
			}
			if ue.Need != tc.wantNeed {
				t.Errorf("need = %d, want %d", ue.Need, tc.wantNeed)
			}
			msg := ue.Error()
			want := "upgrade the " + tc.wantSide
			if !strings.Contains(msg, want) {
				t.Errorf("message %q does not name the upgrade (%q)", msg, want)
			}
			// The message must be usable: it carries both ranges and the
			// needed version, so whoever reads it knows what to install.
			for _, frag := range []string{"protocol versions do not overlap", "speaks", "to speak protocol"} {
				if !strings.Contains(msg, frag) {
					t.Errorf("message %q lacks %q", msg, frag)
				}
			}
		})
	}
}

func TestAgreeMalformedRanges(t *testing.T) {
	if _, err := Agree(3, 1, 1, 1); err == nil {
		t.Error("malformed client range accepted")
	}
	if _, err := Agree(1, 1, 3, 1); err == nil {
		t.Error("malformed coordinator range accepted")
	}
}

// TestMessageFraming pins the wire shape: one JSON object per message, each
// newline-terminated, round-tripping through the same framing both binaries
// share.
func TestMessageFraming(t *testing.T) {
	var buf bytes.Buffer
	w := bufio.NewWriter(&buf)

	hello := Hello{Kind: KindEphemeral, MinVer: 1, MaxVer: 1}
	if err := WriteMessage(w, &hello); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	reply := HelloReply{Agreed: 1, SessionID: "s123"}
	if err := WriteMessage(w, &reply); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	req := Request{Verb: "ping"}
	if err := WriteMessage(w, &req); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	resp := Response{Error: &Error{Code: 5, Msg: "down", Remedy: "start it"}}
	if err := WriteMessage(w, &resp); err != nil {
		t.Fatalf("WriteMessage: %v", err)
	}
	if err := w.Flush(); err != nil {
		t.Fatal(err)
	}

	lines := bytes.Split(buf.Bytes(), []byte("\n"))
	if len(lines) != 5 || len(lines[4]) != 0 {
		t.Fatalf("wire has %d lines, want 4 newline-terminated messages: %q", len(lines), buf.String())
	}
	for i, line := range lines[:4] {
		if len(line) == 0 {
			t.Fatalf("message %d is empty", i)
		}
	}

	r := bufio.NewReader(&buf)
	var gotHello Hello
	if err := ReadMessage(r, &gotHello); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if gotHello.Kind != KindEphemeral || gotHello.MaxVer != 1 {
		t.Errorf("hello round trip = %+v", gotHello)
	}
	var gotReply HelloReply
	if err := ReadMessage(r, &gotReply); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if gotReply.Agreed != 1 || gotReply.SessionID != "s123" {
		t.Errorf("reply round trip = %+v", gotReply)
	}
	var gotReq Request
	if err := ReadMessage(r, &gotReq); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if gotReq.Verb != "ping" {
		t.Errorf("request round trip = %+v", gotReq)
	}
	var gotResp Response
	if err := ReadMessage(r, &gotResp); err != nil {
		t.Fatalf("ReadMessage: %v", err)
	}
	if gotResp.Error == nil || gotResp.Error.Code != 5 || gotResp.Error.Remedy != "start it" {
		t.Errorf("response round trip = %+v", gotResp)
	}
}

func TestReadMessageRejectsGarbage(t *testing.T) {
	r := bufio.NewReader(strings.NewReader("not json\n"))
	var v Hello
	if err := ReadMessage(r, &v); err == nil {
		t.Error("garbage accepted as a message")
	}
}

// TestReadMessageCapHoldsWhileReading is the security pass's oversized-
// message rail: a peer streaming bytes without a newline must not grow the
// coordinator's memory past the cap. The read is bounded as it accumulates
// (ReadSlice loops, capped at MaxMessageBytes), so an unbounded stream
// fails fast with the cap error instead of buffering forever.
func TestReadMessageCapHoldsWhileReading(t *testing.T) {
	// A stream with no newline at all, far beyond the cap: the old code
	// buffered it all before checking; the fixed code refuses at the cap.
	r := bufio.NewReader(strings.NewReader(strings.Repeat("x", MaxMessageBytes+1)))
	var v Hello
	err := ReadMessage(r, &v)
	if err == nil {
		t.Fatal("an oversized message was accepted")
	}
	if !strings.Contains(err.Error(), "cap") {
		t.Errorf("the refusal must name the cap: %v", err)
	}

	// A message just under the cap (the terminating newline counts toward
	// the byte total, as it always has) still decodes — and fails on JSON,
	// not on size.
	r = bufio.NewReader(strings.NewReader(strings.Repeat(" ", MaxMessageBytes-1) + "\n"))
	if err := ReadMessage(r, &v); err == nil || strings.Contains(err.Error(), "cap") {
		t.Errorf("an at-cap message must fail on JSON, not on the size cap: %v", err)
	}

	// A newline inside a legit message passes the size gate and decodes.
	r = bufio.NewReader(strings.NewReader(`{"kind":"host","min_version":1,"max_version":1}` + "\n"))
	if err := ReadMessage(r, &v); err != nil {
		t.Fatalf("a valid message refused: %v", err)
	}
}
