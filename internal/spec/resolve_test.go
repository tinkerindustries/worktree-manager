package spec

import (
	"fmt"
	"strings"
	"testing"
)

// testContext builds a resolution context for the fixtures.
func testContext(slot int, slug string) Context {
	return Context{
		Slug:     slug,
		Slot:     slot,
		Home:     "/Users/test",
		Worktree: "/Users/test/worktrees/" + slug,
		Bases:    map[string]int{"api": 4200, "proxy": 4200},
	}
}

func TestResolvePortsStride(t *testing.T) {
	s := loadFixture(t, "plain-app")
	table, err := Resolve(s, testContext(7, "brisk-otter"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// stride: base + slot
	if got := table["api"].Value; got != 4207 {
		t.Errorf("api = %v, want 4207", got)
	}
}

func TestResolvePortsGroup(t *testing.T) {
	s := loadFixture(t, "compose-app")
	table, err := Resolve(s, testContext(7, "brisk-otter"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// group of size 2: base + slot×2 + offset
	if got := table["proxy"].Value; got != 4214 {
		t.Errorf("proxy = %v, want 4214", got)
	}
	if got := table["api"].Value; got != 4215 {
		t.Errorf("api = %v, want 4215 (proxy + 1)", got)
	}
	if table["compose"].Value != "compose-app-brisk-otter-7" {
		t.Errorf("compose = %v", table["compose"].Value)
	}
	if table["compose_test"].Value != "compose-app-brisk-otter-7-test" {
		t.Errorf("compose_test = %v", table["compose_test"].Value)
	}
}

func TestResolveMissingBase(t *testing.T) {
	s := loadFixture(t, "compose-app")
	ctx := testContext(1, "alpha")
	delete(ctx.Bases, "proxy")
	_, err := Resolve(s, ctx)
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("Resolve error = %v, want FieldError", err)
	}
	if !strings.Contains(fe.Reason, `port resource "proxy"`) {
		t.Errorf("reason %q does not name the port resource", fe.Reason)
	}
}

func TestResolveSlugRefused(t *testing.T) {
	s := loadFixture(t, "compose-app")
	_, err := Resolve(s, testContext(1, "Not A Slug"))
	if err == nil {
		t.Fatal("Resolve accepted an invalid slug")
	}
}

func TestResolveSlotRefused(t *testing.T) {
	s := loadFixture(t, "compose-app")
	if _, err := Resolve(s, testContext(0, "alpha")); err == nil {
		t.Fatal("Resolve accepted slot 0")
	}
	if _, err := Resolve(s, testContext(33, "alpha")); err == nil {
		t.Fatal("Resolve accepted a slot above slots.max")
	}
}

func TestResolveCIDRSlicing(t *testing.T) {
	s := loadFixture(t, "vm-app")
	table, err := Resolve(s, testContext(1, "alpha"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	if table["egress"].Value != "172.30.0.0/22" {
		t.Errorf("slot 1 egress = %v, want 172.30.0.0/22", table["egress"].Value)
	}
	table2, err := Resolve(s, testContext(2, "alpha"))
	if err != nil {
		t.Fatalf("Resolve slot 2: %v", err)
	}
	if table2["egress"].Value != "172.30.4.0/22" {
		t.Errorf("slot 2 egress = %v, want 172.30.4.0/22", table2["egress"].Value)
	}
	if table["vm"].Value != "vm-app-alpha-1" {
		t.Errorf("slot 1 vm = %v", table["vm"].Value)
	}
}

// TestResolveCIDRExhausted pins the phase-8 exhaustion behaviour
// (03-drivers.md §4.3): the vm-app fixture's on_exhaustion is shared-pool
// (the default), so a slot past the pool's 64 blocks falls back to the
// shared pool itself — the loud warning is the driver's — and a spec that
// declares on_exhaustion: fail stops with a FieldError naming the
// exhaustion instead.
func TestResolveCIDRExhausted(t *testing.T) {
	s := loadFixture(t, "vm-app")
	table, err := Resolve(s, testContext(65, "alpha"))
	if err != nil {
		t.Fatalf("shared-pool exhaustion must fall back, not fail: %v", err)
	}
	if table["egress"].Value != "172.30.0.0/16" {
		t.Errorf("the fallback value must be the shared pool itself, got %v", table["egress"].Value)
	}

	fail := &Spec{
		Version: 1, App: "vm-app",
		Slots: Slots{Max: intPtr(100)},
		Resources: []Resource{{
			Type: "cidr", Name: "egress",
			Pool: strPtr("172.30.0.0/16"), Size: intPtr(22),
			OnExhaustion: strPtr("fail"),
		}},
		Emit: Emit{Descriptor: Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	_, err = Resolve(fail, testContext(65, "alpha"))
	if err == nil {
		t.Fatal("on_exhaustion: fail must refuse slot 65, past the pool's 64 blocks")
	}
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("error = %v, want FieldError", err)
	}
	if !strings.Contains(fe.Reason, "exhausted") || !strings.Contains(fe.Reason, "fail") {
		t.Errorf("reason %q must name the exhaustion and the mode", fe.Reason)
	}
}

func TestResolveOverlongNameRefused(t *testing.T) {
	// The worst-case cap check is validation's job; resolution refuses
	// against the actual context too.
	doc := `version: 1
app: compose-app
slots:
  max: 4
resources:
  - type: namespace
    name: compose
    kind: compose
    template: "{app}-{slug}-{slug}-{slug}-{slug}-{slug}"
emit:
  descriptor:
    filename: wt-env.yaml
    format: yaml
`
	s, err := Parse([]byte(doc))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if err := Validate(s); err == nil {
		t.Fatal("Validate accepted a template that overflows the cap at the worst-case slug")
	}
	_, err = Resolve(s, testContext(1, "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"))
	if err == nil {
		t.Fatal("Resolve accepted an over-long resolved namespace")
	}
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("error = %v, want FieldError", err)
	}
	if !strings.Contains(fe.Reason, "cap is 63") {
		t.Errorf("reason %q does not name the cap", fe.Reason)
	}
}

// isolatedNames returns the resources a spec isolates per worktree, which is
// every resource except one declared `default: shared` (03-drivers.md §3.4).
// A shared resource resolves to the same value in every slot by design, so it
// is excluded from the disjointness check rather than exempted case by case.
func isolatedNames(s *Spec) map[string]bool {
	names := make(map[string]bool, len(s.Resources))
	for _, r := range s.Resources {
		if r.Default != nil && *r.Default == "shared" {
			continue
		}
		names[r.Name] = true
	}
	return names
}

// assertTablesDisjoint checks exit criterion 3 as a set intersection over all
// isolated values, not resource name by resource name. Comparing like-named
// rows would pass a spec whose slot-1 api collides with its slot-2 proxy,
// which is the collision two worktrees actually suffer.
func assertTablesDisjoint(t *testing.T, s *Spec, table1, table2 map[string]Resolved) {
	t.Helper()
	isolated := isolatedNames(s)
	seen := make(map[string]string, len(table1))
	for name, r := range table1 {
		if isolated[name] {
			seen[fmt.Sprint(r.Value)] = name
		}
	}
	for name, r := range table2 {
		if !isolated[name] {
			continue
		}
		if other, clash := seen[fmt.Sprint(r.Value)]; clash {
			t.Errorf("slot-2 %q and slot-1 %q both resolve to %v; the tables are not disjoint", name, other, r.Value)
		}
	}
}

// TestExplainTablesDisjoint is exit criterion 3: `spec explain --slot 1` and
// `--slot 2`, given the same bases, produce resource tables from a pure
// function, and the two tables are disjoint — no isolated value appears in
// both.
func TestExplainTablesDisjoint(t *testing.T) {
	for _, fixture := range []string{"compose-app", "plain-app", "vm-app"} {
		t.Run(fixture, func(t *testing.T) {
			s := loadFixture(t, fixture)
			table1, err := Resolve(s, testContext(1, "slot-one"))
			if err != nil {
				t.Fatalf("Resolve slot 1: %v", err)
			}
			table2, err := Resolve(s, testContext(2, "slot-two"))
			if err != nil {
				t.Fatalf("Resolve slot 2: %v", err)
			}
			if len(table1) != len(table2) {
				t.Fatalf("tables differ in size: %d vs %d", len(table1), len(table2))
			}
			assertTablesDisjoint(t, s, table1, table2)
		})
	}
}

// TestExplainTablesDisjointSameSlug pins the stricter reading of criterion
// 3: even with the slug held constant, slot 1 and slot 2 are disjoint,
// because the fixture templates embed {slot}.
func TestExplainTablesDisjointSameSlug(t *testing.T) {
	for _, fixture := range []string{"compose-app", "plain-app", "vm-app"} {
		t.Run(fixture, func(t *testing.T) {
			s := loadFixture(t, fixture)
			table1, err := Resolve(s, testContext(1, "brisk-otter"))
			if err != nil {
				t.Fatalf("Resolve slot 1: %v", err)
			}
			table2, err := Resolve(s, testContext(2, "brisk-otter"))
			if err != nil {
				t.Fatalf("Resolve slot 2: %v", err)
			}
			assertTablesDisjoint(t, s, table1, table2)
		})
	}
}

// intPtr and strPtr build the pointer forms the spec's optional fields
// take; the spec package's tests otherwise parse specs from YAML.
func intPtr(i int) *int       { return &i }
func strPtr(s string) *string { return &s }
