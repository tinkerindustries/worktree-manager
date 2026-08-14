// Package api is the contract between wt and wtd: the message types both
// binaries share, the route table that maps every verb to its HTTP path,
// the endpoint file that carries the coordinator's location and token, and
// the version range both sides advertise.
//
// The wire is plain HTTP on a loopback TCP port: every RPC is
// POST /v1/<verb> with a JSON request body and a JSON response body, and
// the two exceptions are GET /version (the version-range report that
// replaced the hello negotiation) and GET /v1/ping. These are RPCs, not
// REST resources — no resource semantics, no path parameters, no query
// strings (PLAN-SCOPE.md, "In scope").
//
// The route table is the single source of the surface: client and server
// both derive their paths from it, so they cannot drift (plan.md §5,
// phase R1).
package api

import (
	"encoding/json"
	"fmt"
	"time"
)

// The API version range this build speaks. Both binaries ship the same
// range in this phase; a build that understands a newer wire carries a
// higher Max and GET /version lets a mismatched pair refuse with a real
// upgrade message instead of a bare 404. The version is carried as a
// range rather than a single number so an old client and a new
// coordinator (and the reverse) each get a usable upgrade message
// (RELEASE.md, "Three independent versions").
const (
	VersionMin = 1
	VersionMax = 1
)

// Client kinds (ARCHITECTURE.md §10.2). Over HTTP there are no peer
// credentials, so the bearer token is the whole of identity: the token
// from the 0600 endpoint.json for a host client, the coordinator's
// --container-token for a named container, and an issued session id for
// an ephemeral one.
const (
	KindHost      = "host"
	KindNamed     = "named"
	KindEphemeral = "ephemeral"
)

// Request is one verb invocation. Verb names the RPC (the route table's
// key) and Args is the verb's own JSON object, opaque to the transport.
type Request struct {
	Verb string          `json:"verb"`
	Args json.RawMessage `json:"args,omitempty"`
}

// Response is the answer to one request: either a result or an error that
// carries the exit code the client should use — that is what lets exit
// codes 3 and 4 originate in the coordinator and still reach the process
// exit status unchanged (ARCHITECTURE.md §11.3). The response body is
// authoritative for the exit code; the HTTP status is advisory, for
// anything reading the API that is not wt.
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

// MaxMessageBytes caps one request body, enforced with http.MaxBytesReader
// on every request. The largest thing that crosses the wire is the parsed
// spec a client attaches to a call (phase 3+), which is small; the cap
// exists so a broken or hostile peer cannot grow memory without bound. It
// preserves the old wire's cap exactly (1 MiB).
const MaxMessageBytes = 1 << 20 // 1 MiB

// ReadHeaderTimeout bounds how long a connection may sit without sending
// its request headers, replacing the hello deadline: the client sends its
// request immediately after dialing, so ten seconds is generous, and the
// deadline is what keeps a stalled connection — the slowloris shape — from
// holding a goroutine and a buffer indefinitely (the security pass, phase
// 9).
const ReadHeaderTimeout = 10 * time.Second

// VersionInfo is the GET /version response: the coordinator's supported
// API version range. Unversioned on purpose — a client that cannot parse
// it is a client that needs the upgrade message, and the endpoint exists
// precisely so a mismatched pair learns about each other.
type VersionInfo struct {
	Min int `json:"min"`
	Max int `json:"max"`
}

// ErrVersionMismatch names the binary that must move when two version
// ranges do not overlap. Side is "client" or "coordinator"; Need is the
// lowest version the upgraded side must speak.
type ErrVersionMismatch struct {
	Side                 string
	Need                 int
	ClientMin, ClientMax int
	ServerMin, ServerMax int
}

func (e *ErrVersionMismatch) Error() string {
	return fmt.Sprintf(
		"API versions do not overlap: this client speaks %d..%d and this coordinator speaks %d..%d; upgrade the %s to speak API version %d",
		e.ClientMin, e.ClientMax, e.ServerMin, e.ServerMax, e.Side, e.Need)
}

// CheckVersion refuses a pair of version ranges that do not overlap,
// naming which binary must move and the lowest version it must speak. It
// is the client's half of the old hello negotiation: the client reads the
// coordinator's range from GET /version and refuses locally, so the
// mismatch is reported as an exit-3 error with a real upgrade message
// rather than a bare 404 (plan.md §5, phase R1).
func CheckVersion(clientMin, clientMax, serverMin, serverMax int) *Error {
	switch {
	case clientMin > clientMax:
		return &Error{Code: 1, Msg: fmt.Sprintf("client API range %d..%d is malformed: min exceeds max", clientMin, clientMax), Remedy: "check the wt build, then re-run"}
	case serverMin > serverMax:
		return &Error{Code: 1, Msg: fmt.Sprintf("coordinator API range %d..%d is malformed: min exceeds max", serverMin, serverMax), Remedy: "check the wtd build, then re-run"}
	case clientMin > serverMax:
		return &Error{Code: 3,
			Msg:    (&ErrVersionMismatch{Side: "coordinator", Need: clientMin, ClientMin: clientMin, ClientMax: clientMax, ServerMin: serverMin, ServerMax: serverMax}).Error(),
			Remedy: fmt.Sprintf("upgrade wtd to speak API version %d, then re-run", clientMin)}
	case serverMin > clientMax:
		return &Error{Code: 3,
			Msg:    (&ErrVersionMismatch{Side: "client", Need: serverMin, ClientMin: clientMin, ClientMax: clientMax, ServerMin: serverMin, ServerMax: serverMax}).Error(),
			Remedy: fmt.Sprintf("upgrade wt to speak API version %d, then re-run", serverMin)}
	}
	return nil
}
