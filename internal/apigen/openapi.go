package apigen

// openapi.go renders the OpenAPI 3.1 document: the whole surface —
// every route of api.Routes plus GET /version — both authentication
// schemes and the one error body shape, with every schema derived from
// the api and spec Go types. The document is generated, never
// hand-maintained beside the types: regenerating it on a clean tree
// produces no diff, and CI fails if it does (plan.md §5, phase R2).

import (
	"fmt"
	"reflect"

	"github.com/goccy/go-yaml"

	"github.com/tinkerindustries/worktree-manager/internal/api"
)

// OpenAPI renders api/openapi.yaml. The returned bytes are the whole
// document, deterministic across runs: every map is an ordered
// yaml.MapSlice, so a regeneration on a clean tree is byte-identical.
func OpenAPI() ([]byte, error) {
	docs, err := loadDocs()
	if err != nil {
		return nil, err
	}
	w := newSchemaWalker(docs)

	// The component roots: the response envelope (which carries the one
	// error shape), the version report, and every verb's request and
	// result types. Everything they reach into — the spec subtree
	// included — is collected transitively.
	roots := []reflect.Type{
		reflect.TypeOf(api.Response{}),
		reflect.TypeOf(api.VersionInfo{}),
	}
	for _, vt := range verbTypes {
		if vt.args != nil {
			roots = append(roots, vt.args)
		}
		roots = append(roots, vt.result)
	}
	for _, r := range roots {
		w.schemaFor(r)
	}

	paths := yaml.MapSlice{
		{Key: api.VersionPath, Value: versionPath(w, docs)},
	}
	for _, r := range api.Routes {
		vt, err := verbTypeFor(r.Verb)
		if err != nil {
			return nil, err
		}
		paths = append(paths, yaml.MapItem{Key: r.Path, Value: routePath(r, vt, w, docs)})
	}

	doc := yaml.MapSlice{
		{Key: "openapi", Value: "3.1.0"},
		{Key: "info", Value: yaml.MapSlice{
			{Key: "title", Value: "worktree-manager"},
			{Key: "version", Value: fmt.Sprintf("%d..%d", api.VersionMin, api.VersionMax)},
			{Key: "description", Value: "The worktree-manager coordinator API: per-worktree resource allocation served by wtd over loopback HTTP. Every RPC is POST /v1/<verb> with the verb's args object as the JSON body; the two exceptions are GET /version (the unversioned version-range report) and GET /v1/ping. These are RPCs, not REST resources — no resource semantics, no path parameters, no query strings (plan.md §5, phase R1). The route table in internal/api is the single source of this document and of the generated client, so the two cannot drift."},
		}},
		{Key: "servers", Value: []any{yaml.MapSlice{
			{Key: "url", Value: api.DefaultEndpoint},
			{Key: "description", Value: "the compiled-in default. A running coordinator writes its real address and the host token into endpoint.json under WT_HOME/$HOME/.wt; a host client reads it, and a container, which never mounts the store, is given WT_ENDPOINT and WT_CLIENT_TOKEN explicitly."},
		}}},
		{Key: "paths", Value: paths},
		{Key: "components", Value: yaml.MapSlice{
			{Key: "securitySchemes", Value: yaml.MapSlice{
				{Key: "bearerAuth", Value: yaml.MapSlice{
					{Key: "type", Value: "http"},
					{Key: "scheme", Value: "bearer"},
					{Key: "description", Value: "the bearer token is the whole of identity: the host token from the 0600 endpoint.json, the coordinator's --container-token for a named container, or an issued session id for an ephemeral one. A missing, wrong or wrong-kind token is refused with one identical message, compared in constant time (plan.md §5, phase R1)."},
				}},
				{Key: "wtClient", Value: yaml.MapSlice{
					{Key: "type", Value: "apiKey"},
					{Key: "in", Value: "header"},
					{Key: "name", Value: "X-Wt-Client"},
					{Key: "description", Value: "every request, the version report included, must carry X-Wt-Client: 1 — a browser cannot set a custom header on a simple-form POST, so this forces a preflight that is never answered. No CORS header is ever sent (plan.md §5, phase R1)."},
				}},
			}},
			{Key: "schemas", Value: w.schemas},
		}},
	}
	data, err := yaml.Marshal(doc)
	if err != nil {
		return nil, fmt.Errorf("apigen: rendering the OpenAPI document: %w", err)
	}
	return data, nil
}

// versionPath renders the GET /version path item: the unversioned
// version-range report, gated by the Host and X-Wt-Client checks but
// carrying no bearer — a mismatched client must be able to read it to
// learn it must upgrade.
func versionPath(w *schemaWalker, docs *docIndex) yaml.MapSlice {
	return yaml.MapSlice{
		{Key: "get", Value: yaml.MapSlice{
			{Key: "operationId", Value: "version"},
			{Key: "summary", Value: "the coordinator's supported API version range"},
			{Key: "description", Value: typeDescription(docs, reflect.TypeOf(api.VersionInfo{}))},
			{Key: "security", Value: []any{yaml.MapSlice{{Key: "wtClient", Value: []any{}}}}},
			{Key: "responses", Value: yaml.MapSlice{
				{Key: "200", Value: yaml.MapSlice{
					{Key: "description", Value: "the version range, as one JSON object — the one response that is not the Response envelope"},
					{Key: "content", Value: yaml.MapSlice{
						{Key: "application/json", Value: yaml.MapSlice{
							{Key: "schema", Value: yaml.MapSlice{{Key: "$ref", Value: refName("VersionInfo")}}},
						}},
					}},
				}},
				{Key: "400", Value: errorResponse("code 2")},
				{Key: "403", Value: errorResponse("code 3 — refused: the Host check, or a request without X-Wt-Client: 1")},
				{Key: "424", Value: errorResponse("code 4")},
				{Key: "500", Value: errorResponse("code 1")},
			}},
		}},
	}
}

// routePath renders one route's path item: the single operation the
// route table declares, its typed request body and the enveloped
// response with the verb's result schema.
func routePath(r api.Route, vt verbType, w *schemaWalker, docs *docIndex) yaml.MapSlice {
	op := yaml.MapSlice{
		{Key: "operationId", Value: r.Verb},
		{Key: "summary", Value: operationSummary(docs, vt)},
		{Key: "security", Value: []any{yaml.MapSlice{
			{Key: "bearerAuth", Value: []any{}},
			{Key: "wtClient", Value: []any{}},
		}}},
	}
	if r.Method == "POST" {
		op = append(op, yaml.MapItem{Key: "requestBody", Value: requestBody(docs, vt)})
	}
	op = append(op, yaml.MapItem{Key: "responses", Value: responses(vt)})
	return yaml.MapSlice{{Key: methodKey(r.Method), Value: op}}
}

// methodKey is the OpenAPI operation key for an HTTP method.
func methodKey(method string) string {
	switch method {
	case "GET":
		return "get"
	case "POST":
		return "post"
	default:
		return "x-" + method
	}
}

// requestBody renders the typed request body: the verb's args object,
// or the empty object for a verb that carries no arguments (the
// generated client sends an empty object either way, and the session
// admission is the one POST whose body the coordinator ignores).
func requestBody(docs *docIndex, vt verbType) yaml.MapSlice {
	schema := yaml.MapSlice{{Key: "type", Value: "object"}}
	description := ""
	if vt.args == nil {
		description = "the empty object: " + vt.verb + " carries no arguments"
	} else {
		schema = yaml.MapSlice{{Key: "$ref", Value: refName(vt.args.Name())}}
		if d := typeDescription(docs, vt.args); d != "" {
			description = d
		}
	}
	body := yaml.MapSlice{
		{Key: "required", Value: true},
		{Key: "content", Value: yaml.MapSlice{
			{Key: "application/json", Value: yaml.MapSlice{{Key: "schema", Value: schema}}},
		}},
	}
	if description != "" {
		body = append(yaml.MapSlice{{Key: "description", Value: description}}, body...)
	}
	return body
}

// responses renders the operation's responses: the enveloped success
// carrying the verb's typed result, the four coordinator error statuses
// carrying the one error body shape, and the plain 404 for a path
// outside the route table.
func responses(vt verbType) yaml.MapSlice {
	return yaml.MapSlice{
		{Key: "200", Value: yaml.MapSlice{
			{Key: "description", Value: "the Response envelope carrying the " + vt.result.Name() + " result"},
			{Key: "content", Value: yaml.MapSlice{
				{Key: "application/json", Value: yaml.MapSlice{
					{Key: "schema", Value: yaml.MapSlice{
						{Key: "type", Value: "object"},
						{Key: "properties", Value: yaml.MapSlice{
							{Key: "result", Value: yaml.MapSlice{{Key: "$ref", Value: refName(vt.result.Name())}}},
							{Key: "error", Value: yaml.MapSlice{{Key: "$ref", Value: refName("Error")}}},
						}},
					}},
				}},
			}},
		}},
		{Key: "400", Value: errorResponse("code 2: the request body exceeds the 1 MiB cap, or is malformed")},
		{Key: "403", Value: errorResponse("code 3 — refused: the bearer, the Host check, X-Wt-Client, or a safety check")},
		{Key: "404", Value: yaml.MapSlice{
			{Key: "description", Value: "a path outside the route table: a plain 404, not a coordinator error — a route-table-derived client cannot produce it"},
		}},
		{Key: "424", Value: errorResponse("code 4 — a capability the coordinator depends on is missing")},
		{Key: "500", Value: errorResponse("code 1 — a coordinator failure")},
	}
}

// errorResponse is one error status: the one error body shape — the
// Response envelope carrying the Error, whose code in the body is
// authoritative for the exit code while the status is advisory.
func errorResponse(when string) yaml.MapSlice {
	return yaml.MapSlice{
		{Key: "description", Value: when + ". The one error body shape: {\"error\":{\"code\":int,\"msg\":string,\"remedy\":string}} — the body is authoritative for the exit code; the HTTP status is advisory, for anything reading the API that is not wt"},
		{Key: "content", Value: yaml.MapSlice{
			{Key: "application/json", Value: yaml.MapSlice{
				{Key: "schema", Value: yaml.MapSlice{{Key: "$ref", Value: refName("Response")}}},
			}},
		}},
	}
}

// operationSummary is the operation's one-line summary: the doc comment
// of the verb's args type, or of its result type for a verb that sends
// no arguments — the sentence that says what the RPC is for.
func operationSummary(docs *docIndex, vt verbType) string {
	if vt.args != nil {
		if d := typeDescription(docs, vt.args); d != "" {
			return d
		}
	}
	return typeDescription(docs, vt.result)
}

// typeDescription is a type's first-paragraph doc comment from the doc
// index.
func typeDescription(docs *docIndex, t reflect.Type) string {
	if docs == nil {
		return ""
	}
	return docs.types[pkgShort(t.PkgPath())+"."+t.Name()]
}
