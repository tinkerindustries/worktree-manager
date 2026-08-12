package spec

// p6_test.go covers phase 6's spec surface: the reaper.binaries allowlist
// field and the Resolved JSON decode that normalises port values to int —
// the registry half of the rule the descriptor reader already applies.

import (
	"encoding/json"
	"strings"
	"testing"
)

// TestReaperBinariesValidation: the allowlist defaults to empty (a spec
// that names nothing signals nothing — the safe direction, and it must
// remain the default), and what is named must be name-shaped.
func TestReaperBinariesValidation(t *testing.T) {
	// Empty and absent both validate.
	base := "version: 1\napp: x\nresources:\n  - type: port\n    name: api\nemit:\n  descriptor:\n    filename: wt-env.yaml\n    format: yaml\n"
	for _, doc := range []string{base, base + "reaper:\n  binaries: [compose-app-dev]\n"} {
		sp, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("parsing:\n%s\n: %v", doc, err)
		}
		if err := Validate(sp); err != nil {
			t.Errorf("validating:\n%s\n: %v", doc, err)
		}
	}
	// Invalid names are refused with the field named. The backslash case is
	// single-quoted so YAML keeps it literally (double quotes would turn
	// \b into backspace).
	for _, bad := range []string{"", "a b", "/usr/bin/x", `a\b`} {
		quoted := `"` + strings.ReplaceAll(bad, `\`, `\\`) + `"`
		doc := "version: 1\napp: x\nreaper:\n  binaries: [" + quoted + "]\nresources:\n  - type: port\n    name: api\nemit:\n  descriptor:\n    filename: wt-env.yaml\n    format: yaml\n"
		sp, err := Parse([]byte(doc))
		if err != nil {
			t.Fatalf("parsing %q: %v", bad, err)
		}
		err = Validate(sp)
		if err == nil {
			t.Errorf("binary name %q validated; want a refusal", bad)
			continue
		}
		fe, ok := err.(*FieldError)
		if !ok || !strings.HasPrefix(fe.Field, "reaper.binaries") {
			t.Errorf("refusal for %q = %v, want a FieldError on reaper.binaries", bad, err)
		}
	}
}

// TestReaperBinariesDefaultsEmpty: the parse of a spec without the reaper
// block yields an empty allowlist — the closed rail.
func TestReaperBinariesDefaultsEmpty(t *testing.T) {
	sp, err := Parse([]byte("version: 1\napp: x\nemit:\n  descriptor:\n    filename: wt-env.yaml\n    format: yaml\n"))
	if err != nil {
		t.Fatalf("parsing: %v", err)
	}
	if len(sp.Reaper.Binaries) != 0 {
		t.Errorf("default allowlist = %v, want empty", sp.Reaper.Binaries)
	}
}

// TestResolvedJSONDecodeNormalisesPortValues: a port resource's value
// round-trips through encoding/json as int, not float64 — the registry's
// half of the rule the descriptor reader applies, without which the reaper
// would silently see no ports and verify would report type errors.
func TestResolvedJSONDecodeNormalisesPortValues(t *testing.T) {
	data := []byte(`{"type":"port","value":6001}`)
	var r Resolved
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	if v, ok := r.Value.(int); !ok || v != 6001 {
		t.Errorf("port value after decode = %T %v, want int 6001", r.Value, r.Value)
	}
	// Non-port values keep their types.
	data = []byte(`{"type":"state-path","value":"/home/x"}`)
	if err := json.Unmarshal(data, &r); err != nil {
		t.Fatalf("unmarshaling: %v", err)
	}
	if v, ok := r.Value.(string); !ok || v != "/home/x" {
		t.Errorf("state-path value after decode = %T %v, want string", r.Value, r.Value)
	}
	// The map form (the registry's shape) normalises too.
	var file struct {
		Resources map[string]Resolved `json:"resources"`
	}
	if err := json.Unmarshal([]byte(`{"resources":{"api":{"type":"port","value":4201}}}`), &file); err != nil {
		t.Fatalf("unmarshaling the map: %v", err)
	}
	if v, ok := file.Resources["api"].Value.(int); !ok || v != 4201 {
		t.Errorf("map port value after decode = %T %v, want int 4201", file.Resources["api"].Value, file.Resources["api"].Value)
	}
}
