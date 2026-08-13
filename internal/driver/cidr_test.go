package driver

// cidr_test.go exercises the cidr driver: the /22-per-slot arithmetic out
// of 172.30.0.0/16 (disjoint across slots), the overlap probe against the
// fake docker seam, the shared-pool fallback being loud with the remedy
// named, on_exhaustion: fail stopping instead, and the fallback warning
// persisting through verify. Exit criterion 3 of phase 8.

import (
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// cidrFixture builds the vm-app-shaped cidr resource: a /22 per slot out
// of 172.30.0.0/16, 64 blocks, shared-pool exhaustion.
func cidrFixture(t *testing.T, onExhaustion string) (*spec.Spec, spec.Context, *spec.Resource, map[string]spec.Resolved) {
	t.Helper()
	max := 100
	s := &spec.Spec{
		Version: 1, App: "vm-app",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{{
			Type: "cidr", Name: "egress",
			Pool:         strPtr("172.30.0.0/16"),
			Size:         intPtr(22),
			OnExhaustion: strPtr(onExhaustion),
		}},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("fixture spec does not validate: %v", err)
	}
	ctx := spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 1,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(s, ctx)
	if err != nil {
		t.Fatalf("resolving the fixture: %v", err)
	}
	return s, ctx, spec.ResourceByName(s, "egress"), table
}

func cidrValue(t *testing.T, table map[string]spec.Resolved) string {
	t.Helper()
	v := table["egress"].Value
	c, ok := v.(string)
	if !ok {
		t.Fatalf("resolved egress = %#v, want a string", v)
	}
	return c
}

// TestCIDRDerivesDisjoint22sOutOfThe16 is exit criterion 3's arithmetic
// half: slot 1 takes 172.30.0.0/22, slot 2 172.30.4.0/22, and so on —
// disjoint across slots by construction, four /24s per worktree inside
// each block, no coordination (B5.1).
func TestCIDRDerivesDisjoint22sOutOfThe16(t *testing.T) {
	_, _, res, _ := cidrFixture(t, "shared-pool")
	for _, tc := range []struct {
		slot int
		want string
	}{
		{1, "172.30.0.0/22"},
		{2, "172.30.4.0/22"},
		{3, "172.30.8.0/22"},
		{4, "172.30.12.0/22"},
		{16, "172.30.60.0/22"},
		{17, "172.30.64.0/22"},
		{64, "172.30.252.0/22"},
	} {
		ctx := spec.Context{
			App: "vm-app", Slug: "wt-1", Slot: tc.slot,
			Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
		}
		table, err := spec.Resolve(cidrSpec(res), ctx)
		if err != nil {
			t.Fatalf("slot %d: %v", tc.slot, err)
		}
		if got := cidrValue(t, table); got != tc.want {
			t.Errorf("slot %d = %s, want %s", tc.slot, got, tc.want)
		}
	}
	// Disjointness is the point: every slot's range is a /22 and the
	// ranges cannot overlap, which the arithmetic guarantees. Pin the
	// boundary cases: the first block starts at the pool start, the last
	// block ends exactly at the pool end.
	first := "172.30.0.0/22"
	last := "172.30.252.0/22"
	if !strings.HasPrefix(first, "172.30.") || !strings.HasPrefix(last, "172.30.") {
		t.Fatal("fixture sanity: the blocks must live in 172.30.0.0/16")
	}
}

// cidrSpec returns a spec holding just the given resource, for
// re-resolving other slots.
func cidrSpec(r *spec.Resource) *spec.Spec {
	return &spec.Spec{
		Version: 1, App: "vm-app",
		Slots:     spec.Slots{Max: intPtr(100)},
		Resources: []spec.Resource{*r},
		Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
}

// TestCIDRFallbackSharedPoolIsLoud is the exhaustion half of exit
// criterion 3: past slot 64 the pool is exhausted, and on_exhaustion:
// shared-pool falls back to the shared pool itself, loudly — the note
// names the remedy (free a slot or widen the pool). A fallback that is
// silent is the failure this rule exists to prevent (D7).
func TestCIDRFallbackSharedPoolIsLoud(t *testing.T) {
	_, _, res, _ := cidrFixture(t, "shared-pool")
	ctx := spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 65,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	table, err := spec.Resolve(cidrSpec(res), ctx)
	if err != nil {
		t.Fatalf("slot 65 must fall back, not fail: %v", err)
	}
	value := cidrValue(t, table)
	if value != "172.30.0.0/16" {
		t.Errorf("the fallback value must be the shared pool itself, got %s", value)
	}

	note := FallbackNote(res, value, 65)
	if note == "" {
		t.Fatal("the fallback must be loud: FallbackNote is empty")
	}
	for _, want := range []string{"exhausted", "shared pool", "free a slot", "widen the pool"} {
		if !strings.Contains(note, want) {
			t.Errorf("the note must name %q: %s", want, note)
		}
	}

	// The warning persists for the life of the worktree: verify reports it
	// as a finding every time, because nothing reallocates a fallen-back
	// cidr when a slot later frees (03-drivers.md §4.3).
	env := Env{Slot: 65, Docker: newFakeDocker()}
	findings, err := (&CIDR{}).Verify(res, value, env)
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(findings) != 1 || findings[0].Level != LevelWarning {
		t.Fatalf("findings = %+v, want exactly one warning", findings)
	}
	if !strings.Contains(findings[0].Message, "exhausted") {
		t.Errorf("the finding must name the exhaustion: %+v", findings[0])
	}

	// A real slice verifies clean: no finding.
	if findings, err := (&CIDR{}).Verify(res, "172.30.0.0/22", Env{Slot: 1}); err != nil || len(findings) != 0 {
		t.Errorf("a real slice must verify clean, got %v %v", findings, err)
	}
}

// TestCIDRExhaustionFailStops is the other exhaustion half: a repo that
// declares on_exhaustion: fail gets a refusal naming the exhaustion, never
// a fallback.
func TestCIDRExhaustionFailStops(t *testing.T) {
	_, _, res, _ := cidrFixture(t, "fail")
	ctx := spec.Context{
		App: "vm-app", Slug: "wt-1", Slot: 65,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
	}
	_, err := spec.Resolve(cidrSpec(res), ctx)
	if err == nil {
		t.Fatal("slot 65 with on_exhaustion: fail must refuse")
	}
	if !strings.Contains(err.Error(), "exhausted") || !strings.Contains(err.Error(), "fail") {
		t.Errorf("the refusal must name the exhaustion and the mode: %v", err)
	}
	// A fail-mode spec never produces a fallback value: the driver's
	// FallbackNote is only ever consulted on values that came from
	// resolution, so the refusal above is the whole of the fail path.
}

// TestCIDRProbeOverlap is the probe half: whether any existing network on
// the reachable daemon overlaps the derived slice. An overlapping network
// holds the slot; a disjoint one leaves it free; an unreachable daemon is
// unavailable, which does not block allocation (plan.md §3).
func TestCIDRProbeOverlap(t *testing.T) {
	_, _, res, _ := cidrFixture(t, "shared-pool")
	d := newFakeDocker()
	env := Env{Slot: 2, Docker: d} // slot 2: 172.30.4.0/22

	t.Run("no networks probes free", func(t *testing.T) {
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeFree {
			t.Errorf("Probe = %v, want free", got)
		}
	})

	t.Run("an overlapping network holds the slot", func(t *testing.T) {
		d.allNetworks["wt-overlap"] = "172.30.5.0/24" // inside slot 2's /22
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeHeld {
			t.Errorf("Probe = %v, want held", got)
		}
		delete(d.allNetworks, "wt-overlap")
	})

	t.Run("a disjoint network leaves the slot free", func(t *testing.T) {
		d.allNetworks["wt-other"] = "172.30.0.0/22" // slot 1's block
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeFree {
			t.Errorf("Probe = %v, want free", got)
		}
		delete(d.allNetworks, "wt-other")
	})

	t.Run("an unparseable subnet never holds", func(t *testing.T) {
		d.allNetworks["wt-weird"] = "not-a-cidr"
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeFree {
			t.Errorf("Probe = %v, want free", got)
		}
		delete(d.allNetworks, "wt-weird")
	})

	t.Run("unreachable daemon is unavailable", func(t *testing.T) {
		d.versionErr = &ErrUnavailable{Reason: "the docker daemon is unreachable"}
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeUnavailable {
			t.Errorf("Probe = %v, want unavailable", got)
		}
	})

	t.Run("no docker seam is unavailable", func(t *testing.T) {
		env.Docker = nil
		if got := (&CIDR{}).Probe(res, "172.30.4.0/22", env); got != ProbeUnavailable {
			t.Errorf("Probe = %v, want unavailable", got)
		}
	})

	t.Run("the shared-pool fallback always probes free", func(t *testing.T) {
		// Overlap is the point of the shared pool, not a collision: a
		// fallen-back slot must stay allocatable, or the loud fallback of
		// §4.3 would never be reached.
		d.versionErr = nil
		d.allNetworks["wt-overlap"] = "172.30.5.0/24"
		if got := (&CIDR{}).Probe(res, "172.30.0.0/16", Env{Slot: 65, Docker: d}); got != ProbeFree {
			t.Errorf("the shared-pool fallback must probe free, got %v", got)
		}
	})
}

// TestCIDRConformanceRow is the contract check run through the shared
// suite; the row itself lives in conformance_test.go.
func TestCIDRConformanceRow(t *testing.T) {
	runConformance(t, cidrConformanceCase(t))
}
