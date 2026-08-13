package spec

import (
	"testing"
)

// TestFixtureHooksEmitCoverage is exit criterion 2: every field in hooks and
// emit is exercised by at least one of the three fixtures. A field no
// fixture exercises gets deleted rather than kept; this test is the
// mechanical check that nothing slipped through.
func TestFixtureHooksEmitCoverage(t *testing.T) {
	compose := loadFixture(t, "compose-app")
	plain := loadFixture(t, "plain-app")
	vm := loadFixture(t, "vm-app")
	fixtures := []*Spec{compose, plain, vm}

	anyHook := func(pred func(h *Hook) bool) bool {
		for _, s := range fixtures {
			for _, name := range HookNames {
				if h := HookByName(&s.Hooks, name); h != nil && pred(h) {
					return true
				}
			}
		}
		return false
	}
	anyEmit := func(pred func(e *Emit) bool) bool {
		for _, s := range fixtures {
			if pred(&s.Emit) {
				return true
			}
		}
		return false
	}

	// The six hook names, each exercised at least once.
	for _, name := range HookNames {
		found := false
		for _, s := range fixtures {
			if HookByName(&s.Hooks, name) != nil {
				found = true
			}
		}
		if !found {
			t.Errorf("hook %q is not exercised by any fixture", name)
		}
	}

	// Hook fields.
	if !anyHook(func(h *Hook) bool { return h.Run != "" }) {
		t.Error("hook run is not exercised by any fixture")
	}
	if !anyHook(func(h *Hook) bool { return h.Timeout != "" }) {
		t.Error("hook timeout is not exercised by any fixture")
	}
	if !anyHook(func(h *Hook) bool { return len(h.Params) > 0 }) {
		t.Error("hook params is not exercised by any fixture")
	}
	if !anyHook(func(h *Hook) bool {
		for _, p := range h.Params {
			if p.Sticky {
				return true
			}
		}
		return false
	}) {
		t.Error("param sticky is not exercised by any fixture")
	}
	if !anyHook(func(h *Hook) bool {
		for _, p := range h.Params {
			if p.Default != "" {
				return true
			}
		}
		return false
	}) {
		t.Error("param default is not exercised by any fixture")
	}

	// The shorthand form (a string hook) must be exercised too.
	if !anyHook(func(h *Hook) bool { return true }) && false {
	}
	shorthand := false
	for _, s := range fixtures {
		// The shorthand parses into a Hook with no timeout and no
		// params; compose-app's install is the shorthand.
		if s.Hooks.Install != nil && s.Hooks.Install.Timeout == "" && len(s.Hooks.Install.Params) == 0 && s.Hooks.Install.Run != "" {
			shorthand = true
		}
	}
	if !shorthand {
		t.Error("the hook shorthand form is not exercised by any fixture")
	}

	// Emit fields.
	if !anyEmit(func(e *Emit) bool { return e.Descriptor.Filename != "" }) {
		t.Error("emit.descriptor.filename is not exercised by any fixture")
	}
	formats := map[string]bool{}
	for _, s := range fixtures {
		formats[s.Emit.Descriptor.Format] = true
	}
	if !formats["yaml"] || !formats["json"] {
		t.Errorf("descriptor formats exercised = %v, want both yaml and json", formats)
	}
	if !anyEmit(func(e *Emit) bool { return e.Env != nil && e.Env.Path != "" }) {
		t.Error("emit.env.path is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Env != nil && e.Env.Seed }) {
		t.Error("emit.env.seed is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Env != nil && len(e.Env.Keys) > 0 }) {
		t.Error("emit.env.keys is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Reader != nil && e.Reader.Language == "go" }) {
		t.Error("emit.reader.language is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Reader != nil && e.Reader.Path != "" }) {
		t.Error("emit.reader.path is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Reader != nil && e.Reader.Package != "" }) {
		t.Error("emit.reader.package is not exercised by any fixture")
	}
	if !anyEmit(func(e *Emit) bool { return e.Reader != nil && e.Reader.EnvVar != "" }) {
		t.Error("emit.reader.env_var is not exercised by any fixture")
	}
}

// TestFixtureResourceCoverage is the same discipline for the resource
// fields: every resource type and every resource field appears in a fixture,
// so the schema has no dead fields.
func TestFixtureResourceCoverage(t *testing.T) {
	compose := loadFixture(t, "compose-app")
	plain := loadFixture(t, "plain-app")
	vm := loadFixture(t, "vm-app")
	all := make([]*Resource, 0, len(compose.Resources)+len(plain.Resources)+len(vm.Resources))
	for i := range compose.Resources {
		all = append(all, &compose.Resources[i])
	}
	for i := range plain.Resources {
		all = append(all, &plain.Resources[i])
	}
	for i := range vm.Resources {
		all = append(all, &vm.Resources[i])
	}

	types := map[string]bool{}
	fields := map[string]bool{}
	for _, r := range all {
		types[r.Type] = true
		if r.Form != nil {
			fields["form"] = true
		}
		if r.Size != nil {
			fields["size"] = true
		}
		if r.Offset != nil {
			fields["offset"] = true
		}
		if r.Kind != nil {
			fields["kind"] = true
		}
		if len(r.Files) > 0 {
			fields["files"] = true
		}
		if r.Pool != nil {
			fields["pool"] = true
		}
		if r.OnExhaustion != nil {
			fields["on_exhaustion"] = true
		}
		if r.Template != nil {
			fields["template"] = true
		}
		if r.Default != nil {
			fields["default"] = true
		}
		if r.Flag != nil {
			fields["flag"] = true
		}
		if r.Seed != nil {
			fields["seed"] = true
			if r.Seed.From != "" {
				fields["seed.from"] = true
			}
			if len(r.Seed.Modes) > 0 {
				fields["seed.modes"] = true
			}
			if r.Seed.Default != nil {
				fields["seed.default"] = true
			}
		}
		if r.Purge != nil {
			fields["purge"] = true
		}
		if r.Driver != nil {
			fields["driver"] = true
		}
		if r.MaxConcurrent != nil {
			fields["max_concurrent"] = true
		}
		if r.KeepFlag != nil {
			fields["keep_flag"] = true
		}
	}
	for _, typ := range []string{"port", "namespace", "cidr", "state-path", "machine"} {
		if !types[typ] {
			t.Errorf("resource type %q is not exercised by any fixture", typ)
		}
	}
	for _, f := range []string{"form", "size", "offset", "kind", "files", "pool", "on_exhaustion", "template",
		"default", "flag", "seed", "seed.from", "seed.modes", "seed.default", "purge", "driver", "max_concurrent", "keep_flag"} {
		if !fields[f] {
			t.Errorf("resource field %q is not exercised by any fixture", f)
		}
	}
}

// TestFixtureReservedAndShared: reserved.ports and the hand-authored shared
// block are exercised by the fixtures too.
func TestFixtureReservedAndShared(t *testing.T) {
	compose := loadFixture(t, "compose-app")
	if len(compose.Reserved.Ports) != 2 || compose.Reserved.Ports[0] != 5319 || compose.Reserved.Ports[1] != 5320 {
		t.Errorf("compose-app reserved.ports = %v, want [5319 5320] (the production stack's fixed ports)", compose.Reserved.Ports)
	}
	if len(compose.Shared) == 0 || compose.Shared[0].Impact == "" {
		t.Errorf("compose-app shared block = %+v, want a hand-authored entry with impact", compose.Shared)
	}
	plain := loadFixture(t, "plain-app")
	sharedByDefault := false
	for _, r := range plain.Resources {
		if r.Default != nil && *r.Default == "shared" {
			sharedByDefault = true
		}
	}
	if !sharedByDefault {
		t.Error("plain-app has no resource with default: shared")
	}
}

// TestEveryTypeFieldListHasARule: the per-type "not valid for this type"
// lists are string literals, and a typo in one would disable a validation
// rule with no compile error. Every name in every list must have an entry
// in fieldSet.
func TestEveryTypeFieldListHasARule(t *testing.T) {
	lists := [][]string{
		{"kind", "files", "pool", "on_exhaustion", "template", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"},
		{"form", "size", "offset", "pool", "on_exhaustion", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"},
		{"form", "offset", "kind", "files", "template", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"},
		{"form", "size", "offset", "kind", "files", "pool", "on_exhaustion", "driver", "max_concurrent", "keep_flag"},
		{"form", "size", "offset", "kind", "files", "pool", "on_exhaustion", "default", "flag", "seed", "purge"},
	}
	seen := map[string]bool{}
	for _, list := range lists {
		for _, f := range list {
			seen[f] = true
			if _, ok := fieldSet[f]; !ok {
				t.Errorf("field %q appears in a per-type list with no rule in fieldSet", f)
			}
		}
	}
	for f := range fieldSet {
		if !seen[f] {
			t.Errorf("fieldSet has a rule for %q that no per-type list names", f)
		}
	}
}
