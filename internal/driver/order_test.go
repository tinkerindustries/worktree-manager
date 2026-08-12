package driver

// order_test.go pins the sequencing contract of 03-drivers.md §5: apply in
// dependency order derived from the template references, teardown in
// reverse. A scripted driver records the calls so the order is observable.

import (
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// scriptedDriver is a test driver whose apply and teardown record their
// calls and fail on demand.
type scriptedDriver struct {
	typ         string
	apply       bool
	teardown    bool
	gates       bool
	applyErr    map[string]error // resource name → apply error
	teardownErr map[string]error // resource name → teardown error
	applied     []string
	tornDown    []string
}

func (d *scriptedDriver) Type() string          { return d.typ }
func (d *scriptedDriver) HasApply() bool        { return d.apply }
func (d *scriptedDriver) HasTeardown() bool     { return d.teardown }
func (d *scriptedDriver) GatesAllocation() bool { return d.gates }

func (d *scriptedDriver) Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error) {
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		return nil, err
	}
	return table[r.Name].Value, nil
}

func (d *scriptedDriver) Probe(*spec.Resource, any, Env) ProbeResult { return ProbeFree }

func (d *scriptedDriver) Apply(r *spec.Resource, value any, env Env) (ApplyResult, error) {
	d.applied = append(d.applied, r.Name)
	if err := d.applyErr[r.Name]; err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{Notes: []string{"applied " + r.Name}}, nil
}

func (d *scriptedDriver) Teardown(r *spec.Resource, value any, env Env) error {
	d.tornDown = append(d.tornDown, r.Name)
	if err := d.teardownErr[r.Name]; err != nil {
		return err
	}
	return nil
}

func (d *scriptedDriver) Verify(*spec.Resource, any, Env) ([]Finding, error) { return nil, nil }
func (d *scriptedDriver) BlastRadius(*spec.Resource, *spec.Spec) string      { return "prose" }

// chainSpec builds a spec of state-path resources where every later one
// references the previous: a, b (references a), c (references b).
func chainSpec(t *testing.T, home string) *spec.Spec {
	t.Helper()
	s := &spec.Spec{
		Version: 1, App: "chain",
		Slots: spec.Slots{Max: intPtr(32)},
		Resources: []spec.Resource{
			{Type: "state-path", Name: "a", Template: strPtr(home + "/{slug}/a")},
			{Type: "state-path", Name: "b", Template: strPtr(home + "/{slug}/b")},
			{Type: "state-path", Name: "c", Template: strPtr(home + "/{slug}/c")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	// Make the dependency explicit the way templates express it: b's
	// template references a, c's references b.
	s.Resources[1].Template = strPtr(home + "/{slug}/{a}/b")
	s.Resources[2].Template = strPtr(home + "/{slug}/{b}/c")
	if err := spec.Validate(s); err != nil {
		t.Fatalf("chain spec does not validate: %v", err)
	}
	return s
}

func chainValues(t *testing.T, s *spec.Spec, home string) map[string]spec.Resolved {
	t.Helper()
	table, err := spec.Resolve(s, spec.Context{
		App: "chain", Slug: "wt-1", Slot: 1,
		Home: home, Worktree: home + "/wt-1",
	})
	if err != nil {
		t.Fatalf("resolving the chain spec: %v", err)
	}
	return table
}

// TestDependencyOrderPinsApplyAndTeardownOrder is exit criterion 8's first
// half: apply runs in the order the templates imply — a, then b (whose
// template references a), then c (which references b) — and teardown runs
// in reverse.
func TestDependencyOrderPinsApplyAndTeardownOrder(t *testing.T) {
	home := t.TempDir()
	s := chainSpec(t, home)
	reg := NewRegistry(&scriptedDriver{typ: "state-path", apply: true, teardown: true})

	order := reg.ApplyOrder(s)
	if len(order) != 3 || order[0] != "a" || order[1] != "b" || order[2] != "c" {
		t.Fatalf("ApplyOrder = %v, want [a b c]", order)
	}
	teardown := reg.TeardownOrder(s)
	if len(teardown) != 3 || teardown[0] != "c" || teardown[1] != "b" || teardown[2] != "a" {
		t.Fatalf("TeardownOrder = %v, want [c b a]", teardown)
	}
}

// TestApplyAllOrderAndRollback is exit criterion 8: apply runs in the order
// the templates imply, and a failure part-way through tears down in reverse.
func TestApplyAllOrderAndRollback(t *testing.T) {
	home := t.TempDir()
	s := chainSpec(t, home)
	values := chainValues(t, s, home)
	env := Env{Spec: s, Home: home, Worktree: home + "/wt-1"}

	t.Run("apply in template order", func(t *testing.T) {
		d := &scriptedDriver{typ: "state-path", apply: true, teardown: true}
		rep := NewRegistry(d).ApplyAll(s, values, env)
		if rep.Failed != "" {
			t.Fatalf("ApplyAll failed: %v", rep.Err)
		}
		if len(d.applied) != 3 || d.applied[0] != "a" || d.applied[1] != "b" || d.applied[2] != "c" {
			t.Fatalf("apply order = %v, want [a b c]", d.applied)
		}
		if len(rep.Outcomes) != 3 || len(rep.Outcomes[0].Notes) == 0 {
			t.Fatalf("outcomes = %+v, want notes per resource", rep.Outcomes)
		}
	})

	t.Run("failure part-way rolls back in reverse", func(t *testing.T) {
		d := &scriptedDriver{typ: "state-path", apply: true, teardown: true,
			applyErr: map[string]error{"b": errScripted("apply of b fails")}}
		rep := NewRegistry(d).ApplyAll(s, values, env)
		if rep.Failed != "b" {
			t.Fatalf("Failed = %q, want b", rep.Failed)
		}
		if len(d.applied) != 2 || d.applied[0] != "a" || d.applied[1] != "b" {
			t.Fatalf("apply attempted = %v, want [a b]", d.applied)
		}
		// The rollback tears down only what was applied, in reverse: a.
		if len(d.tornDown) != 1 || d.tornDown[0] != "a" {
			t.Fatalf("rollback teardown = %v, want [a]", d.tornDown)
		}
		if len(rep.RolledBack) != 1 || rep.RolledBack[0] != "a" {
			t.Fatalf("RolledBack = %v, want [a]", rep.RolledBack)
		}
	})

	t.Run("failure on the first resource rolls back nothing", func(t *testing.T) {
		d := &scriptedDriver{typ: "state-path", apply: true, teardown: true,
			applyErr: map[string]error{"a": errScripted("apply of a fails")}}
		rep := NewRegistry(d).ApplyAll(s, values, env)
		if rep.Failed != "a" {
			t.Fatalf("Failed = %q, want a", rep.Failed)
		}
		if len(d.tornDown) != 0 {
			t.Fatalf("nothing was applied, so nothing may be torn down: %v", d.tornDown)
		}
	})
}

// TestTeardownAllReverseOrderContinuingPastFailure is exit criterion 6: the
// reverse-order teardown continues past a failure and reports everything
// that survived.
func TestTeardownAllReverseOrderContinuingPastFailure(t *testing.T) {
	home := t.TempDir()
	s := chainSpec(t, home)
	values := chainValues(t, s, home)
	env := Env{Spec: s, Home: home, Worktree: home + "/wt-1"}

	d := &scriptedDriver{typ: "state-path", apply: true, teardown: true,
		teardownErr: map[string]error{"b": errScripted("b is stuck")}}
	rep := NewRegistry(d).TeardownAll(s, values, env, nil, nil)

	// Reverse order: c, b (fails), a — and the failure does not stop a.
	if len(d.tornDown) != 3 || d.tornDown[0] != "c" || d.tornDown[1] != "b" || d.tornDown[2] != "a" {
		t.Fatalf("teardown order = %v, want [c b a]", d.tornDown)
	}
	if rep.Clean() {
		t.Fatal("the report must not be clean while b survived")
	}
	if len(rep.Survivors) != 1 || rep.Survivors[0].Name != "b" {
		t.Fatalf("survivors = %+v, want b", rep.Survivors)
	}
	if !strings.Contains(rep.Summary(), "b is stuck") {
		t.Errorf("the summary must name what survived and why: %q", rep.Summary())
	}
}

// TestTeardownAllRefusalAndUnavailable: a refused teardown and an
// unavailable one both block freeing the slot, and the report keeps them
// distinct from survivors.
func TestTeardownAllRefusalAndUnavailable(t *testing.T) {
	home := t.TempDir()
	s := chainSpec(t, home)
	values := chainValues(t, s, home)
	env := Env{Spec: s, Home: home, Worktree: home + "/wt-1"}

	d := &scriptedDriver{typ: "state-path", apply: true, teardown: true,
		teardownErr: map[string]error{
			"c": &RefusalError{Reason: "refusing to purge /x: it is the shared source"},
			"b": &ErrUnavailable{Reason: "the docker daemon is unreachable"},
		}}
	rep := NewRegistry(d).TeardownAll(s, values, env, nil, nil)

	if rep.Clean() {
		t.Fatal("the report must not be clean")
	}
	if len(rep.Refusals) != 1 || !strings.Contains(rep.Refusals[0].Reason, "shared source") {
		t.Fatalf("refusals = %+v, want the purge refusal", rep.Refusals)
	}
	if len(rep.Unavailable) != 1 || !strings.Contains(rep.Unavailable[0], "unreachable") {
		t.Fatalf("unavailable = %v, want the daemon reason", rep.Unavailable)
	}
	// a still went down: teardown continues past both.
	if len(d.tornDown) != 3 {
		t.Fatalf("teardown must continue past refusal and unavailability: %v", d.tornDown)
	}
}

// TestTeardownAllRegistryHandleWithoutSpecRow: a recorded value whose spec
// row is gone survives — the slot stays held (failing closed) instead of
// freeing it with the objects still out there.
func TestTeardownAllRegistryHandleWithoutSpecRow(t *testing.T) {
	home := t.TempDir()
	s := chainSpec(t, home)
	values := chainValues(t, s, home)
	values["old_resource"] = spec.Resolved{Type: "state-path", Value: home + "/old"}
	env := Env{Spec: s, Home: home, Worktree: home + "/wt-1"}

	d := &scriptedDriver{typ: "state-path", apply: true, teardown: true}
	rep := NewRegistry(d).TeardownAll(s, values, env, nil, nil)
	if rep.Clean() {
		t.Fatal("a recorded handle with no spec row must keep the slot held")
	}
	if len(rep.Survivors) != 1 || rep.Survivors[0].Name != "old_resource" {
		t.Fatalf("survivors = %+v, want the spec-less handle", rep.Survivors)
	}
}

type scriptedErr string

func (e scriptedErr) Error() string { return string(e) }

func errScripted(msg string) error { return scriptedErr(msg) }
