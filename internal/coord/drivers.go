package coord

import (
	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// ProbeFromRegistry builds the allocation probe from the driver registry: a
// candidate slot's resources are probed per resource driver, and a driver
// that gates allocation (port, and cidr from phase 8) makes a Held result
// skip the slot. Unavailable does not block allocation (plan.md §3), and a
// namespace hit means a previous teardown was incomplete, not that the slot
// is taken (03-drivers.md §4.2) — which is why the namespace driver does not
// gate and the probe never consults it.
func ProbeFromRegistry(reg driver.Registry) Probe {
	return func(s *spec.Spec, slot int, resources map[string]spec.Resolved) ProbeResult {
		held, unavailable := false, false
		for i := range s.Resources {
			r := &s.Resources[i]
			res, ok := resources[r.Name]
			if !ok {
				continue
			}
			d := reg.Driver(r.Type)
			if d == nil || !d.GatesAllocation() {
				continue
			}
			switch d.Probe(r, res.Value, driver.Env{}) {
			case driver.ProbeHeld:
				held = true
			case driver.ProbeUnavailable:
				unavailable = true
			}
		}
		if held {
			return ProbeHeld
		}
		if unavailable {
			return ProbeUnavailable
		}
		return ProbeFree
	}
}

// InstallDrivers wires the driver registry into the handler: the allocation
// probe and the teardown path. cmd/wtd calls it at startup with the
// phase-4 registry; a test calls it with fakes to drive the seam.
func (h *Handler) InstallDrivers(reg driver.Registry) {
	h.Drivers = reg
	h.Probe = ProbeFromRegistry(reg)
}
