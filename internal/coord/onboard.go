package coord

// onboard.go is the phase-7 coordinator surface: the two primitives the
// onboarding skill reads that earlier phases did not build — ports.scan
// (what is listening on this machine right now) and bands.suggest (where
// an app's port band could sit). Neither binary performs inference: the
// scan reports listeners as facts, the suggestion proposes ranges that
// fit, and whether a listener belongs to a production stack — or a
// proposed base is the one the developer wants — is the skill's judgement
// (plan.md §3, §8 R6; 09-onboarding.md §2).
//
// ports.scan runs in the coordinator because listener discovery needs the
// host network namespace (08-platform.md §3): a client inside a container
// would see the container's namespaces, and the report says so.
// bands.suggest reads the ledger and computes the required size from the
// spec, exactly as bands.reserve does — the skill chooses only where the
// bases sit, not how large they are (02-coordination.md §6.2).

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// verbPortsScan is the wire name of the ports.scan verb.
const verbPortsScan = "ports.scan"

// verbBandsSuggest is the wire name of the bands.suggest verb.
const verbBandsSuggest = "bands.suggest"

// portsScan implements the ports.scan verb: every LISTEN TCP socket in the
// coordinator's network namespace, sorted by port. It reports facts and
// never reserves anything — the onboarding skill's phase-1 audit reads it
// to see the listeners the developer has not declared (09-onboarding.md
// §3). A scan that cannot see every listener says so, with the remedy
// naming the missing discovery tool (plan.md §3: bounded coverage is
// stated, never silent).
func (h *Handler) portsScan(s *Session, req *protocol.Request) *protocol.Response {
	holders, err := platform.AllListeners()
	if err != nil {
		return respErr(4, err.Error(),
			"install the discovery tool the message names, then re-run: wt ports scan")
	}
	out := protocol.PortsScanResult{}
	unknown := 0
	for _, hld := range holders {
		out.Listeners = append(out.Listeners, protocol.PortsScanEntry{
			Port: hld.Port, PID: hld.PID, Command: hld.Command,
		})
		if hld.Port == 0 {
			unknown++
		}
	}
	if unknown > 0 {
		out.Notes = append(out.Notes, fmt.Sprintf(
			"%d listener(s) could not report their port (the discovery tool's output carries none); each is listed with the port as 0 — the pid and command are still facts", unknown))
	}
	if platform.InContainer() {
		// The coordinator runs where it runs; inside a container its scan
		// sees the container's namespaces, which is a different machine
		// from the host the bands describe (08-platform.md §3). Stated,
		// never silent.
		out.Notes = append(out.Notes,
			"this coordinator runs inside a container: the scan sees this container's network namespace only — run wtd on the host for the machine's listeners")
	}
	return &protocol.Response{Result: mustJSON(out)}
}

// suggestBand implements the bands.suggest verb: given the spec, propose
// one base per port resource — the lowest base whose required range fits —
// against the current ledger. The required size is the coordinator's
// computation (slot ceiling × ports per slot, the same bandSpan bands.reserve
// registers), so the skill chooses only where the bases go, not how large
// they are.
//
// The suggestion is constrained, never classifying: a base is acceptable
// when its range [base, base+span-1] overlaps no other app's band range
// and no host-global reservation port, and when the resources of this app
// do not collide with each other — two resources sharing a base are
// acceptable exactly when their derived port sets are disjoint (the group
// form derives interleaved ports from one base), and resources on
// different bases must keep disjoint ranges. The first acceptable base per
// resource, in spec order, wins; a resource with no acceptable base
// anywhere in the port space is reported with the bound stated, never
// silently skipped.
func (h *Handler) suggestBand(s *Session, req *protocol.Request) *protocol.Response {
	var args protocol.SuggestBandArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed bands.suggest request: %v", err), "upgrade wt: this coordinator expects a spec")
	}
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the suggestion is refused whole: %v", err),
			"fix the spec, then re-run: wt bands suggest")
	}

	h.mu.Lock()
	defer h.mu.Unlock()
	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}

	spans := bandSpan(&args.Spec)
	max := slotMax(&args.Spec)
	// The ranges no suggestion may touch: every registered band's range
	// (any app, this one included — re-registration replaces in place, and
	// a suggestion that collides with the band being replaced is not a
	// suggestion), and every host-global reserved port.
	var busy []spanRange
	for _, b := range bands.Bands {
		for name, base := range b.Bases {
			size := b.Spans[name]
			if size < 1 {
				size = spans[name]
			}
			busy = append(busy, spanRange{lo: base, hi: base + size - 1})
		}
	}
	for _, r := range bands.Reservations {
		for _, p := range r.Ports {
			busy = append(busy, spanRange{lo: p, hi: p})
		}
	}

	out := protocol.SuggestBandResult{App: args.Spec.App}
	chosen := map[string]spanRange{} // resource name → proposed band range
	for i := range args.Spec.Resources {
		r := &args.Spec.Resources[i]
		if r.Type != "port" {
			continue
		}
		span := spans[r.Name]
		base, ok := suggestBase(r, max, span, busy, &args.Spec, chosen)
		if !ok {
			out.Notes = append(out.Notes, fmt.Sprintf(
				"no base fits resource %q anywhere in 1..%d: every range of %d consecutive ports overlaps an existing band or a host-global reservation — free a range (host reservations are re-judged by a person), then re-run",
				r.Name, 65535-span+1, span))
			continue
		}
		chosen[r.Name] = spanRange{lo: base, hi: base + span - 1}
		out.Suggestions = append(out.Suggestions, protocol.BandSuggestion{
			Resource: r.Name, Base: base, Span: span,
			Low: base, High: base + span - 1,
		})
	}
	sort.Strings(out.Notes)
	return &protocol.Response{Result: mustJSON(out)}
}

// spanRange is one busy range of the port space.
type spanRange struct {
	lo, hi int
}

// suggestBase finds the lowest acceptable base for one port resource. A
// base is acceptable when its band range [base, base+span-1] overlaps no
// busy range, and the resource does not collide with an already-chosen
// resource of the same app: sharing a base is allowed exactly when the two
// resources' derived port sets are disjoint (the group form derives
// interleaved ports from one base), and different bases must keep disjoint
// ranges.
func suggestBase(r *spec.Resource, max, span int, busy []spanRange, s *spec.Spec, chosen map[string]spanRange) (int, bool) {
	size, offset := portGeometry(r)
	for base := 1; base+span-1 <= 65535; base++ {
		cand := spanRange{lo: base, hi: base + span - 1}
		if overlapsAny(cand, busy) {
			continue
		}
		if !fitsWithChosen(cand, base, size, offset, max, s, chosen) {
			continue
		}
		return base, true
	}
	return 0, false
}

// overlapsAny reports whether the candidate range touches any busy range.
func overlapsAny(cand spanRange, busy []spanRange) bool {
	for _, b := range busy {
		if cand.hi < b.lo || b.hi < cand.lo {
			continue
		}
		return true
	}
	return false
}

// fitsWithChosen reports whether the candidate base can coexist with the
// bases already suggested for this app's earlier resources: a shared base
// requires disjoint derived port sets, a different base requires disjoint
// band ranges.
func fitsWithChosen(cand spanRange, base, size, offset, max int, s *spec.Spec, chosen map[string]spanRange) bool {
	for name, other := range chosen {
		if other.lo == base {
			// Same base: acceptable only when the two resources' port sets
			// never coincide — the group form. Two stride resources on one
			// base derive identical ports for every slot, which would
			// collide at allocation.
			otherSize, otherOffset := geometryOf(s, name)
			if !disjointPortSets(base, size, offset, max, otherSize, otherOffset, max) {
				return false
			}
			continue
		}
		if cand.lo <= other.hi && other.lo <= cand.hi {
			// Two independent bases must keep their ranges disjoint, like
			// two apps' bands must.
			return false
		}
	}
	return true
}

// portGeometry is a resource's derivation geometry: stride is size 1
// offset 0.
func portGeometry(r *spec.Resource) (size, offset int) {
	size, offset = 1, 0
	if r.Form != nil && *r.Form == "group" {
		if r.Size != nil {
			size = *r.Size
		}
		if r.Offset != nil {
			offset = *r.Offset
		}
	}
	return size, offset
}

// geometryOf looks up a chosen resource's geometry from the spec.
func geometryOf(s *spec.Spec, name string) (int, int) {
	for i := range s.Resources {
		if s.Resources[i].Name == name {
			return portGeometry(&s.Resources[i])
		}
	}
	return 1, 0
}

// disjointPortSets reports whether two resources deriving from the same
// base ever derive the same port for some slot.
func disjointPortSets(base, sizeA, offsetA, maxA, sizeB, offsetB, maxB int) bool {
	setB := map[int]bool{}
	for slot := 1; slot <= maxB; slot++ {
		setB[base+(slot-1)*sizeB+offsetB] = true
	}
	for slot := 1; slot <= maxA; slot++ {
		p := base + (slot-1)*sizeA + offsetA
		if setB[p] {
			return false
		}
	}
	return true
}
