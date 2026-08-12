package spec

import (
	"strings"
	"testing"
)

// descriptorLike is the minimal shape of the phase-5 descriptor, enough to
// prove the quoting rule: every string scalar is quoted, so a slug that is a
// YAML 1.1 boolean word round-trips as a string.
type descriptorLike struct {
	Version int    `yaml:"version"`
	App     string `yaml:"app"`
	Slug    string `yaml:"slug"`
	Slot    int    `yaml:"slot"`
}

// TestYAMLQuotingRoundTrip is exit criterion 5: a spec that emits a YAML
// descriptor round-trips a worktree slugged `no` without turning it into
// false. The emission is unconditional — every string scalar is quoted — so
// `on`, `off`, `yes` and `y` are covered by the same rule.
func TestYAMLQuotingRoundTrip(t *testing.T) {
	for _, slug := range []string{"no", "on", "off", "yes", "y"} {
		t.Run(slug, func(t *testing.T) {
			out, err := EmitYAML(descriptorLike{Version: 1, App: "bacio", Slug: slug, Slot: 7})
			if err != nil {
				t.Fatalf("EmitYAML: %v", err)
			}
			if !strings.Contains(string(out), `slug: "`+slug+`"`) {
				t.Errorf("emitted YAML does not quote the slug:\n%s", out)
			}
			var back descriptorLike
			if err := unmarshalStrict(out, &back); err != nil {
				t.Fatalf("re-parsing emitted YAML: %v", err)
			}
			if back.Slug != slug {
				t.Errorf("slug round-tripped as %q, want %q (a YAML 1.1 parser would coerce it to a boolean)", back.Slug, slug)
			}
			if back.Version != 1 || back.App != "bacio" || back.Slot != 7 {
				t.Errorf("round-trip changed other fields: %+v", back)
			}
		})
	}
}

// TestYAMLQuotingIsUnconditional pins the rule that quoting applies to every
// string scalar, not just boolean-looking ones.
func TestYAMLQuotingIsUnconditional(t *testing.T) {
	out, err := EmitYAML(descriptorLike{Version: 1, App: "compose-app", Slug: "brisk-otter", Slot: 1})
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	for _, want := range []string{`version: 1`, `app: "compose-app"`, `slug: "brisk-otter"`, `slot: 1`} {
		if !strings.Contains(string(out), want) {
			t.Errorf("emitted YAML lacks %q:\n%s", want, out)
		}
	}
}

// TestSpecEmitRoundTrip emits a whole spec and re-parses it: the schema is
// round-trippable through the same emitter the descriptor will use.
func TestSpecEmitRoundTrip(t *testing.T) {
	s := loadFixture(t, "compose-app")
	out, err := EmitYAML(s)
	if err != nil {
		t.Fatalf("EmitYAML: %v", err)
	}
	back, err := Parse(out)
	if err != nil {
		t.Fatalf("re-parsing emitted spec: %v\n%s", err, out)
	}
	if err := Validate(back); err != nil {
		t.Fatalf("re-parsed spec does not validate: %v", err)
	}
	if back.App != s.App || len(back.Resources) != len(s.Resources) {
		t.Errorf("round-trip changed the spec: %+v", back)
	}
}
