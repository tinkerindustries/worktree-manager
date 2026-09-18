package apigen

// apigen_test.go pins the phase-R2 exit criteria (plan.md §5, phase R2):
// the verb table covers the route table exactly, the rendered client has
// one method per route, the rendered document describes the whole
// surface, and the committed artefacts are current — a route added to
// api.Routes without regenerating fails here. The tests render in memory
// and compare, so they catch drift without needing a git checkout.

import (
	"bytes"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"strings"
	"testing"

	"github.com/goccy/go-yaml"

	"github.com/tinkerindustries/worktree-manager/internal/api"
)

// TestRouteTableAndVerbTypesAgree: every route in api.Routes has exactly
// one entry in the generator's verbTypes table, and the table names no
// route the table does not serve. A route added to api.Routes without a
// generator entry — and without regenerating — fails here, naming the
// verb.
func TestRouteTableAndVerbTypesAgree(t *testing.T) {
	served := map[string]bool{}
	for _, r := range api.Routes {
		served[r.Verb] = true
	}
	typed := map[string]bool{}
	for _, vt := range verbTypes {
		if typed[vt.verb] {
			t.Errorf("verbTypes lists %q twice", vt.verb)
		}
		typed[vt.verb] = true
		if vt.result == nil {
			t.Errorf("verbTypes entry %q has no result type", vt.verb)
		}
	}
	for _, r := range api.Routes {
		if !typed[r.Verb] {
			t.Errorf("route %q (%s %s) has no verbTypes entry — add one and regenerate: go run ./cmd/wtgen", r.Verb, r.Method, r.Path)
		}
	}
	for verb := range typed {
		if !served[verb] {
			t.Errorf("verbTypes lists %q, which api.Routes does not serve", verb)
		}
	}
}

// TestGeneratedClientHasOneMethodPerRoute: the rendered client has
// exactly one method per route of api.Routes (plus Version for
// GET /version), named by the wire verb. The rendered source is parsed —
// not the committed file — so a route added to the table without
// regenerating fails here even before the drift check.
func TestGeneratedClientHasOneMethodPerRoute(t *testing.T) {
	src, err := Client()
	if err != nil {
		t.Fatal(err)
	}
	fset := token.NewFileSet()
	f, err := parser.ParseFile(fset, "client.go", src, 0)
	if err != nil {
		t.Fatalf("the rendered client does not parse: %v", err)
	}
	methods := map[string]bool{}
	for _, d := range f.Decls {
		fn, ok := d.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) != 1 {
			continue
		}
		recv := fn.Recv.List[0].Type
		if star, ok := recv.(*ast.StarExpr); ok {
			recv = star.X
		}
		if ident, ok := recv.(*ast.Ident); ok && ident.Name == "Client" {
			methods[fn.Name.Name] = true
		}
	}
	// The client's own plumbing — the one dial-and-request path, the
	// token setter and the transport accessor — sits outside the route
	// methods.
	plumbing := map[string]bool{"do": true, "SetToken": true, "HTTP": true}
	want := map[string]bool{"Version": true}
	for _, r := range api.Routes {
		want[methodName(r.Verb)] = true
	}
	for m := range methods {
		if !want[m] && !plumbing[m] {
			t.Errorf("the generated client has a method %q that no route declares", m)
		}
	}
	for m := range want {
		if !methods[m] {
			t.Errorf("the generated client has no method for %q — regenerate: go run ./cmd/wtgen", m)
		}
	}
}

// TestOpenAPIDescribesTheWholeSurface: the rendered document parses as
// YAML and asserts the phase's shape: OpenAPI 3.1, every route of
// api.Routes plus GET /version, both security schemes, and the one error
// body shape {"error":{"code":int,"msg":string,"remedy":string}}.
func TestOpenAPIDescribesTheWholeSurface(t *testing.T) {
	data, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatalf("the rendered document is not YAML: %v", err)
	}
	if doc["openapi"] != "3.1.0" {
		t.Errorf("openapi = %v, want 3.1.0", doc["openapi"])
	}
	info, ok := doc["info"].(map[string]any)
	if !ok || info["title"] == "" || info["version"] == "" {
		t.Errorf("info = %v, want a title and a version", info)
	}
	paths, ok := doc["paths"].(map[string]any)
	if !ok {
		t.Fatal("the document has no paths")
	}
	if len(paths) != len(api.Routes)+1 {
		t.Errorf("paths = %d, want %d routes plus /version", len(paths), len(api.Routes)+1)
	}
	for _, r := range api.Routes {
		item, ok := paths[r.Path].(map[string]any)
		if !ok {
			t.Errorf("no path item for %s", r.Path)
			continue
		}
		op, ok := item[strings.ToLower(r.Method)].(map[string]any)
		if !ok {
			t.Errorf("no %s operation for %s", r.Method, r.Path)
			continue
		}
		if op["operationId"] != r.Verb {
			t.Errorf("operationId for %s = %v, want %q", r.Path, op["operationId"], r.Verb)
		}
		if r.Method == "POST" {
			if _, ok := op["requestBody"]; !ok {
				t.Errorf("the POST operation for %s has no requestBody", r.Path)
			}
		}
		if _, ok := op["responses"]; !ok {
			t.Errorf("the operation for %s has no responses", r.Path)
		}
	}
	if _, ok := paths[api.VersionPath]; !ok {
		t.Error("the document has no /version path")
	}
	components, ok := doc["components"].(map[string]any)
	if !ok {
		t.Fatal("the document has no components")
	}
	schemes, ok := components["securitySchemes"].(map[string]any)
	if !ok || len(schemes) != 2 {
		t.Errorf("securitySchemes = %v, want exactly the two schemes", schemes)
	}
	for _, name := range []string{"bearerAuth", "wtClient"} {
		if _, ok := schemes[name]; !ok {
			t.Errorf("missing security scheme %q", name)
		}
	}
	schemas, ok := components["schemas"].(map[string]any)
	if !ok {
		t.Fatal("the document has no component schemas")
	}
	errSchema, ok := schemas["Error"].(map[string]any)
	if !ok {
		t.Fatal("the document has no Error schema")
	}
	props, ok := errSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("the Error schema has no properties")
	}
	for _, name := range []string{"code", "msg", "remedy"} {
		if _, ok := props[name]; !ok {
			t.Errorf("the Error schema lacks %q", name)
		}
	}
	if required, ok := errSchema["required"].([]any); !ok || len(required) != 3 {
		t.Errorf("the Error schema's required = %v, want code, msg and remedy", errSchema["required"])
	}
}

// TestOpenAPIDerivesSchemasFromTheTypes: the component schemas come from
// the Go types exactly as encoding/json renders them — AllocateArgs
// requires spec, slug and path, and the spec subtree crosses the wire
// under its Go field names (Spec has no json tags).
func TestOpenAPIDerivesSchemasFromTheTypes(t *testing.T) {
	data, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	var doc map[string]any
	if err := yaml.Unmarshal(data, &doc); err != nil {
		t.Fatal(err)
	}
	schemas := doc["components"].(map[string]any)["schemas"].(map[string]any)
	args, ok := schemas["AllocateArgs"].(map[string]any)
	if !ok {
		t.Fatal("no AllocateArgs schema")
	}
	required, _ := args["required"].([]any)
	if len(required) != 3 || required[0] != "spec" || required[1] != "slug" || required[2] != "path" {
		t.Errorf("AllocateArgs required = %v, want [spec slug path]", required)
	}
	specSchema, ok := schemas["Spec"].(map[string]any)
	if !ok {
		t.Fatal("no Spec schema")
	}
	props, ok := specSchema["properties"].(map[string]any)
	if !ok {
		t.Fatal("the Spec schema has no properties")
	}
	if _, ok := props["Version"]; !ok {
		t.Error("the Spec schema lacks the Go-name property Version — the spec crosses the wire under its Go field names")
	}
	if _, ok := props["version"]; ok {
		t.Error("the Spec schema carries a yaml-name property; the wire uses Go field names")
	}
}

// TestRenderingIsDeterministic: two renders are byte-identical, so the
// CI drift check — regenerate on a clean tree, fail on any diff — is
// meaningful rather than a casualty of map ordering.
func TestRenderingIsDeterministic(t *testing.T) {
	a1, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	a2, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a1, a2) {
		t.Error("OpenAPI() is not deterministic across runs")
	}
	c1, err := Client()
	if err != nil {
		t.Fatal(err)
	}
	c2, err := Client()
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(c1, c2) {
		t.Error("Client() is not deterministic across runs")
	}
}

// TestAddingARouteWithoutRegeneratingFails is the proof of the phase's
// drift guarantee: a route appended to the live route table — exactly
// what a developer who adds a route and forgets to regenerate has done —
// makes both renderers refuse, because the generator cannot describe a
// route it has no types for. The committed artefacts would also be stale
// for TestCommittedArtefactsAreCurrent and for the CI drift job.
func TestAddingARouteWithoutRegeneratingFails(t *testing.T) {
	api.Routes = append(api.Routes, api.Route{Verb: "bogus", Method: "POST", Path: "/v1/bogus"})
	defer func() { api.Routes = api.Routes[:len(api.Routes)-1] }()
	if _, err := OpenAPI(); err == nil {
		t.Error("OpenAPI() succeeded with a route that has no verbTypes entry; it must refuse")
	}
	if _, err := Client(); err == nil {
		t.Error("Client() succeeded with a route that has no verbTypes entry; it must refuse")
	}
}

// TestCommittedArtefactsAreCurrent: the committed artefacts are
// regenerated in memory and compared byte-for-byte with what is on
// disk. A route added to api.Routes — or a type, tag or doc comment
// changed — without running `go run ./cmd/wtgen` fails here: the
// artefacts are generated, never hand-maintained beside the types
// (plan.md §5, phase R2).
func TestCommittedArtefactsAreCurrent(t *testing.T) {
	openapi, err := OpenAPI()
	if err != nil {
		t.Fatal(err)
	}
	clientSrc, err := Client()
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		path string
		want []byte
	}{
		{"../../api/openapi.yaml", openapi},
		{"../../internal/api/client/client.go", clientSrc},
	} {
		got, err := os.ReadFile(tc.path)
		if err != nil {
			t.Fatalf("reading %s: %v", tc.path, err)
		}
		if !bytes.Equal(got, tc.want) {
			t.Errorf("%s is stale: regenerate with `go run ./cmd/wtgen` and commit the diff", tc.path)
		}
	}
}
