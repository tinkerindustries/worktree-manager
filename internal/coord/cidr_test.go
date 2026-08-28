package coord

// cidr_test.go exercises the phase-8 cidr machinery through the
// coordinator: the loud shared-pool fallback carried on the allocate
// result (03-drivers.md §4.3 — a fallback that is silent is the failure
// the rule exists to prevent), and the probe holding a slot whose derived
// slice overlaps an existing network.

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"

	"github.com/mrgeoffrich/worktree-manager/internal/api"
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// cidrAllocSpec is a small pool whose exhaustion is cheap to reach: two
// /30 blocks in a /29. Slot 1 takes 192.168.0.0/30, slot 2
// 192.168.0.4/30, slot 3 falls back to the shared pool 192.168.0.0/29.
func cidrAllocSpec(t *testing.T) *spec.Spec {
	t.Helper()
	s := &spec.Spec{
		Version: 1, App: "cidr-app",
		Slots: spec.Slots{Max: intPtr(8)},
		Resources: []spec.Resource{{
			Type: "cidr", Name: "egress",
			Pool: strPtr("192.168.0.0/29"), Size: intPtr(30),
		}},
		Emit: spec.Emit{Descriptor: spec.Descriptor{Filename: "wt-env.yaml", Format: "yaml"}},
	}
	if err := spec.Validate(s); err != nil {
		t.Fatalf("cidr spec does not validate: %v", err)
	}
	return s
}

// allocateEntry allocates one entry for the app and returns its slot and
// the response.
func allocateCIDR(t *testing.T, h *Harness, sess *Session, sp *spec.Spec, slug string) (int, api.AllocateResult) {
	t.Helper()
	resp := h.Request(context.Background(), sess, verbAllocate, &api.AllocateArgs{
		Spec: *sp, Slug: slug, Path: filepath.Join(t.TempDir(), slug),
		Description: "a cidr worktree",
	})
	if resp.Error != nil {
		t.Fatalf("allocating %s: %v", slug, resp.Error)
	}
	var res api.AllocateResult
	mustUnmarshal(t, resp.Result, &res)
	return res.Slot, res
}

// TestAllocateCIDRFallbackIsLoud is exit criterion 3's allocation half:
// past the pool's last block the shared-pool fallback allocates — the
// response carries the warning naming the remedy, so the fallback is never
// silent.
func TestAllocateCIDRFallbackIsLoud(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.Docker = &noNetworksDocker{} // the subject is the allocator, not the probe
	h.H.InstallDrivers(driver.NewRegistry(&driver.CIDR{}))
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	sp := cidrAllocSpec(t)

	slot1, res1 := allocateCIDR(t, h, sess, sp, "wt-one")
	if slot1 != 1 {
		t.Fatalf("slot 1 = %d", slot1)
	}
	if got := res1.Resources["egress"].Value; got != "192.168.0.0/30" {
		t.Errorf("slot 1 egress = %v, want 192.168.0.0/30", got)
	}
	if len(res1.Notes) != 0 {
		t.Errorf("a real slice must carry no fallback note, got %v", res1.Notes)
	}

	allocateCIDR(t, h, sess, sp, "wt-two") // slot 2: 192.168.0.4/30

	slot3, res3 := allocateCIDR(t, h, sess, sp, "wt-three")
	if slot3 != 3 {
		t.Fatalf("slot 3 = %d", slot3)
	}
	if got := res3.Resources["egress"].Value; got != "192.168.0.0/29" {
		t.Errorf("the fallback value must be the shared pool, got %v", got)
	}
	if len(res3.Notes) != 1 {
		t.Fatalf("the fallback must be loud: notes = %v", res3.Notes)
	}
	for _, want := range []string{"egress", "exhausted", "shared pool", "free a slot", "widen the pool"} {
		if !strings.Contains(res3.Notes[0], want) {
			t.Errorf("the note must name %q: %s", want, res3.Notes[0])
		}
	}

	// The warning persists for the life of the worktree: re-running init
	// (the existing-entry path) reports it again, because nothing
	// reallocates a fallen-back cidr when a slot later frees.
	_, again := allocateCIDR(t, h, sess, sp, "wt-three")
	if len(again.Notes) != 1 || !strings.Contains(again.Notes[0], "exhausted") {
		t.Errorf("a re-run must repeat the fallback warning, got %v", again.Notes)
	}
}

// TestAllocateCIDRProbeHoldsOverlappingSlot is the probe's allocation
// half: a derived slice that overlaps an existing network holds the slot,
// so the allocator skips it and names the skip (B1.6's rule applied to
// subnets).
func TestAllocateCIDRProbeHoldsOverlappingSlot(t *testing.T) {
	h := NewHarness(t, filepath.Join(tempRoot(t), "wt"))
	h.H.Docker = &overlapDocker{subnet: "192.168.0.0/30"} // slot 1's slice is taken
	h.H.InstallDrivers(driver.NewRegistry(&driver.CIDR{}))
	sess, err := h.Connect(api.KindHost, "")
	if err != nil {
		t.Fatalf("hello: %v", err)
	}
	sp := cidrAllocSpec(t)

	slot, res := allocateCIDR(t, h, sess, sp, "wt-one")
	if slot != 2 {
		t.Fatalf("slot 1 overlaps an existing network; the allocator must skip it, got slot %d", slot)
	}
	if got := res.Resources["egress"].Value; got != "192.168.0.4/30" {
		t.Errorf("slot 2 egress = %v, want 192.168.0.4/30", got)
	}
	if len(res.Skipped) != 1 || !strings.Contains(res.Skipped[0], "slot 1 skipped") {
		t.Errorf("the skip must be named: %v", res.Skipped)
	}
}

// overlapDocker is the minimal docker seam for the cidr probe: one
// existing network with a fixed subnet.
type overlapDocker struct {
	subnet string
}

func (d *overlapDocker) Version() error { return nil }
func (d *overlapDocker) ListContainers(string) ([]string, error) {
	return nil, nil
}
func (d *overlapDocker) ListNetworks(string) ([]string, error)     { return nil, nil }
func (d *overlapDocker) ListVolumes(string) ([]string, error)      { return nil, nil }
func (d *overlapDocker) ListNetworksAll() ([]string, error)        { return []string{"wt-held"}, nil }
func (d *overlapDocker) NetworkSubnet(string) (string, error)      { return d.subnet, nil }
func (d *overlapDocker) RemoveContainers([]string) error           { return nil }
func (d *overlapDocker) RemoveNetworks([]string) error             { return nil }
func (d *overlapDocker) RemoveVolumes([]string) error              { return nil }
func (d *overlapDocker) NetworkEndpoints(string) ([]string, error) { return nil, nil }
func (d *overlapDocker) DisconnectContainer(string, string) error  { return nil }
func (d *overlapDocker) WithHost(string) driver.Docker             { return d }

// noNetworksDocker is the docker seam for a test whose subject is the
// allocator rather than the probe: a reachable daemon holding no network,
// so no derived slice overlaps anything.
//
// Without it the probe asks the machine's own daemon, and the answer
// depends on whatever networks that daemon happens to hold — a Windows
// runner's default nat network sits inside 192.168.0.0/16 and takes the
// first two slots with it.
type noNetworksDocker struct{ overlapDocker }

func (d *noNetworksDocker) ListNetworksAll() ([]string, error) { return nil, nil }

// mustUnmarshal decodes a response result into v, failing the test on a
// decode error.
func mustUnmarshal(t *testing.T, data []byte, v any) {
	t.Helper()
	if err := json.Unmarshal(data, v); err != nil {
		t.Fatalf("decoding the response: %v", err)
	}
}

// intPtr builds the pointer form the spec's optional fields take.
func intPtr(i int) *int { return &i }
