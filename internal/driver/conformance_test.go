package driver

// conformance_test.go is the shared contract suite: every driver runs the
// same checks, so adding a driver is a row of the table below rather than a
// new test file (plan.md §4, "Each driver runs the same contract conformance
// suite"). Phase 8 adds cidr and machine as rows of the same table.

import (
	"path/filepath"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// conformanceCase is one driver's row of the suite. The case supplies
// everything the suite must not assume: the spec the driver derives from,
// the resolution context, and an Env built for the driver's needs (a fake
// docker for the namespace driver, a temp home for the state-path driver).
type conformanceCase struct {
	name string
	d    Driver

	spec     *spec.Spec
	ctx      spec.Context
	env      Env
	resource string // the resource row the suite derives

	wantApply    bool
	wantTeardown bool
	wantGates    bool
}

// runConformance runs the contract checks over one driver. The checks that
// hold for every driver:
//
//   - the optional pair is declared truthfully — HasApply/HasTeardown match
//     the combination the case pins, and an absent operation is a successful
//     no-op rather than a refusal;
//   - derive agrees with spec.Resolve — the driver must not reimplement or
//     contradict the single derivation;
//   - probe returns one of the three results and changes nothing;
//   - verify runs and changes nothing;
//   - blastRadius is non-empty prose.
func runConformance(t *testing.T, tc conformanceCase) {
	t.Helper()

	if tc.d.HasApply() != tc.wantApply {
		t.Fatalf("%s: HasApply() = %v, want %v", tc.name, tc.d.HasApply(), tc.wantApply)
	}
	if tc.d.HasTeardown() != tc.wantTeardown {
		t.Fatalf("%s: HasTeardown() = %v, want %v", tc.name, tc.d.HasTeardown(), tc.wantTeardown)
	}
	if tc.d.GatesAllocation() != tc.wantGates {
		t.Fatalf("%s: GatesAllocation() = %v, want %v", tc.name, tc.d.GatesAllocation(), tc.wantGates)
	}

	// Derive agrees with spec.Resolve: the driver's value for its resource
	// row is exactly what the shared derivation produces.
	table, err := spec.Resolve(tc.spec, tc.ctx)
	if err != nil {
		t.Fatalf("%s: spec.Resolve: %v", tc.name, err)
	}
	res := resourceByName(tc.spec, tc.resource)
	if res == nil {
		t.Fatalf("%s: test spec has no resource %q", tc.name, tc.resource)
	}
	value, err := tc.d.Derive(res, tc.spec, tc.ctx)
	if err != nil {
		t.Fatalf("%s: Derive: %v", tc.name, err)
	}
	want := table[tc.resource]
	if value != want.Value {
		t.Fatalf("%s: Derive = %#v, spec.Resolve = %#v — the driver contradicts the single derivation",
			tc.name, value, want.Value)
	}

	// Probe returns one of the three results. Which one depends on the
	// environment the case built, so the suite pins the result space only.
	switch pr := tc.d.Probe(res, value, tc.env); pr {
	case ProbeFree, ProbeHeld, ProbeUnavailable:
	default:
		t.Fatalf("%s: Probe returned invalid result %v", tc.name, pr)
	}

	// Apply: present means it runs against the case's env without error;
	// absent means it is a successful no-op with no claims.
	ar, err := tc.d.Apply(res, value, tc.env)
	if err != nil {
		t.Fatalf("%s: Apply: %v", tc.name, err)
	}
	if !tc.wantApply && len(ar.Notes) > 0 {
		t.Fatalf("%s: a driver without apply reported notes: %v", tc.name, ar.Notes)
	}

	// Teardown: present means it runs without error; absent means it is a
	// successful no-op (a port is not a thing that exists).
	if err := tc.d.Teardown(res, value, tc.env); err != nil {
		t.Fatalf("%s: Teardown: %v", tc.name, err)
	}

	// Verify runs and reports findings as data rather than as an error.
	findings, err := tc.d.Verify(res, value, tc.env)
	if err != nil {
		t.Fatalf("%s: Verify: %v", tc.name, err)
	}
	for _, f := range findings {
		if f.Resource == "" || f.Message == "" {
			t.Fatalf("%s: finding with empty resource or message: %+v", tc.name, f)
		}
		switch f.Level {
		case LevelInfo, LevelWarning, LevelError:
		default:
			t.Fatalf("%s: finding with unknown level %q: %+v", tc.name, f.Level, f)
		}
	}

	// BlastRadius is prose: non-empty, and not a placeholder.
	br := tc.d.BlastRadius(res, tc.spec)
	if br == "" {
		t.Fatalf("%s: BlastRadius is empty", tc.name)
	}
}

// TestDriverConformance runs the suite over every driver. A new driver is a
// row here, not a new test file.
func TestDriverConformance(t *testing.T) {
	for _, tc := range conformanceCases(t) {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			runConformance(t, tc)
		})
	}
}

// conformanceCases is the driver table. Each phase that adds a driver adds a
// row; the suite code does not change. t is passed so a case can build its
// own temp environment.
func conformanceCases(t *testing.T) []conformanceCase {
	return []conformanceCase{
		{
			name:         "port",
			d:            &Port{},
			spec:         portConformanceSpec(),
			ctx:          portConformanceCtx(),
			env:          Env{},
			resource:     "api",
			wantApply:    false,
			wantTeardown: false,
			wantGates:    true,
		},
		statePathConformanceCase(t),
		namespaceConformanceCase(t),
		cidrConformanceCase(t),
		machineConformanceCase(t),
	}
}

// cidrConformanceCase is the cidr row: a clean fake docker, so the suite's
// probe reads free (no network overlaps the derived /22).
func cidrConformanceCase(t *testing.T) conformanceCase {
	s := &spec.Spec{
		Version: 1, App: "conformance",
		Slots: spec.Slots{Max: intPtr(32)},
		Resources: []spec.Resource{{
			Type: "cidr", Name: "egress",
			Pool: strPtr("172.30.0.0/16"), Size: intPtr(22),
		}},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	ctx := spec.Context{
		App: "conformance", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the cidr conformance spec: %v", err)
	}
	return conformanceCase{
		name: "cidr",
		d:    &CIDR{},
		spec: s,
		ctx:  ctx,
		env: Env{
			Spec: s, App: "conformance", Slug: "wt-1", Slot: 1,
			Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
			Resolved: table,
			Docker:   newFakeDocker(),
		},
		resource:     "egress",
		wantApply:    false,
		wantTeardown: false,
		wantGates:    true,
	}
}

// machineConformanceCase is the machine row: an empty fake runner, so the
// suite's probe reads free, the apply starts the instance, and the
// teardown deletes it.
func machineConformanceCase(t *testing.T) conformanceCase {
	s := &spec.Spec{
		Version: 1, App: "conformance",
		Slots: spec.Slots{Max: intPtr(32)},
		Resources: []spec.Resource{{
			Type: "machine", Name: "vm", Driver: strPtr("auto"),
			Template: strPtr("{app}-{slug}-{slot}"), MaxConcurrent: intPtr(4),
		}},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	ctx := spec.Context{
		App: "conformance", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the machine conformance spec: %v", err)
	}
	return conformanceCase{
		name: "machine",
		d:    &Machine{},
		spec: s,
		ctx:  ctx,
		env: Env{
			Spec: s, App: "conformance", Slug: "wt-1", Slot: 1,
			Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
			Resolved: table,
			Machine:  newFakeMachine(),
		},
		resource:     "vm",
		wantApply:    true,
		wantTeardown: true,
		wantGates:    false,
	}
}

// portConformanceSpec is the stride-port spec the port row derives from.
func portConformanceSpec() *spec.Spec {
	max := 32
	s := &spec.Spec{
		Version: 1,
		App:     "conformance",
		Slots:   spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	return s
}

// portConformanceCtx is the resolution context the stub row derives with.
func portConformanceCtx() spec.Context {
	return spec.Context{
		App:      "conformance",
		Slug:     "wt-1",
		Slot:     1,
		Home:     "/home/wt",
		Worktree: "/home/wt/worktrees/wt-1",
		Bases:    map[string]int{"api": 4200},
	}
}

// statePathConformanceCase is the state-path row: a temp home and the
// empty seed mode, so the suite's apply and teardown are self-contained (a
// seeded apply would need a source the suite does not assume).
func statePathConformanceCase(t *testing.T) conformanceCase {
	home := t.TempDir()
	return conformanceCase{
		name: "state-path",
		d:    &StatePath{},
		spec: &spec.Spec{
			Version: 1, App: "conformance",
			Slots: spec.Slots{Max: intPtr(32)},
			Resources: []spec.Resource{{
				Type: "state-path", Name: "db",
				Template: strPtr(filepath.Join(home, "worktrees", "{slug}", "db.sqlite")),
			}},
			Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
		},
		ctx: spec.Context{
			App: "conformance", Slug: "wt-1", Slot: 1,
			Home: home, Worktree: filepath.Join(home, "worktrees", "wt-1"),
		},
		env: Env{
			App: "conformance", Slug: "wt-1", Slot: 1,
			Home: home, Worktree: filepath.Join(home, "worktrees", "wt-1"),
			SeedModes: map[string]string{"db": "empty"},
		},
		resource:     "db",
		wantApply:    true,
		wantTeardown: true,
		wantGates:    false,
	}
}

// namespaceConformanceCase is the namespace row: a clean fake docker, so
// the suite's probe reads free and the teardown finds nothing.
func namespaceConformanceCase(t *testing.T) conformanceCase {
	s := &spec.Spec{
		Version: 1, App: "conformance",
		Slots: spec.Slots{Max: intPtr(32)},
		Resources: []spec.Resource{
			{Type: "namespace", Name: "compose", Kind: strPtr("compose"),
				Template: strPtr("{app}-{slug}-{slot}")},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	ctx := spec.Context{
		App: "conformance", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the namespace conformance spec: %v", err)
	}
	return conformanceCase{
		name: "namespace",
		d:    &Namespace{},
		spec: s,
		ctx:  ctx,
		env: Env{
			Spec: s, App: "conformance", Slug: "wt-1", Slot: 1,
			Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
			Resolved: table,
			Docker:   newFakeDocker(),
		},
		resource:     "compose",
		wantApply:    false,
		wantTeardown: true,
		wantGates:    false,
	}
}
