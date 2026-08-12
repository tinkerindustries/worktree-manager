package spec

import (
	"strconv"

	"github.com/goccy/go-yaml"
)

// EmitYAML marshals v as YAML with every string scalar quoted, unconditionally.
//
// A worktree may legitimately be slugged `no`, `on`, `off`, `yes` or `y`, and
// a YAML 1.1 emitter turns those into booleans on the way back in. The slug
// rule (M1 §4.1) admits them, so the hazard follows the slug everywhere and
// the answer is to quote every string scalar rather than to check whether a
// particular value happens to look boolean (ARCHITECTURE.md §8.2).
//
// Phase 5's descriptor emitter uses this same function, and the spec round-
// trip test exercises it: a worktree slugged `no` must come back as "no",
// never as false.
func EmitYAML(v any) ([]byte, error) {
	return yaml.MarshalWithOptions(v, yaml.CustomMarshaler[string](func(s string) ([]byte, error) {
		return []byte(strconv.Quote(s)), nil
	}))
}
