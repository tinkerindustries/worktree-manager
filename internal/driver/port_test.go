package driver

// port_test.go exercises the port driver: derivation in both forms, the
// loopback bind probe (free, held, unavailable), verify, and the stride
// ceiling the driver must not contradict.

import (
	"net"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// TestPortDeriveForms pins the two derivations of ARCHITECTURE.md §8.4:
// stride is base + slot, group is base + slot×size + offset. Both come from
// spec.Resolve — the driver calls it rather than deriving a second time.
func TestPortDeriveForms(t *testing.T) {
	cases := []struct {
		name     string
		res      spec.Resource
		base     int
		slot     int
		want     int
		validate bool
	}{
		{"stride", spec.Resource{Type: "port", Name: "api"}, 4200, 7, 4207, true},
		{"stride slot 1", spec.Resource{Type: "port", Name: "api"}, 4200, 1, 4201, true},
		{"group", spec.Resource{Type: "port", Name: "api", Form: strPtr("group"), Size: intPtr(2), Offset: intPtr(1)}, 4200, 3, 4207, true},
		{"group offset 0", spec.Resource{Type: "port", Name: "proxy", Form: strPtr("group"), Size: intPtr(2), Offset: intPtr(0)}, 4200, 3, 4206, true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			max := 32
			s := &spec.Spec{
				Version:   1,
				App:       "conformance",
				Slots:     spec.Slots{Max: &max},
				Resources: []spec.Resource{tc.res},
				Emit:      spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
			}
			ctx := spec.Context{
				App: "conformance", Slug: "wt-1", Slot: tc.slot,
				Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-1",
				Bases: map[string]int{tc.res.Name: tc.base},
			}
			v, err := (&Port{}).Derive(&s.Resources[0], s, ctx)
			if err != nil {
				t.Fatalf("Derive: %v", err)
			}
			if v != tc.want {
				t.Errorf("Derive = %v, want %d", v, tc.want)
			}
		})
	}
}

// TestPortProbeFreeAndHeld runs the real bind probe against a listener in
// this process: held while it is open, free after it closes. The same
// process holds the port, so there is no race with a stranger.
func TestPortProbeFreeAndHeld(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	defer l.Close()

	if got := (&Port{}).Probe(nil, port, Env{}); got != ProbeHeld {
		t.Errorf("Probe while held = %v, want held", got)
	}
	if err := l.Close(); err != nil {
		t.Fatalf("closing the listener: %v", err)
	}
	if got := (&Port{}).Probe(nil, port, Env{}); got != ProbeFree {
		t.Errorf("Probe after release = %v, want free", got)
	}
}

// TestPortProbeUnavailable: a value that is not an int cannot be bound at
// all, which is unavailable — the probe could not run, and allocation must
// not be blocked by it (plan.md §3).
func TestPortProbeUnavailable(t *testing.T) {
	if got := (&Port{}).Probe(nil, "not-a-port", Env{}); got != ProbeUnavailable {
		t.Errorf("Probe of a non-int value = %v, want unavailable", got)
	}
}

// TestPortVerify reports the port's state without changing anything: a
// warning naming the port when bound, an info line when free.
func TestPortVerify(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("reserving a port: %v", err)
	}
	port := l.Addr().(*net.TCPAddr).Port
	defer l.Close()

	findings, err := (&Port{}).Verify(&spec.Resource{Name: "api"}, port, Env{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if len(findings) != 1 {
		t.Fatalf("Verify bound = %d findings, want 1", len(findings))
	}
	if findings[0].Level != LevelWarning || !strings.Contains(findings[0].Message, "bound") {
		t.Errorf("bound finding = %+v, want a warning naming the bound port", findings[0])
	}

	if err := l.Close(); err != nil {
		t.Fatalf("closing the listener: %v", err)
	}
	findings, err = (&Port{}).Verify(&spec.Resource{Name: "api"}, port, Env{})
	if err != nil {
		t.Fatalf("Verify: %v", err)
	}
	if findings[0].Level != LevelInfo || !strings.Contains(findings[0].Message, "free") {
		t.Errorf("free finding = %+v, want an info line naming the free port", findings[0])
	}
}

// TestPortStrideCeilingNotReimplemented pins the division of labour: the
// stride form's 99-slot ceiling is spec validate's, enforced at validation,
// and the driver neither re-implements it nor contradicts it — deriving the
// highest legal slot works, and a stride spec above the ceiling is refused
// before any driver runs.
func TestPortStrideCeilingNotReimplemented(t *testing.T) {
	max := 99
	s := &spec.Spec{
		Version: 1, App: "conformance",
		Slots: spec.Slots{Max: &max},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("a stride spec at the ceiling must validate: %v", err)
	}
	ctx := spec.Context{
		App: "conformance", Slug: "wt-99", Slot: 99,
		Home: "/home/wt", Worktree: "/home/wt/worktrees/wt-99",
		Bases: map[string]int{"api": 4200},
	}
	if v, err := (&Port{}).Derive(&s.Resources[0], s, ctx); err != nil || v != 4299 {
		t.Errorf("Derive at slot 99 = %v, %v; want 4299", v, err)
	}

	over := 100
	s2 := &spec.Spec{
		Version: 1, App: "conformance",
		Slots: spec.Slots{Max: &over},
		Resources: []spec.Resource{
			{Type: "port", Name: "api"},
		},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.json", Format: "json"}},
	}
	if err := spec.Validate(s2); err == nil {
		t.Error("a stride spec with slots.max 100 must be refused at validation")
	}
}

// TestPortBlastRadiusNamesTheCollision: the shared-block prose explains what
// sharing a port costs, in the requirement's own terms (the second stack to
// bind fails, loudly).
func TestPortBlastRadiusNamesTheCollision(t *testing.T) {
	br := (&Port{}).BlastRadius(&spec.Resource{Name: "api"}, nil)
	if br == "" || !strings.Contains(br, "port") {
		t.Errorf("BlastRadius = %q, want prose naming the port collision", br)
	}
}

func strPtr(s string) *string { return &s }
func intPtr(i int) *int       { return &i }
