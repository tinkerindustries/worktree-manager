package coord

import (
	"encoding/json"
	"fmt"
	"sort"

	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// reserveBand implements the bands.reserve verb: explicit registration of
// an app's port band, or of a host-global reservation. Registration is
// explicit because grabbing a free range on the fly would make the ledger's
// contents depend on the order in which repos happened to be used
// (02-coordination.md §6.2). A host reservation requires the note naming
// what holds the range — an unlabelled reservation is one nobody can later
// judge (plan.md §8, R6).
func (h *Handler) reserveBand(s *Session, req *protocol.Request) *protocol.Response {
	var args protocol.ReserveBandArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed bands.reserve request: %v", err), "upgrade wt: this coordinator expects bases or host ports and a note")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}

	if args.Host {
		return h.reserveHost(s, &args, &bands)
	}
	return h.reserveApp(s, &args, &bands)
}

// reserveApp registers one app's band. The coordinator computes the
// required size from the spec — services (ports per slot) times the slot
// ceiling — so the onboarding skill only chooses where the bases sit, not
// how large they are. One app, one band: re-registration replaces in place,
// which is how a re-run of onboarding fixes a bad band.
func (h *Handler) reserveApp(s *Session, args *protocol.ReserveBandArgs, bands *store.BandsFile) *protocol.Response {
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the registration is refused whole: %v", err),
			"fix the spec, then re-run: wt bands reserve")
	}
	app := args.Spec.App
	spans := bandSpan(&args.Spec)

	if len(spans) == 0 {
		return respErr(3, fmt.Sprintf("app %q declares no port resources; there is no band to register", app),
			"the ledger holds port bands; an app without ports needs none")
	}
	// Every base must name a port resource and every port resource must have
	// a base — completeness against the spec, checked coordinator-side.
	for name := range args.Bases {
		if _, ok := spans[name]; !ok {
			return respErr(3, fmt.Sprintf("base %q names no port resource of app %q", name, app),
				"pass one --base per port resource, e.g. --base api=4200")
		}
	}
	for name := range spans {
		if _, ok := args.Bases[name]; !ok {
			return respErr(3, fmt.Sprintf("no base supplied for port resource %q of app %q", name, app),
				fmt.Sprintf("pass a base for every port resource: --base %s=<port>", name))
		}
	}
	// The band's span must stay inside the port space: base plus the
	// highest slot's derivation. Caught at registration so the skill hears
	// about it while choosing bases, not at the first allocation.
	max := slotMax(&args.Spec)
	for name, base := range args.Bases {
		if base < 1 || base > 65535 {
			return respErr(3, fmt.Sprintf("base %d for port resource %q is not a port number (1..65535)", base, name),
				"give a base in 1..65535, e.g. --base api=4200")
		}
		top := topPort(portResource(&args.Spec, name), base, max)
		if top > 65535 {
			return respErr(3, fmt.Sprintf("base %d for port resource %q leaves the band over the port space: "+
				"the top slot derives port %d, above 65535", base, name, top),
				fmt.Sprintf("lower the base so %s + (slots.max-1)×size + offset stays at or below 65535", name))
		}
	}

	band := findBand(*bands, app)
	if band == nil {
		bands.Bands = append(bands.Bands, store.Band{App: app, Bases: args.Bases})
	} else {
		band.Bases = args.Bases // one app, one band: re-registration replaces
	}
	if err := h.st.WriteBands(*bands); err != nil {
		return h.storeErr("writing the band ledger", err)
	}
	return &protocol.Response{Result: mustJSON(protocol.ReserveBandResult{
		App: app, Bases: args.Bases, Spans: spans,
	})}
}

// reserveHost writes a host-global reservation: ports no app may allocate
// from, with the required note. Production stacks and anything else the
// machine runs are a property of the machine, so they live in the ledger,
// not in any repo's spec (ARCHITECTURE.md §8.4).
func (h *Handler) reserveHost(s *Session, args *protocol.ReserveBandArgs, bands *store.BandsFile) *protocol.Response {
	if args.Note == "" {
		return respErr(3, "a host reservation must carry a note naming what holds the range",
			"re-run with --note, e.g. wt bands reserve --host --port 5319 --port 5320 --note \"compose-app production stack\"")
	}
	if len(args.Ports) == 0 {
		return respErr(3, "a host reservation must name at least one port",
			"pass the ports, e.g. wt bands reserve --host --port 5319 --note \"...\"")
	}
	seen := make(map[int]bool, len(args.Ports))
	ports := make([]int, 0, len(args.Ports))
	for _, p := range args.Ports {
		if p < 1 || p > 65535 {
			return respErr(3, fmt.Sprintf("reserved port %d is not a port number (1..65535)", p),
				"give port numbers in 1..65535")
		}
		if seen[p] {
			continue
		}
		seen[p] = true
		ports = append(ports, p)
	}
	sort.Ints(ports)
	bands.Reservations = append(bands.Reservations, store.Reservation{Ports: ports, Note: args.Note})
	if err := h.st.WriteBands(*bands); err != nil {
		return h.storeErr("writing the band ledger", err)
	}
	return &protocol.Response{Result: mustJSON(protocol.ReserveBandResult{
		Host: true, Ports: ports, Note: args.Note,
	})}
}

// listBands implements the bands.list verb: the whole ledger, apps sorted
// by name and reservations by their lowest port.
func (h *Handler) listBands(s *Session, req *protocol.Request) *protocol.Response {
	h.mu.Lock()
	defer h.mu.Unlock()
	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}
	out := protocol.BandsListResult{
		Bands:        make([]protocol.BandInfo, 0, len(bands.Bands)),
		Reservations: make([]protocol.ReservationInfo, 0, len(bands.Reservations)),
	}
	apps := make([]string, 0, len(bands.Bands))
	for _, b := range bands.Bands {
		apps = append(apps, b.App)
	}
	sort.Strings(apps)
	for _, app := range apps {
		b := findBand(bands, app)
		out.Bands = append(out.Bands, protocol.BandInfo{App: app, Bases: b.Bases})
	}
	resv := make([]store.Reservation, len(bands.Reservations))
	copy(resv, bands.Reservations)
	sort.Slice(resv, func(i, j int) bool { return resv[i].Ports[0] < resv[j].Ports[0] })
	for _, r := range resv {
		out.Reservations = append(out.Reservations, protocol.ReservationInfo{Ports: r.Ports, Note: r.Note})
	}
	return &protocol.Response{Result: mustJSON(out)}
}

// bandSpan computes, per port resource, the number of consecutive ports one
// base must cover: the slot ceiling times the ports one slot consumes (size
// for a group, 1 for stride). This is the required size the coordinator
// derives from the spec — services times the slot ceiling
// (02-coordination.md §6.2).
func bandSpan(s *spec.Spec) map[string]int {
	max := slotMax(s)
	spans := make(map[string]int)
	for i := range s.Resources {
		r := &s.Resources[i]
		if r.Type != "port" {
			continue
		}
		spans[r.Name] = max * portsPerSlot(r)
	}
	return spans
}

// portsPerSlot is how many ports one slot consumes of a resource's base:
// the group size, or 1 for stride.
func portsPerSlot(r *spec.Resource) int {
	if r.Form != nil && *r.Form == "group" && r.Size != nil {
		return *r.Size
	}
	return 1
}

// topPort is the highest port a base ever derives: base + (max-1)×size +
// offset, the stride form being size 1 offset 0.
func topPort(r *spec.Resource, base, max int) int {
	size, offset := 1, 0
	if r.Form != nil && *r.Form == "group" {
		if r.Size != nil {
			size = *r.Size
		}
		if r.Offset != nil {
			offset = *r.Offset
		}
	}
	return base + (max-1)*size + offset
}

// slotMax applies the spec's slot ceiling with the default-32 fallback
// (M2 §5.3). The spec package's own helper is unexported; this is the same
// rule against the exported fields.
func slotMax(s *spec.Spec) int {
	if s.Slots.Max != nil && *s.Slots.Max >= 1 {
		return *s.Slots.Max
	}
	return spec.DefaultSlotMax
}

// portResource returns one port resource by name.
func portResource(s *spec.Spec, name string) *spec.Resource {
	for i := range s.Resources {
		if s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}
