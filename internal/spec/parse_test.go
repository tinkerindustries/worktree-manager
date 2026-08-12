package spec

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// fixturePath returns the absolute path of a fixture spec.
func fixturePath(t *testing.T, name string) string {
	t.Helper()
	abs, err := filepath.Abs(filepath.Join("..", "..", "testdata", "fixtures", name, SpecFilename))
	if err != nil {
		t.Fatal(err)
	}
	return abs
}

// loadFixture parses and validates one fixture spec.
func loadFixture(t *testing.T, name string) *Spec {
	t.Helper()
	data, err := os.ReadFile(fixturePath(t, name))
	if err != nil {
		t.Fatalf("reading %s: %v", name, err)
	}
	s, err := Parse(data)
	if err != nil {
		t.Fatalf("parsing %s: %v", name, err)
	}
	if err := Validate(s); err != nil {
		t.Fatalf("validating %s: %v", name, err)
	}
	return s
}

func TestParseFixtureSpecs(t *testing.T) {
	for _, name := range []string{"compose-app", "plain-app", "vm-app"} {
		t.Run(name, func(t *testing.T) {
			s := loadFixture(t, name)
			if s.App != name {
				t.Errorf("app = %q, want %q", s.App, name)
			}
			if s.Version != 1 {
				t.Errorf("version = %d, want 1", s.Version)
			}
		})
	}
}

func TestParseVersionRefusal(t *testing.T) {
	doc := "version: 2\napp: future\n"
	_, err := Parse([]byte(doc))
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("Parse error = %v, want a FieldError", err)
	}
	if fe.Field != "version" {
		t.Errorf("field = %q, want %q", fe.Field, "version")
	}
	if !strings.Contains(fe.Reason, "unsupported version 2") {
		t.Errorf("reason %q does not name the version found", fe.Reason)
	}
	if !strings.Contains(fe.Reason, "[1]") {
		t.Errorf("reason %q does not name the supported versions", fe.Reason)
	}
}

func TestParseVersionRequired(t *testing.T) {
	doc := "app: no-version\n"
	_, err := Parse([]byte(doc))
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("Parse error = %v, want a FieldError", err)
	}
	if fe.Field != "version" {
		t.Errorf("field = %q, want %q", fe.Field, "version")
	}
}

func TestParseUnknownFieldRefused(t *testing.T) {
	doc := "version: 1\napp: bacio\nbogus: 3\n"
	_, err := Parse([]byte(doc))
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("Parse error = %v, want a FieldError", err)
	}
	if fe.Field != "bogus" {
		t.Errorf("field = %q, want %q", fe.Field, "bogus")
	}
}

func TestParseUnknownResourceType(t *testing.T) {
	doc := "version: 1\napp: x\nresources:\n  - type: floppy\n    name: f\n"
	parsed, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	// The unknown type is refused at validation, which can name the
	// field (resources[i].type) — parse alone cannot see the index.
	err = Validate(parsed)
	if err == nil {
		t.Fatal("Validate succeeded, want refusal of unknown resource type")
	}
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("error = %v, want FieldError", err)
	}
	if fe.Field != "resources[0].type" {
		t.Errorf("field = %q, want resources[0].type", fe.Field)
	}
	if !strings.Contains(fe.Reason, "floppy") {
		t.Errorf("reason %q does not name the type", fe.Reason)
	}
}

func TestParseHookShorthandAndFullForm(t *testing.T) {
	doc := `version: 1
app: x
hooks:
  install: "pnpm install"
  build:
    run: go build ./...
    timeout: 10m
    params:
      profile:
        sticky: true
        default: dev
emit:
  descriptor:
    filename: wt-env.yaml
    format: yaml
`
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Hooks.Install == nil || s.Hooks.Install.Run != "pnpm install" {
		t.Errorf("shorthand install = %+v, want run %q", s.Hooks.Install, "pnpm install")
	}
	if s.Hooks.Install.Timeout != "" || len(s.Hooks.Install.Params) != 0 {
		t.Errorf("shorthand install should carry no timeout and no params, got %+v", s.Hooks.Install)
	}
	b := s.Hooks.Build
	if b == nil || b.Run != "go build ./..." || b.Timeout != "10m" {
		t.Fatalf("build hook = %+v", b)
	}
	p, ok := b.Params["profile"]
	if !ok || !p.Sticky || p.Default != "dev" {
		t.Errorf("build params = %+v, want sticky profile=dev", b.Params)
	}
	if err := Validate(s); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

// asFieldError is errors.As for *FieldError.
func asFieldError(err error, target **FieldError) bool {
	for err != nil {
		if fe, ok := err.(*FieldError); ok {
			*target = fe
			return true
		}
		u, ok := err.(interface{ Unwrap() error })
		if !ok {
			return false
		}
		err = u.Unwrap()
	}
	return false
}
