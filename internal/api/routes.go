package api

// routes.go is the single route table: the verb-to-path mapping both
// binaries derive their surface from. The server builds its ServeMux from
// it and the client builds its request paths from it, so a verb added in
// one place is added in both and the two cannot drift (plan.md §5, phase
// R1).
//
// Every RPC is POST /v1/<verb> with a JSON request body and a JSON
// response body; the two exceptions are GET /version (the unversioned
// version-range report, which replaced the hello negotiation) and
// GET /v1/ping. These are RPCs, not REST resources — no resource
// semantics, no path parameters, no query strings. The table is frozen at
// the end of R1: R2 generates the OpenAPI document and the client from
// it, and R3 and R4 must not add, rename or remove a route
// (PLAN-SCOPE.md, "Behaviours").

// Verb names, the wire names the Request envelope and the client helper
// carry. The path form replaces the '.' with '/' under /v1.
const (
	VerbSession      = "session"
	VerbPing         = "ping"
	VerbAllocate     = "allocate"
	VerbMaterialise  = "materialise"
	VerbActivate     = "activate"
	VerbRelease      = "release"
	VerbRm           = "rm"
	VerbBandsReserve = "bands.reserve"
	VerbBandsList    = "bands.list"
	VerbBandsSuggest = "bands.suggest"
	VerbPortsScan    = "ports.scan"
	VerbList         = "list"
	VerbDoctor       = "doctor"
	VerbReconcile    = "reconcile"
	VerbClientsList  = "clients.list"
)

// Route is one entry of the route table: the verb, the HTTP method it is
// served with and the path.
type Route struct {
	// Verb is the wire name, the Request envelope's verb field.
	Verb string
	// Method is the HTTP method: POST for every RPC except ping.
	Method string
	// Path is the HTTP path, e.g. "/v1/bands/reserve" for "bands.reserve".
	Path string
}

// Routes is the whole surface, in a stable order. Client and server both
// derive from it; nothing else hard-codes a path or a verb name.
var Routes = []Route{
	{Verb: VerbSession, Method: "POST", Path: "/v1/session"},
	{Verb: VerbAllocate, Method: "POST", Path: "/v1/allocate"},
	{Verb: VerbMaterialise, Method: "POST", Path: "/v1/materialise"},
	{Verb: VerbActivate, Method: "POST", Path: "/v1/activate"},
	{Verb: VerbRelease, Method: "POST", Path: "/v1/release"},
	{Verb: VerbRm, Method: "POST", Path: "/v1/rm"},
	{Verb: VerbBandsReserve, Method: "POST", Path: "/v1/bands/reserve"},
	{Verb: VerbBandsList, Method: "POST", Path: "/v1/bands/list"},
	{Verb: VerbBandsSuggest, Method: "POST", Path: "/v1/bands/suggest"},
	{Verb: VerbPortsScan, Method: "POST", Path: "/v1/ports/scan"},
	{Verb: VerbList, Method: "POST", Path: "/v1/list"},
	{Verb: VerbDoctor, Method: "POST", Path: "/v1/doctor"},
	{Verb: VerbReconcile, Method: "POST", Path: "/v1/reconcile"},
	{Verb: VerbClientsList, Method: "POST", Path: "/v1/clients/list"},
	{Verb: VerbPing, Method: "GET", Path: "/v1/ping"},
}

// VersionPath is GET /version, the unversioned version-range report that
// replaced the hello negotiation: a mismatched client gets a real upgrade
// message instead of a bare 404. It is not a verb — it carries no Request
// envelope — which is why it stands apart from the route table.
const VersionPath = "/version"

// PathForVerb returns the HTTP path for a verb name, from the route table.
// The client derives every request path through it, so a verb the client
// sends is a verb the server serves by construction.
func PathForVerb(verb string) (string, bool) {
	for _, r := range Routes {
		if r.Verb == verb {
			return r.Path, true
		}
	}
	return "", false
}

// VerbForPath returns the verb name for an HTTP path, from the route
// table. The server derives its dispatch through it.
func VerbForPath(path string) (string, bool) {
	for _, r := range Routes {
		if r.Path == path {
			return r.Verb, true
		}
	}
	return "", false
}

// IsRPC reports whether path is one of the served routes. The two GETs
// are routes too — they are RPCs, not resources, and they are part of the
// surface the table owns.
func IsRPC(path string) bool {
	if path == VersionPath {
		return true
	}
	_, ok := VerbForPath(path)
	return ok
}
