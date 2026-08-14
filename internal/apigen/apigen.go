// Package apigen is the phase-R2 generator: it derives the OpenAPI 3.1
// document at api/openapi.yaml and the Go client at
// internal/api/client/client.go from the route table and the *Args/
// *Result types in internal/api. The Go types are the single source of
// truth — the document is generated from them, never hand-maintained
// beside them — and a verb added to the route table without regenerating
// fails the build's drift checks (plan.md §5, phase R2).
//
// The shape follows internal/generate, the module's first generator: a
// function another package calls (cmd/wtgen and the tests), rendering
// whole files that are gofmt-clean by construction. Two decisions bind
// the output:
//
//   - The client depends on the standard library and the api package
//     alone: no third-party HTTP, routing or codegen library is added
//     (PLAN-SCOPE.md non-goal "No third-party HTTP, routing or codegen
//     libraries"), and the runtime dependency list stays at exactly two.
//   - The document is rendered through github.com/goccy/go-yaml — a
//     dependency the module already carries — from an ordered tree, so
//     regeneration is byte-identical and the CI drift check is
//     meaningful.
//
// Neither artefact is a verb: cmd/wtgen drives them at build time, the
// tests pin them, and no binary imports this package.
package apigen

import (
	"fmt"
	"go/ast"
	"go/doc"
	"go/parser"
	"go/token"
	"os"
	"reflect"
	"strings"

	"github.com/goccy/go-yaml"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
)

// verbType is one route's Go types: the wire verb and the request and
// result types its RPC carries. Args is nil for a verb whose request
// carries no arguments (the generated client sends an empty object);
// every route has a result. The table is the one place the wire names
// meet the Go types, and it is compile-checked — the entries are
// reflect.TypeOf of real api types, so a renamed or deleted type fails
// the build — while TestRouteTableAndVerbTypesAgree proves the table
// covers api.Routes exactly, so a route added to the table without a
// generator entry (and without regenerating) fails the tests.
type verbType struct {
	verb   string
	args   reflect.Type // nil: the verb sends no arguments
	result reflect.Type
}

// verbTypes is the generator's half of the route table: one entry per
// route, keyed by the wire verb. The method and path come from api.Routes
// at render time, so the route table stays the single source of the
// surface.
var verbTypes = []verbType{
	{verb: api.VerbSession, result: reflect.TypeOf(api.SessionResult{})},
	{verb: api.VerbPing, result: reflect.TypeOf(api.PingResult{})},
	{verb: api.VerbAllocate, args: reflect.TypeOf(api.AllocateArgs{}), result: reflect.TypeOf(api.AllocateResult{})},
	{verb: api.VerbMaterialise, args: reflect.TypeOf(api.MaterialiseArgs{}), result: reflect.TypeOf(api.MaterialiseResult{})},
	{verb: api.VerbActivate, args: reflect.TypeOf(api.EntryRef{}), result: reflect.TypeOf(api.ActivateResult{})},
	{verb: api.VerbRelease, args: reflect.TypeOf(api.EntryRef{}), result: reflect.TypeOf(api.ReleaseResult{})},
	{verb: api.VerbRm, args: reflect.TypeOf(api.RmArgs{}), result: reflect.TypeOf(api.RmResult{})},
	{verb: api.VerbBandsReserve, args: reflect.TypeOf(api.ReserveBandArgs{}), result: reflect.TypeOf(api.ReserveBandResult{})},
	{verb: api.VerbBandsList, result: reflect.TypeOf(api.BandsListResult{})},
	{verb: api.VerbBandsSuggest, args: reflect.TypeOf(api.SuggestBandArgs{}), result: reflect.TypeOf(api.SuggestBandResult{})},
	{verb: api.VerbPortsScan, result: reflect.TypeOf(api.PortsScanResult{})},
	{verb: api.VerbList, args: reflect.TypeOf(api.ListArgs{}), result: reflect.TypeOf(api.ListResult{})},
	{verb: api.VerbDoctor, result: reflect.TypeOf(api.DoctorResult{})},
	{verb: api.VerbReconcile, args: reflect.TypeOf(api.ReconcileArgs{}), result: reflect.TypeOf(api.ReconcileResult{})},
	{verb: api.VerbClientsList, result: reflect.TypeOf(api.ClientsListResult{})},
}

// methodName derives the generated client's method name from a wire
// verb: each dot-separated segment is capitalised and joined, so
// "bands.reserve" becomes BandsReserve and "clients.list" becomes
// ClientsList. The route table's verbs are the only inputs, so the
// method set tracks the route set by construction.
func methodName(verb string) string {
	var b strings.Builder
	for _, seg := range strings.Split(verb, ".") {
		if seg == "" {
			continue
		}
		b.WriteString(strings.ToUpper(seg[:1]))
		b.WriteString(seg[1:])
	}
	return b.String()
}

// verbTypeFor returns the table entry for a wire verb. Every route in
// api.Routes has one — TestRouteTableAndVerbTypesAgree proves it — so a
// missing entry is a generator defect that must fail loudly, naming the
// verb.
func verbTypeFor(verb string) (verbType, error) {
	for _, vt := range verbTypes {
		if vt.verb == verb {
			return vt, nil
		}
	}
	return verbType{}, fmt.Errorf("apigen: no verb-type entry for route %q — add one to the verbTypes table and regenerate", verb)
}

// docIndex is the go/doc view of the two packages whose types cross the
// wire, used to carry each type's and field's doc comment into the
// generated document as its description. It is a presentation layer only:
// the schema structure comes from reflection over the live types.
type docIndex struct {
	types  map[string]string            // "api.AllocateArgs" → its doc comment
	fields map[string]map[string]string // "api.AllocateArgs" → field name → doc comment
}

// loadDocs builds the doc index from the api and spec package sources.
// The packages are parsed with the standard library (go/parser, go/doc),
// so the generator stays dependency-free beyond the module's own two
// runtime dependencies.
func loadDocs() (*docIndex, error) {
	idx := &docIndex{
		types:  map[string]string{},
		fields: map[string]map[string]string{},
	}
	for _, p := range []struct{ dir, pkg string }{
		{"internal/api", "api"},
		{"internal/spec", "spec"},
	} {
		if err := idx.load(p.dir, p.pkg); err != nil {
			return nil, err
		}
	}
	return idx, nil
}

func (idx *docIndex) load(dir, pkg string) error {
	fset := token.NewFileSet()
	pkgs, err := parser.ParseDir(fset, dir, func(fi os.FileInfo) bool {
		return !strings.HasSuffix(fi.Name(), "_test.go")
	}, parser.ParseComments)
	if err != nil {
		return fmt.Errorf("apigen: parsing %s for doc comments: %w", dir, err)
	}
	var astPkg *ast.Package
	for _, p := range pkgs {
		astPkg = p
	}
	if astPkg == nil {
		return fmt.Errorf("apigen: no package found in %s", dir)
	}
	dpkg := doc.New(astPkg, pkg, 0)
	for _, t := range dpkg.Types {
		key := pkg + "." + t.Name
		idx.types[key] = firstParagraph(t.Doc)
		if t.Decl == nil || len(t.Decl.Specs) == 0 {
			continue
		}
		ts, ok := t.Decl.Specs[0].(*ast.TypeSpec)
		if !ok {
			continue
		}
		st, ok := ts.Type.(*ast.StructType)
		if !ok || st.Fields == nil {
			continue
		}
		for _, f := range st.Fields.List {
			for _, n := range f.Names {
				if idx.fields[key] == nil {
					idx.fields[key] = map[string]string{}
				}
				idx.fields[key][n.Name] = firstParagraph(f.Doc.Text())
			}
		}
	}
	return nil
}

// firstParagraph is a doc comment's first paragraph as one line: the
// sentence a reader of the generated document sees as a description.
func firstParagraph(doc string) string {
	if i := strings.Index(doc, "\n\n"); i >= 0 {
		doc = doc[:i]
	}
	return strings.Join(strings.Fields(doc), " ")
}

// pkgShort maps a type's import path onto the short name the doc index
// is keyed by. Only the two wire packages are indexed; anything else
// simply has no descriptions.
func pkgShort(pkgPath string) string {
	switch {
	case strings.HasSuffix(pkgPath, "/internal/api"):
		return "api"
	case strings.HasSuffix(pkgPath, "/internal/spec"):
		return "spec"
	default:
		return pkgPath
	}
}

// yamlNull is the OpenAPI 3.1 null type, used for pointer fields: a nil
// pointer crosses the wire as null.
var yamlNull = yaml.MapSlice{{Key: "type", Value: "null"}}
