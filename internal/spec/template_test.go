package spec

import (
	"strings"
	"testing"
)

func TestTemplateVars(t *testing.T) {
	tests := []struct {
		tmpl string
		want []string
	}{
		{"{app}-{slug}", []string{"app", "slug"}},
		{"{compose}-test", []string{"compose"}},
		{"{home}/.bacio/worktrees/{slug}/db.sqlite", []string{"home", "slug"}},
		{"curl -fsS http://127.0.0.1:{api}/healthz", []string{"api"}},
		{"no variables", nil},
		{"{slot}", []string{"slot"}},
	}
	for _, tt := range tests {
		got, err := templateVars(tt.tmpl)
		if err != nil {
			t.Errorf("templateVars(%q): %v", tt.tmpl, err)
			continue
		}
		if len(got) != len(tt.want) {
			t.Errorf("templateVars(%q) = %v, want %v", tt.tmpl, got, tt.want)
			continue
		}
		for i := range got {
			if got[i] != tt.want[i] {
				t.Errorf("templateVars(%q) = %v, want %v", tt.tmpl, got, tt.want)
				break
			}
		}
	}
}

func TestTemplateVarsMalformed(t *testing.T) {
	for _, tmpl := range []string{"{app", "{}", "a { b"} {
		if _, err := templateVars(tmpl); err == nil {
			t.Errorf("templateVars(%q) succeeded, want error", tmpl)
		}
	}
}

func TestSubstitute(t *testing.T) {
	lookup := map[string]string{
		"app":  "bacio",
		"slug": "brisk-otter",
		"slot": "7",
	}
	got, err := substitute("{app}-{slug}-{slot}", func(name string) (string, bool) {
		v, ok := lookup[name]
		return v, ok
	})
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "bacio-brisk-otter-7" {
		t.Errorf("substitute = %q", got)
	}
}

func TestSubstituteUnknown(t *testing.T) {
	_, err := substitute("{nope}", func(string) (string, bool) { return "", false })
	if err == nil || !strings.Contains(err.Error(), "{nope}") {
		t.Errorf("substitute error = %v, want it to name {nope}", err)
	}
}

func TestSubstituteSinglePass(t *testing.T) {
	// A resolved value is never re-scanned for references.
	got, err := substitute("{a}", func(name string) (string, bool) {
		if name == "a" {
			return "{b}", true
		}
		return "", false
	})
	if err != nil {
		t.Fatalf("substitute: %v", err)
	}
	if got != "{b}" {
		t.Errorf("substitute = %q, want {b} untouched", got)
	}
}

// TestCycleDetection covers the topological sort: a DAG resolves, a cycle is
// named as a path.
func TestCycleDetection(t *testing.T) {
	dag := "version: 1\napp: x\nresources:\n" +
		"  - type: namespace\n    name: a\n    kind: compose\n    template: \"{b}\"\n" +
		"  - type: namespace\n    name: b\n    kind: compose\n    template: \"{app}-{slug}\"\n"
	s, err := Parse([]byte(dag))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if cyc := findTemplateCycle(s); cyc != nil {
		t.Errorf("findTemplateCycle(dag) = %v, want nil", cyc)
	}

	loop := "version: 1\napp: x\nresources:\n" +
		"  - type: namespace\n    name: a\n    kind: compose\n    template: \"{b}\"\n" +
		"  - type: namespace\n    name: b\n    kind: compose\n    template: \"{a}\"\n"
	s2, err := Parse([]byte(loop))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	cyc := findTemplateCycle(s2)
	if len(cyc) < 2 {
		t.Fatalf("findTemplateCycle(loop) = %v, want a cycle path", cyc)
	}
	if cyc[0] != cyc[len(cyc)-1] {
		t.Errorf("cycle %v does not close on itself", cyc)
	}
	if err := Validate(s2); err == nil {
		t.Error("Validate accepted a template cycle")
	}
}
