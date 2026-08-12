// Package protocol is the wire between wt and wtd: the message types both
// binaries share, the newline-delimited JSON framing, and the version
// negotiation that runs on connect.
//
// The wire is one JSON object per message, each terminated by a newline —
// no framing header, no length prefix, no protobuf, no gRPC
// (PLAN-SCOPE.md, "Generalisation past the five compiled-in drivers").
// The first exchange on a connection is a hello: the client states the
// protocol version range it supports and declares its identity; the
// coordinator replies with the agreed version or refuses, naming the
// upgrade in either direction (plan.md §3's refuse-rather-than-partially-
// honour rule applied to the wire, ARCHITECTURE.md §11.3).
package protocol

import (
	"bufio"
	"encoding/json"
	"fmt"
)

// The protocol version range this build speaks. Both binaries ship the same
// range in this phase; a build that understands a newer wire carries a
// higher Max and the negotiation refuses a pair whose ranges do not
// overlap. The version is carried as a range rather than a single number so
// an old client and a new coordinator (and the reverse) each get a usable
// upgrade message (RELEASE.md, "Three independent versions").
const (
	VersionMin = 1
	VersionMax = 1
)

// Client kinds, per ARCHITECTURE.md §10.2. Only the ephemeral declaration
// and the named token are things the client asserts; a host identity comes
// from the kernel through peer credentials and the coordinator never trusts
// a claim of it.
const (
	KindHost      = "host"
	KindNamed     = "named"
	KindEphemeral = "ephemeral"
)

// UpgradeError is the refusal produced when two version ranges do not
// overlap. Side names which binary must be upgraded, so the message is
// usable in both directions: a client ahead of the coordinator and a
// coordinator ahead of the client.
type UpgradeError struct {
	Side                 string // "client" or "coordinator"
	Need                 int    // the lowest version the upgraded side must speak
	ClientMin, ClientMax int
	ServerMin, ServerMax int
}

func (e *UpgradeError) Error() string {
	return fmt.Sprintf(
		"protocol versions do not overlap: this client speaks %d..%d and this coordinator speaks %d..%d; upgrade the %s to speak protocol %d",
		e.ClientMin, e.ClientMax, e.ServerMin, e.ServerMax, e.Side, e.Need)
}

// Agree resolves the highest protocol version common to the two ranges, or
// refuses with an UpgradeError naming which side must move. The agreed
// version is the highest common one: Max on both sides when the ranges
// overlap at all.
func Agree(clientMin, clientMax, serverMin, serverMax int) (int, error) {
	switch {
	case clientMin > clientMax:
		return 0, fmt.Errorf("client protocol range %d..%d is malformed: min exceeds max", clientMin, clientMax)
	case serverMin > serverMax:
		return 0, fmt.Errorf("coordinator protocol range %d..%d is malformed: min exceeds max", serverMin, serverMax)
	case clientMin > serverMax:
		return 0, &UpgradeError{Side: "coordinator", Need: clientMin,
			ClientMin: clientMin, ClientMax: clientMax, ServerMin: serverMin, ServerMax: serverMax}
	case serverMin > clientMax:
		return 0, &UpgradeError{Side: "client", Need: serverMin,
			ClientMin: clientMin, ClientMax: clientMax, ServerMin: serverMin, ServerMax: serverMax}
	}
	if clientMax < serverMax {
		return clientMax, nil
	}
	return serverMax, nil
}

// Hello is the first message on a connection: the client's protocol range
// and its identity declaration. Kind is "host", "named" or "ephemeral". A
// host client declares nothing — the coordinator reads the uid from peer
// credentials and ignores any claim — a named client presents Token, and an
// ephemeral client declares itself so the coordinator issues a session id
// and marks its entries reclaimable (ARCHITECTURE.md §10.2).
type Hello struct {
	Kind   string `json:"kind"`
	Token  string `json:"token,omitempty"`
	MinVer int    `json:"min_version"`
	MaxVer int    `json:"max_version"`
}

// HelloReply is the coordinator's answer to a hello: the agreed version and,
// for an ephemeral client, the issued session id; or a refusal whose error
// names the upgrade.
type HelloReply struct {
	Agreed    int    `json:"agreed,omitempty"`
	SessionID string `json:"session_id,omitempty"`
	Error     *Error `json:"error,omitempty"`
}

// Request is one verb invocation. Args is the verb's own JSON object,
// opaque to the framing.
type Request struct {
	Verb string          `json:"verb"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the answer to one request: either a result or an error that
// carries the exit code the client should use — that is what lets exit
// codes 3, 4 and 5 originate in the coordinator and still reach the process
// exit status unchanged (ARCHITECTURE.md §11.3).
type Response struct {
	Result json.RawMessage `json:"result,omitempty"`
	Error  *Error          `json:"error,omitempty"`
}

// Error crosses the wire with the exit code, message and remedy — the same
// shape internal/cli's Error carries, so a response error maps straight
// onto the client's process exit code and its "every error names the
// command that fixes it" rule (plan.md §3).
type Error struct {
	Code   int    `json:"code"`
	Msg    string `json:"msg"`
	Remedy string `json:"remedy"`
}

// MaxMessageBytes caps one wire message. The largest thing that crosses the
// socket is the parsed spec a client attaches to a call (phase 3+), which
// is small; the cap exists so a broken or hostile peer cannot grow memory
// without bound.
const MaxMessageBytes = 1 << 20 // 1 MiB

// ReadMessage reads one newline-terminated JSON object from r into v.
func ReadMessage(r *bufio.Reader, v any) error {
	line, err := r.ReadBytes('\n')
	if err != nil {
		return err
	}
	if len(line) > MaxMessageBytes {
		return fmt.Errorf("message of %d bytes exceeds the %d-byte wire cap", len(line), MaxMessageBytes)
	}
	if err := json.Unmarshal(line, v); err != nil {
		return fmt.Errorf("message is not one JSON object: %w", err)
	}
	return nil
}

// WriteMessage writes v as one newline-terminated JSON object to w. The
// caller flushes.
func WriteMessage(w *bufio.Writer, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if _, err := w.Write(data); err != nil {
		return err
	}
	return w.WriteByte('\n')
}
