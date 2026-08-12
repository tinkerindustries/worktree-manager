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

func TestResolvePortsStrideAndGroup(t *testing.T) {
	s := loadFixture(t, "compose-app")
	table, err := Resolve(s, testContext(7, "brisk-otter"))
	if err != nil {
		t.Fatalf("Resolve: %v", err)
	}
	// stride: base + slot
	if got := table["api"].Value; got != 4207 {
		t.Errorf("api = %v, want 4207", got)
	}
	// group: base + slot×1 + (−1) = api − 1
	if got := table["proxy"].Value; got != 4206 {
		t.Errorf("proxy = %v, want 4206 (api − 1)", got)
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

func TestResolveCIDRExhausted(t *testing.T) {
	s := loadFixture(t, "vm-app")
	_, err := Resolve(s, testContext(65, "alpha"))
	if err == nil {
		t.Fatal("Resolve accepted slot 65, past the pool's 64 blocks")
	}
	var fe *FieldError
	if !asFieldError(err, &fe) {
		t.Fatalf("error = %v, want FieldError", err)
	}
	if !strings.Contains(fe.Reason, "exhausted") {
		t.Errorf("reason %q does not say the pool is exhausted", fe.Reason)
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

// TestExplainTablesDisjoint is exit criterion 3: `spec explain --slot 1` and
// `--slot 2`, given the same bases, produce resource tables from a pure
// function, and the two tables are disjoint — no resolved value appears in
// both. The fixture templates embed {slot} precisely so the property holds
// with the slug held constant, as the criterion reads it.
func TestExplainTablesDisjoint(t *testing.T) {
	for _, fixture := range []string{"compose-app", "vm-app"} {
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
			for name, r1 := range table1 {
				r2, ok := table2[name]
				if !ok {
					t.Errorf("resource %q missing from slot-2 table", name)
					continue
				}
				if fmt.Sprint(r1.Value) == fmt.Sprint(r2.Value) {
					t.Errorf("resource %q resolves to %v in both tables; tables are not disjoint", name, r1.Value)
				}
			}
		})
	}
}

// TestExplainTablesDisjointSameSlug pins the stricter reading of criterion
// 3: even with the slug held constant, slot 1 and slot 2 are disjoint,
// because the fixture templates embed {slot}.
func TestExplainTablesDisjointSameSlug(t *testing.T) {
	s := loadFixture(t, "compose-app")
	table1, err := Resolve(s, testContext(1, "brisk-otter"))
	if err != nil {
		t.Fatalf("Resolve slot 1: %v", err)
	}
	table2, err := Resolve(s, testContext(2, "brisk-otter"))
	if err != nil {
		t.Fatalf("Resolve slot 2: %v", err)
	}
	for name, r1 := range table1 {
		if fmt.Sprint(r1.Value) == fmt.Sprint(table2[name].Value) {
			t.Errorf("resource %q resolves to %v in both tables; tables are not disjoint", name, r1.Value)
		}
	}
}
