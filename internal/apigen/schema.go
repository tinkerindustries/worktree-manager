package apigen

import (
	"encoding/json"
	"reflect"
	"strings"

	"github.com/goccy/go-yaml"
)

// schema.go is the reflect-based JSON Schema walker: it turns the Go
// types that cross the wire — the api *Args/*Result types and everything
// they reach into, including the spec subtree — into OpenAPI 3.1
// component schemas. The wire shape is derived from the structs exactly
// as encoding/json renders them: the json tag names the field (a field
// without a json tag crosses the wire under its Go name — that is how
// the spec subtree is carried), omitempty makes it optional, and a
// pointer is nullable. This is the single source of truth the document
// is derived from; nothing here is hand-maintained beside the types.

// jsonRawMessage is the type of api.Request.Args and api.Response.Result:
// an already-encoded JSON value, which the schema leaves unconstrained.
var jsonRawMessage = reflect.TypeOf(json.RawMessage{})

// schemaWalker collects component schemas in first-visit order, so the
// generated document is deterministic across runs. Each named struct
// becomes one component, referenced by $ref; cycles are broken by the
// seen set, so a self-referential type renders as a reference to itself.
type schemaWalker struct {
	docs    *docIndex
	schemas yaml.MapSlice // component name → schema, in first-visit order
	seen    map[string]bool
}

func newSchemaWalker(docs *docIndex) *schemaWalker {
	return &schemaWalker{docs: docs, seen: map[string]bool{}}
}

// refName is the component reference for a type name.
func refName(name string) string {
	return "#/components/schemas/" + name
}

// ref is the component reference mapping for a type name — the value a
// schema entry takes when the type is described as a component.
func ref(name string) yaml.MapSlice {
	return yaml.MapSlice{{Key: "$ref", Value: refName(name)}}
}

// schemaFor renders the JSON Schema for one field type. A pointer
// renders as its element with null admitted; a named struct renders as a
// component reference (collecting the component on first sight).
func (w *schemaWalker) schemaFor(t reflect.Type) any {
	nullable := false
	for t.Kind() == reflect.Ptr {
		nullable = true
		t = t.Elem()
	}
	s := w.schemaNonNull(t)
	if !nullable {
		return s
	}
	if ms, ok := s.(yaml.MapSlice); ok && len(ms) == 1 && ms[0].Key == "$ref" {
		return yaml.MapSlice{
			{Key: "anyOf", Value: []any{ms, yamlNull}},
		}
	}
	// An inline schema (a scalar, a map, a slice): widen its type list
	// to admit null. A schema with no type entry — the unconstrained
	// shape for `any` — is already closed under null.
	if ms, ok := s.(yaml.MapSlice); ok {
		for i, item := range ms {
			if item.Key == "type" {
				ms[i] = yaml.MapItem{Key: "type", Value: []any{item.Value, "null"}}
				return ms
			}
		}
	}
	return s
}

// schemaNonNull renders the schema for a non-pointer type.
func (w *schemaWalker) schemaNonNull(t reflect.Type) any {
	// A named type: either a component (a struct) or a named scalar.
	if t.Name() != "" && t.PkgPath() != "" {
		if t == jsonRawMessage {
			return yaml.MapSlice{}
		}
		if t.PkgPath() == "time" && t.Name() == "Time" {
			return w.scalar("string", t)
		}
		if t.Kind() == reflect.Struct {
			return w.structSchema(t)
		}
		return w.scalar(jsonTypeName(t.Kind()), t)
	}
	switch t.Kind() {
	case reflect.String:
		return w.scalar("string", t)
	case reflect.Bool:
		return w.scalar("boolean", t)
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return w.scalar("integer", t)
	case reflect.Float32, reflect.Float64:
		return w.scalar("number", t)
	case reflect.Slice, reflect.Array:
		if t.Elem().Kind() == reflect.Uint8 {
			// A bare byte slice is a byte string; json.RawMessage (the
			// one named []byte that crosses the wire) is handled above.
			return w.scalar("string", t)
		}
		return yaml.MapSlice{
			{Key: "type", Value: "array"},
			{Key: "items", Value: w.schemaFor(t.Elem())},
		}
	case reflect.Map:
		return yaml.MapSlice{
			{Key: "type", Value: "object"},
			{Key: "additionalProperties", Value: w.schemaFor(t.Elem())},
		}
	case reflect.Struct:
		// An unnamed struct (declared inline): rendered in place, never a
		// component — a component needs a name to be referenced by.
		return w.buildStruct(t)
	default:
		// An interface (any), a func, a chan: unconstrained — the wire
		// value is opaque to the schema.
		return yaml.MapSlice{}
	}
}

// scalar renders a scalar schema, carrying the type's doc comment as
// its description when the package is indexed.
func (w *schemaWalker) scalar(typ string, t reflect.Type) yaml.MapSlice {
	s := yaml.MapSlice{{Key: "type", Value: typ}}
	if d := w.typeDoc(t); d != "" {
		s = append(s, yaml.MapItem{Key: "description", Value: d})
	}
	return s
}

// structSchema collects one named struct as a component and returns its
// reference. The component is registered before its fields are walked,
// so a self-reference renders as a reference rather than recursing
// forever.
func (w *schemaWalker) structSchema(t reflect.Type) any {
	key := t.PkgPath() + "." + t.Name()
	if w.seen[key] {
		return ref(t.Name())
	}
	w.seen[key] = true
	idx := len(w.schemas)
	w.schemas = append(w.schemas, yaml.MapItem{Key: t.Name(), Value: nil})
	w.schemas[idx].Value = w.buildStruct(t)
	return ref(t.Name())
}

// buildStruct renders one struct's object schema: type, the type's doc
// comment, the properties in field order, and the required list (every
// field without omitempty).
func (w *schemaWalker) buildStruct(t reflect.Type) yaml.MapSlice {
	s := yaml.MapSlice{{Key: "type", Value: "object"}}
	if d := w.typeDoc(t); d != "" {
		s = append(s, yaml.MapItem{Key: "description", Value: d})
	}
	props := yaml.MapSlice{}
	var required []any
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if f.PkgPath != "" {
			continue // unexported fields never cross the wire
		}
		name, optional, ok := jsonField(f)
		if !ok {
			continue // json:"-"
		}
		prop := w.schemaFor(f.Type)
		if d := w.fieldDoc(t, f.Name); d != "" {
			prop = withDescription(prop, d)
		}
		props = append(props, yaml.MapItem{Key: name, Value: prop})
		if !optional {
			required = append(required, name)
		}
	}
	s = append(s, yaml.MapItem{Key: "properties", Value: props})
	if len(required) > 0 {
		s = append(s, yaml.MapItem{Key: "required", Value: required})
	}
	return s
}

// jsonField returns a field's wire name, whether it is optional
// (omitempty), and whether it crosses the wire at all. A field with no
// json tag crosses the wire under its Go name — exactly how the spec
// subtree is carried — and json:"-" fields never cross it.
func jsonField(f reflect.StructField) (name string, optional, ok bool) {
	tag := f.Tag.Get("json")
	if tag == "-" {
		return "", false, false
	}
	if tag == "" {
		return f.Name, false, true
	}
	parts := strings.Split(tag, ",")
	name = parts[0]
	if name == "" {
		name = f.Name
	}
	for _, p := range parts[1:] {
		if p == "omitempty" {
			optional = true
		}
	}
	return name, optional, true
}

// withDescription attaches a description to a rendered schema, whether
// it is a component reference (wrapped in allOf, since a $ref cannot
// carry siblings) or an inline schema.
func withDescription(s any, desc string) any {
	ms, ok := s.(yaml.MapSlice)
	if !ok {
		return s
	}
	for _, item := range ms {
		if item.Key == "$ref" {
			return yaml.MapSlice{
				{Key: "allOf", Value: []any{ms}},
				{Key: "description", Value: desc},
			}
		}
	}
	return append(yaml.MapSlice{{Key: "description", Value: desc}}, ms...)
}

// typeDoc is a named type's first-paragraph doc comment, "" when the
// package is not indexed or the type has none.
func (w *schemaWalker) typeDoc(t reflect.Type) string {
	if w.docs == nil {
		return ""
	}
	return w.docs.types[pkgShort(t.PkgPath())+"."+t.Name()]
}

// fieldDoc is one field's first-paragraph doc comment, "" when absent.
func (w *schemaWalker) fieldDoc(t reflect.Type, field string) string {
	if w.docs == nil {
		return ""
	}
	key := pkgShort(t.PkgPath()) + "." + t.Name()
	if m := w.docs.fields[key]; m != nil {
		return m[field]
	}
	return ""
}

// jsonTypeName maps a reflect kind onto its JSON Schema type name.
func jsonTypeName(k reflect.Kind) string {
	switch k {
	case reflect.String:
		return "string"
	case reflect.Bool:
		return "boolean"
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64:
		return "integer"
	case reflect.Float32, reflect.Float64:
		return "number"
	default:
		return ""
	}
}
