package spec

import (
	"fmt"
	"regexp"

	"github.com/goccy/go-yaml"
	"github.com/goccy/go-yaml/ast"
)

// unmarshalStrict decodes YAML refusing unknown fields, so a spec field that
// this schema does not know (a typo, or a field from a newer schema version)
// is refused whole rather than silently ignored.
func unmarshalStrict(data []byte, v any) error {
	return yaml.UnmarshalWithOptions(data, v, yaml.DisallowUnknownField())
}

// UnmarshalYAML implements yaml.NodeUnmarshaler: the shorthand form
// `install: "pnpm install"` is accepted as equivalent to
// `install: {run: "pnpm install"}`, since a hook with no timeout and no
// params is the common case. Any mapping form is decoded strictly.
func (h *Hook) UnmarshalYAML(node ast.Node) error {
	if node.Type() == ast.StringType {
		var s string
		if err := yaml.Unmarshal([]byte(node.String()), &s); err != nil {
			return err
		}
		h.Run = s
		return nil
	}
	type plain Hook
	var p plain
	if err := unmarshalStrict([]byte(node.String()), &p); err != nil {
		return err
	}
	*h = Hook(p)
	return nil
}

var unknownFieldRe = regexp.MustCompile(`unknown field "([^"]+)"`)

// parseFieldError converts a goccy error into a FieldError where the field
// can be named. goccy names unknown fields itself; everything else keeps the
// parser's message as the reason.
func parseFieldError(err error) error {
	if m := unknownFieldRe.FindStringSubmatch(err.Error()); m != nil {
		return &FieldError{Field: m[1], Reason: "unknown field"}
	}
	return &FieldError{Field: "", Reason: err.Error()}
}

// FieldError is a validation or parse failure naming the field and the
// reason. It is the error type the spec library returns for every refusal.
type FieldError struct {
	Field  string
	Reason string
}

func (e *FieldError) Error() string {
	if e.Field == "" {
		return e.Reason
	}
	return fmt.Sprintf("%s: %s", e.Field, e.Reason)
}
