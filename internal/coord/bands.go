package coord

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/api"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
	"github.com/tinkerindustries/worktree-manager/internal/store"
)

// reserveBand implements the bands.reserve verb: explicit registration of
// an app's port band, or of a host-global reservation. Registration is
// explicit because grabbing a free range on the fly would make the ledger's
// contents depend on the order in which repos happened to be used
// (02-coordination.md §6.2). A host reservation requires the note naming
// what holds the range — an unlabelled reservation is one nobody can later
// judge (plan.md §8, R6).
//
// The verb is host-client-only: it changes machine-global policy, and the
// grant a container client receives is "the ability to create entries and
// mutate what it created" (ARCHITECTURE.md §12.2) — the ledger is neither.
// The security pass (phase 9) makes that boundary real: an ephemeral or
// named container cannot move the machine's port space or declare a
// co-resident production stack.
func (h *Handler) reserveBand(s *Session, req *api.Request) *api.Response {
	var args api.ReserveBandArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed bands.reserve request: %v", err), "upgrade wt: this coordinator expects bases or host ports and a note")
	}
	if s.Identity.Kind != api.KindHost {
		return &api.Response{Error: &api.Error{
			Code:   3,
			Msg:    fmt.Sprintf("bands.reserve changes machine-global policy (the port ledger) and only a host client may do that; this connection is a %s client, whose grant is limited to creating entries and mutating what it created (ARCHITECTURE.md §12.2)", s.Identity.Kind),
			Remedy: "run 'wt bands reserve' on the host, as the owning user",
		}}
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	if args.Host {
		return h.reserveHost(s, &args)
	}
	return h.reserveApp(s, &args)
}

// reserveApp registers one app's band. The coordinator computes the
// required size from the spec — services (ports per slot) times the slot
// ceiling — so the onboarding skill only chooses where the bases sit, not
// how large they are. One app, one band: re-registration replaces in place,
// which is how a re-run of onboarding fixes a bad band.
func (h *Handler) reserveApp(s *Session, args *api.ReserveBandArgs) *api.Response {
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

	// One app, one band: re-registration replaces in place, which is how a
	// re-run of onboarding fixes a bad band. The upsert is the whole
	// replacement — the store drops the app's old rows and inserts the new
	// ones in one transaction.
	if err := h.st.UpsertBand(store.Band{App: app, Bases: args.Bases, Spans: spans}); err != nil {
		return h.storeErr("writing the band ledger", err)
	}
	return &api.Response{Result: mustJSON(api.ReserveBandResult{
		App: app, Bases: args.Bases, Spans: spans,
	})}
}

// reserveHost writes a host-global reservation: ports no app may allocate
// from and compose project names no teardown may reach, with the required
// note. Production stacks and anything else the machine runs are a property
// of the machine, so they live in the ledger, not in any repo's spec
// (ARCHITECTURE.md §8.4). The reserved names are the phase-4 rail behind
// the namespace driver's teardown refusal (03-drivers.md §4.2, B8.2): a
// person declares the co-resident stack's compose project name once per
// machine, and label-based teardown is refused when a resolved name matches
// it.
func (h *Handler) reserveHost(s *Session, args *api.ReserveBandArgs) *api.Response {
	if args.Note == "" {
		return respErr(3, "a host reservation must carry a note naming what holds the range",
			"re-run with --note, e.g. wt bands reserve --host --port 5319 --port 5320 --name compose-app-prod --note \"compose-app production stack\"")
	}
	if len(args.Ports) == 0 && len(args.Names) == 0 {
		return respErr(3, "a host reservation must name at least one port or one compose project name",
			"pass the ports (--port) or the project names (--name), e.g. wt bands reserve --host --name compose-app-prod --note \"...\"")
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
	seenNames := make(map[string]bool, len(args.Names))
	names := make([]string, 0, len(args.Names))
	for _, n := range args.Names {
		if n == "" {
			return respErr(3, "a reserved compose project name must not be empty",
				"give the project name, e.g. --name compose-app-prod")
		}
		if strings.ContainsAny(n, " \t\n") {
			return respErr(3, fmt.Sprintf("reserved compose project name %q contains whitespace", n),
				"give the exact project name, e.g. --name compose-app-prod")
		}
		if len(n) > spec.NamespaceMaxLen {
			return respErr(3, fmt.Sprintf("reserved compose project name %q is %d characters; cap is %d", n, len(n), spec.NamespaceMaxLen),
				"give a shorter project name")
		}
		if !seenNames[n] {
			seenNames[n] = true
			names = append(names, n)
		}
	}
	sort.Strings(names)
	if err := h.st.AddReservation(store.Reservation{Ports: ports, Names: names, Note: args.Note}); err != nil {
		return h.storeErr("writing the band ledger", err)
	}
	return &api.Response{Result: mustJSON(api.ReserveBandResult{
		Host: true, Ports: ports, Names: names, Note: args.Note,
	})}
}

// listBands implements the bands.list verb: the whole ledger, apps sorted
// by name and reservations by their lowest port.
func (h *Handler) listBands(s *Session, req *api.Request) *api.Response {
	h.mu.Lock()
	defer h.mu.Unlock()
	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}
	out := api.BandsListResult{
		Bands:        make([]api.BandInfo, 0, len(bands.Bands)),
		Reservations: make([]api.ReservationInfo, 0, len(bands.Reservations)),
	}
	apps := make([]string, 0, len(bands.Bands))
	for _, b := range bands.Bands {
		apps = append(apps, b.App)
	}
	sort.Strings(apps)
	for _, app := range apps {
		b := findBand(bands, app)
		out.Bands = append(out.Bands, api.BandInfo{App: app, Bases: b.Bases})
	}
	resv := make([]store.Reservation, len(bands.Reservations))
	copy(resv, bands.Reservations)
	// Sort by the lowest port when the reservation has ports, else by its
	// first reserved name — a name-only reservation must not panic the sort.
	sort.Slice(resv, func(i, j int) bool {
		pi, pj := firstPort(resv[i]), firstPort(resv[j])
		if pi != pj {
			return pi < pj
		}
		return firstReservedName(resv[i]) < firstReservedName(resv[j])
	})
	for _, r := range resv {
		out.Reservations = append(out.Reservations, api.ReservationInfo{Ports: r.Ports, Names: r.Names, Note: r.Note})
	}
	return &api.Response{Result: mustJSON(out)}
}

// firstPort is a reservation's lowest port, or a value that sorts name-only
// reservations apart from ported ones.
func firstPort(r store.Reservation) int {
	if len(r.Ports) == 0 {
		return 1 << 30
	}
	return r.Ports[0]
}

// firstReservedName is a reservation's first reserved name, or "".
func firstReservedName(r store.Reservation) string {
	if len(r.Names) == 0 {
		return ""
	}
	return r.Names[0]
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
