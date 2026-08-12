package coord

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/mrgeoffrich/worktree-manager/internal/driver"
	"github.com/mrgeoffrich/worktree-manager/internal/protocol"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Verb names, shared between the dispatch and the tests.
const (
	verbAllocate     = "allocate"
	verbActivate     = "activate"
	verbRelease      = "release"
	verbBandsReserve = "bands.reserve"
	verbBandsList    = "bands.list"
)

// ProbeResult is the outcome of probing one candidate slot's derived
// resources. Held skips the slot; Unavailable does not block allocation —
// the plan's rule is "an unavailable probe does not block allocation"
// (plan.md §3), and a slot whose resources probe held is skipped and named.
type ProbeResult int

const (
	ProbeFree ProbeResult = iota
	ProbeHeld
	ProbeUnavailable
)

// Probe is the seam the phase-4 port driver plugs into: "skip anything
// probing held", with the probe itself phase 4's work. This phase ships the
// no-probe probe — every slot probes free — explicitly, rather than
// pretending a probe ran. InstallDrivers replaces the Handler's Probe with
// the driver-backed probe at coordinator startup; a test can install a fake.
// The spec is passed so the probe can consult each resource's driver with
// the resource itself (the namespace driver, for one, needs the kind).
type Probe func(s *spec.Spec, slot int, resources map[string]spec.Resolved) ProbeResult

// noProbe is phase 3's probe: nothing is held. The real probe (the phase-4
// port driver's) reports from the host network namespace; until then the
// no-probe case is explicit in the allocation path.
func noProbe(*spec.Spec, int, map[string]spec.Resolved) ProbeResult { return ProbeFree }

// ReservingTimeout is how long a reserving entry may sit before the
// coordinator ages it out on its own timer (ARCHITECTURE.md §11.2). It must
// cover materialisation — the machine driver takes minutes — and it also
// covers a client that died mid-sequence, which is the point of the state.
const ReservingTimeout = 10 * time.Minute

// allocate implements the allocate verb: validate the spec, find the band,
// take the lowest free slot from 1 upward, resolve the resources, commit
// the entry as reserving owned by this client.
//
// An existing entry for this (app, slug) is authoritative (ARCHITECTURE.md
// §8.6 rule 5): re-running init reconciles and rebuilds, it never
// reallocates, so the stored slot and resources are returned as they are —
// unless the entry belongs to another client, which is a refused mutating
// call.
func (h *Handler) allocate(s *Session, req *protocol.Request) *protocol.Response {
	var args protocol.AllocateArgs
	if err := json.Unmarshal(req.Args, &args); err != nil {
		return respErr(1, fmt.Sprintf("malformed allocate request: %v", err), "upgrade wt: this coordinator expects a spec, slug and path")
	}
	if err := spec.Validate(&args.Spec); err != nil {
		return respErr(3, fmt.Sprintf("the spec sent with the allocation is refused whole: %v", err),
			"fix the spec, then re-run the allocation")
	}
	if !spec.ValidSlug(args.Slug) {
		return respErr(3, fmt.Sprintf("slug %q is not valid (must match ^[a-z0-9][a-z0-9-]*$, at most 32 characters)", args.Slug),
			"give a kebab-case slug of at most 32 characters")
	}
	if args.Path == "" {
		return respErr(3, "the worktree path is required", "run from inside the worktree, or pass the worktree path")
	}
	if n := len(strings.Fields(args.Description)); n > 10 {
		return respErr(3, fmt.Sprintf("the description is %d words; the field is for ten words or fewer", n),
			"give a description of ten words or fewer, then re-run")
	}

	h.mu.Lock()
	defer h.mu.Unlock()

	// Record the spec for reclamation: a dead ephemeral client's entry has
	// no visible path for the walk-up lookup, and the cached spec is what
	// its teardown computes dependent projects and purge refusals from.
	// Best-effort — a cache failure is logged, never a refusal.
	h.cacheSpec(args.Spec.App, &args.Spec)

	reg, err := h.st.ReadRegistry()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	bands, err := h.st.ReadBands()
	if err != nil {
		return h.storeErr("reading the band ledger", err)
	}

	// The slot namespace is keyed on the spec's app field, never the repo
	// path — a standalone clone has a different path and must still share
	// slot space with the checkout it was cloned from (02-coordination.md
	// §5.1).
	app := args.Spec.App

	if e := registryEntry(reg, app, args.Slug); e != nil {
		if err := h.checkOwner(s, e); err != nil {
			return &protocol.Response{Error: err}
		}
		// Rule 5: the entry's slot is authoritative once written. Re-running
		// init reconciles and rebuilds; it never reallocates. The stored
		// resources are denormalised, so the caller needs no spec to use
		// them.
		shared, serr := h.sharedRows(&args.Spec, ctxFor(&args.Spec, e), e.Resources)
		if serr != nil {
			return &protocol.Response{Error: serr}
		}
		// The cidr fallback warning persists for the life of the worktree
		// (nothing reallocates a fallen-back cidr), so a re-run reports it
		// again, loudly.
		return &protocol.Response{Result: mustJSON(protocol.AllocateResult{
			App: app, Slug: e.Slug, Slot: e.Slot, State: e.State,
			Resources: e.Resources, PathVisible: e.PathVisible, Secrets: e.Secrets,
			Path: e.Path, Existed: true, Shared: shared,
			Notes: cidrFallbackNotes(&args.Spec, e.Resources, e.Slot),
		})}
	}

	// Registration is explicit: an app with port resources and no registered
	// bases is refused, naming the registration command — grabbing a free
	// range on the fly would make the ledger's contents depend on the order
	// in which repos happened to be used (02-coordination.md §6.2). An app
	// with no port resources has no bases to register and needs no band.
	bases := map[string]int{}
	if hasPortResources(&args.Spec) {
		band := findBand(bands, app)
		if band == nil {
			return &protocol.Response{Error: &protocol.Error{
				Code: 3,
				Msg:  fmt.Sprintf("app %q has no registered port band; allocation is refused", app),
				Remedy: fmt.Sprintf("register the band from inside the repository: wt bands reserve --base <name>=<port>... " +
					"(one base per port resource, e.g. --base api=4200)"),
			}}
		}
		bases = band.Bases
	}

	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return respErr(4, fmt.Sprintf("the coordinator cannot determine the home directory for {home}: %v", err),
			"set $HOME for the coordinator, then re-run")
	}

	// The rebuild-from-descriptor path (ARCHITECTURE.md §9.1, attach
	// outcome 4): a descriptor exists and no entry does — the registry was
	// deleted, corrupted, or is another machine's — and the client sent the
	// descriptor's slot. The descriptor beats the registry (rule 1), so the
	// entry is rebuilt at that slot; the slot must be in range, must not be
	// held by another entry, and its derived resources must not fall in
	// either exclusion set — a hand-edited manifest is still refused, never
	// honoured (ARCHITECTURE.md §8.4). The probe is not run: this is a
	// rebuild of a recorded allocation, not a fresh allocation, and the
	// recorded slot is the allocation.
	var slot int
	var resources map[string]spec.Resolved
	var skipped []string
	probeNote := ""
	if args.SlotHint > 0 {
		max := spec.DefaultSlotMax
		if args.Spec.Slots.Max != nil && *args.Spec.Slots.Max >= 1 {
			max = *args.Spec.Slots.Max
		}
		if args.SlotHint > max {
			return respErr(3, fmt.Sprintf("the descriptor's slot %d is above this app's ceiling of %d", args.SlotHint, max),
				"re-run init after fixing the descriptor (or deleting it to reallocate)")
		}
		if holder := registrySlotEntry(reg, app, args.SlotHint); holder != nil {
			return respErr(3, fmt.Sprintf("the descriptor's slot %d is held by entry %q at %s; the descriptor cannot claim a slot another entry holds",
				args.SlotHint, holder.Slug, holder.Path),
				"delete or fix the descriptor, then re-run init")
		}
		res, rerr := spec.Resolve(&args.Spec, spec.Context{
			App: app, Slug: args.Slug, Slot: args.SlotHint,
			Home: home, Worktree: args.Path, Bases: bases,
		})
		if rerr != nil {
			return respErr(3, fmt.Sprintf("resolving the descriptor's slot %d: %v", args.SlotHint, rerr),
				"check the spec and the band registration, then re-run")
		}
		reservedSet := make(map[int]bool, len(args.Spec.Reserved.Ports))
		for _, p := range args.Spec.Reserved.Ports {
			reservedSet[p] = true
		}
		resvSet := make(map[int]bool)
		for _, r := range bands.Reservations {
			for _, p := range r.Ports {
				resvSet[p] = true
			}
		}
		if excluded(res, reservedSet, resvSet) {
			return respErr(3, fmt.Sprintf("the descriptor's slot %d resolves to a port in the spec's reserved block or the host-global reservations; a hand-edited manifest is refused, never honoured",
				args.SlotHint),
				"delete or fix the descriptor, then re-run init to reallocate")
		}
		slot, resources = args.SlotHint, res
	} else {
		picked, perr := h.pickSlot(&args.Spec, reg, bands, app, args.Slug, args.Path, home, bases,
			s.Identity.Key, s.Identity.Kind)
		if perr != nil {
			return &protocol.Response{Error: perr}
		}
		slot, resources = picked.slot, picked.resources
		skipped, probeNote = picked.skipped, picked.probeNote
	}

	now := time.Now().UTC().Format(time.RFC3339Nano)
	entry := store.Entry{
		App: app, Slug: args.Slug, Slot: slot,
		Owner: s.Identity.Key, OwnerKind: s.Identity.Kind, Ephemeral: s.Identity.Ephemeral,
		Path: args.Path, PathVisible: pathVisible(args.Path),
		State:          store.StateReserving,
		Resources:      resources,
		DescriptorPath: args.DescriptorPath,
		Description:    args.Description,
		Secrets:        args.Secrets,
		CreatedAt:      now, LastSeen: now,
	}
	shared, serr := h.sharedRows(&args.Spec, spec.Context{
		App: app, Slug: entry.Slug, Slot: entry.Slot,
		Home: home, Worktree: entry.Path, Bases: bases,
	}, entry.Resources)
	if serr != nil {
		return &protocol.Response{Error: serr}
	}

	reg.Entries = append(reg.Entries, entry)
	if err := h.st.WriteRegistry(reg); err != nil {
		return h.storeErr("writing the registry", err)
	}

	return &protocol.Response{Result: mustJSON(protocol.AllocateResult{
		App: app, Slug: entry.Slug, Slot: entry.Slot, State: entry.State,
		Resources: entry.Resources, PathVisible: entry.PathVisible, Secrets: entry.Secrets,
		ProbeNote: probeNote, Skipped: skipped, Shared: shared,
		Notes: cidrFallbackNotes(&args.Spec, entry.Resources, entry.Slot),
	})}
}

// cidrFallbackNotes returns the loud shared-pool fallback warning for
// every cidr resource whose recorded value fell back — the allocation
// path's half of the 03-drivers.md §4.3 rule that a fallback is never
// silent. The driver's Verify reports the same warning for the life of the
// worktree, because nothing reallocates a fallen-back cidr when a slot
// later frees.
func cidrFallbackNotes(sp *spec.Spec, resources map[string]spec.Resolved, slot int) []string {
	var notes []string
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Type != "cidr" {
			continue
		}
		v, ok := resources[r.Name]
		if !ok {
			continue
		}
		value, ok := v.Value.(string)
		if !ok {
			continue
		}
		if note := driver.FallbackNote(r, value, slot); note != "" {
			notes = append(notes, fmt.Sprintf("%s: %s", r.Name, note))
		}
	}
	return notes
}

// ctxFor builds the template context for one existing entry: the entry's
// own slot, slug and path, and the coordinator's home.
func ctxFor(sp *spec.Spec, e *store.Entry) spec.Context {
	return spec.Context{
		App: sp.App, Slug: e.Slug, Slot: e.Slot,
		Home: homeDirOrEmpty(), Worktree: e.Path, Bases: nil,
	}
}

// homeDirOrEmpty is the coordinator's home, or "" where it cannot be
// determined — the shared block's {home} resolves to the empty string in
// that case rather than failing the whole allocation.
func homeDirOrEmpty() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// sharedRows computes the descriptor's shared block, both halves
// (03-drivers.md §6): the generated half — every resource with
// default: shared, with its driver's blast-radius prose as the impact —
// and the hand-authored half from the spec. The coordinator is the only
// side that imports the drivers, so the rows are computed here and carried
// to the client's emitter, which copies them into the descriptor.
func (h *Handler) sharedRows(sp *spec.Spec, ctx spec.Context, resolved map[string]spec.Resolved) ([]protocol.SharedRow, *protocol.Error) {
	var rows []protocol.SharedRow
	for i := range sp.Resources {
		r := &sp.Resources[i]
		if r.Default == nil || *r.Default != "shared" {
			continue
		}
		v, ok := resolved[r.Name]
		if !ok {
			return nil, &protocol.Error{Code: 3,
				Msg:    fmt.Sprintf("shared block: resource %q has default: shared but no resolved value", r.Name),
				Remedy: "fix the spec, then re-run"}
		}
		text := ""
		if d := h.Drivers.Driver(r.Type); d != nil {
			text = d.BlastRadius(r, sp)
		}
		if strings.TrimSpace(text) == "" {
			return nil, &protocol.Error{Code: 3,
				Msg:    fmt.Sprintf("shared block: resource %q has default: shared but no impact text is available", r.Name),
				Remedy: "check the driver registry, then re-run"}
		}
		rows = append(rows, protocol.SharedRow{Name: fmt.Sprint(v.Value), Impact: text})
	}
	for i := range sp.Shared {
		name, err := spec.Substitute(sp.Shared[i].Name, ctx, resolved)
		if err != nil {
			return nil, &protocol.Error{Code: 3,
				Msg:    fmt.Sprintf("shared block: resolving shared[%d].name: %v", i, err),
				Remedy: "fix the spec, then re-run"}
		}
		rows = append(rows, protocol.SharedRow{Name: name, Impact: sp.Shared[i].Impact})
	}
	return rows, nil
}

// pickedSlot is the allocation algorithm's answer: the chosen slot and its
// resources, plus the bounded-coverage statements the output must carry —
// which slots were skipped and why (a held port is never remediated, the
// allocator says which slot it skipped, B1.6), and whether the chosen
// slot's probe was unavailable (the registry was the only check).
type pickedSlot struct {
	slot      int
	resources map[string]spec.Resolved
	skipped   []string
	probeNote string
}

// pickSlot runs the allocation algorithm: the lowest free value from 1
// upward that is not held by an entry for this app, not excluded by the
// spec's reserved block or the ledger's host-global reservations, and whose
// derived resources do not probe held. Slot 0 is the primary checkout:
// never allocated, never managed (ARCHITECTURE.md §8.4). ownerKey and
// ownerKind are the calling client's identity, which the exhaustion message
// needs to count the slots the caller cannot free.
func (h *Handler) pickSlot(s *spec.Spec, reg store.RegistryFile, bands store.BandsFile, app, slug, path, home string, bases map[string]int, ownerKey, ownerKind string) (*pickedSlot, *protocol.Error) {
	max := spec.DefaultSlotMax
	if s.Slots.Max != nil && *s.Slots.Max >= 1 {
		max = *s.Slots.Max
	}
	reservedSet := make(map[int]bool, len(s.Reserved.Ports))
	for _, p := range s.Reserved.Ports {
		reservedSet[p] = true
	}
	resvSet := make(map[int]bool)
	for _, r := range bands.Reservations {
		for _, p := range r.Ports {
			resvSet[p] = true
		}
	}
	probe := h.Probe
	if probe == nil {
		probe = noProbe
	}
	// Skipped collects the slots the probe held and why (B1.6): the output
	// says which slot it skipped so the reason is never silent.
	var skipped []string

	for slot := 1; slot <= max; slot++ {
		if registrySlotHeld(reg, app, slot) {
			continue
		}
		resources, err := spec.Resolve(s, spec.Context{
			App: app, Slug: slug, Slot: slot,
			Home: home, Worktree: path, Bases: bases,
		})
		if err != nil {
			return nil, &protocol.Error{
				Code:   3,
				Msg:    fmt.Sprintf("resolving slot %d: %v", slot, err),
				Remedy: "check the spec and the band registration, then re-run",
			}
		}
		if excluded(resources, reservedSet, resvSet) {
			continue
		}
		probeNote := ""
		switch probe(s, slot, resources) {
		case ProbeHeld:
			// A held port is never remediated (B1.6): whatever holds it is
			// probably the developer's own running instance, and killing it
			// is out of the tool's hands. Skip the slot and say which one —
			// the output names it so the reason is never silent.
			skipped = append(skipped, fmt.Sprintf(
				"slot %d skipped: a derived port is bound by something outside the registry, and a held port is never remediated", slot))
			continue
		case ProbeUnavailable:
			// Unavailable does not block allocation (plan.md §3), and the
			// output states that the registry was the only check performed.
			probeNote = "the port probe was unavailable; the registry was the only check performed"
		case ProbeFree:
		}
		return &pickedSlot{slot: slot, resources: resources, skipped: skipped, probeNote: probeNote}, nil
	}

	// Exhaustion is actionable: the message names the range, the cleanup
	// command, and how many of the occupied slots the caller cannot free —
	// otherwise the remedy it names would appear to do nothing
	// (02-coordination.md §5.3). The occupied count separates the two
	// exhaustion shapes: slots held by entries (cleanup frees the caller's
	// own) versus slots whose every port is excluded by the spec's reserved
	// block or the host-global reservations (no entry holds them, and no
	// cleanup would help).
	occupied, foreign := 0, 0
	for slot := 1; slot <= max; slot++ {
		if e := registrySlotEntry(reg, app, slot); e != nil {
			occupied++
			if e.Owner != ownerKey || e.OwnerKind != ownerKind {
				foreign++
			}
		}
	}
	if occupied == 0 {
		return nil, &protocol.Error{
			Code: 3,
			Msg: fmt.Sprintf("app %q has no free slot in 1..%d: every slot's derived ports fall in the spec's "+
				"reserved block or the host-global reservations, so no slot is allocatable", app, max),
			Remedy: "move the band bases or narrow the exclusions (the spec's reserved block, or 'wt bands reserve --host'), then re-run",
		}
	}
	return nil, &protocol.Error{
		Code: 3,
		Msg: fmt.Sprintf("app %q has no free slot in 1..%d: all %d slots are occupied, "+
			"and %d of them are owned by other clients and cannot be freed from here",
			app, max, occupied, foreign),
		Remedy: "run 'wt cleanup' to reclaim your own slots; the foreign-owned ones are released only by their owners",
	}
}

// excluded reports whether any derived port of the resource table falls in
// either exclusion set: the spec's reserved block (a property of the
// repository) or the ledger's host-global reservations (a property of the
// machine). Both are enforced at allocation (ARCHITECTURE.md §8.4), and a
// hand-edited manifest is caught again in phase 4's verify.
func excluded(resources map[string]spec.Resolved, specReserved, ledgerReserved map[int]bool) bool {
	for _, r := range resources {
		if r.Type != "port" {
			continue
		}
		p, ok := r.Value.(int)
		if !ok {
			continue
		}
		if specReserved[p] || ledgerReserved[p] {
			return true
		}
	}
	return false
}

// pathVisible records whether the coordinator can stat the path the
// creating client saw. The coordinator runs on the host, so it can stat a
// host path directly; a path that exists only inside a container is
// recorded, never checked, and never counted as stale (ARCHITECTURE.md
// §8.3).
func pathVisible(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// checkOwner runs the authorisation check on a mutating call: an entry
// owned by another client is refused, naming the owner and its last-seen
// time — the coordinator acts with host privilege on a caller's behalf, so
// ownership is the boundary that replaces filesystem permissions
// (ARCHITECTURE.md §4.3, §8.6 rule 6). Last-seen is the coordinator's own
// measurement from clients.json, never a timestamp a client wrote.
func (h *Handler) checkOwner(s *Session, e *store.Entry) *protocol.Error {
	if e.Owner == s.Identity.Key && e.OwnerKind == s.Identity.Kind {
		return nil
	}
	lastSeen := "never recorded"
	if f, err := h.st.ReadClients(); err == nil {
		for _, c := range f.Clients {
			if c.Kind == e.OwnerKind && c.Identity == e.Owner {
				lastSeen = c.LastSeen
				break
			}
		}
	}
	return &protocol.Error{
		Code: 3,
		Msg: fmt.Sprintf("entry %q is owned by %s client %s, last seen %s; "+
			"only the owning client may mutate it (ARCHITECTURE.md §4.3)",
			e.Slug, e.OwnerKind, e.Owner, lastSeen),
		Remedy: fmt.Sprintf("run the operation as the owning %s client %s, or ask its owner to run it",
			e.OwnerKind, e.Owner),
	}
}

// activate implements the activate verb: flip a reserving entry to active.
// A mutating call against an entry owned by another client is refused.
func (h *Handler) activate(s *Session, req *protocol.Request) *protocol.Response {
	var ref protocol.EntryRef
	if err := json.Unmarshal(req.Args, &ref); err != nil {
		return respErr(1, fmt.Sprintf("malformed activate request: %v", err), "upgrade wt: this coordinator expects an app and slug")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	reg, err := h.st.ReadRegistry()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	e := registryEntry(reg, ref.App, ref.Slug)
	if e == nil {
		return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", ref.App, ref.Slug),
			"allocate the worktree first, then re-run")
	}
	if perr := h.checkOwner(s, e); perr != nil {
		return &protocol.Response{Error: perr}
	}
	e.State = store.StateActive
	e.LastSeen = time.Now().UTC().Format(time.RFC3339Nano)
	if err := h.st.WriteRegistry(reg); err != nil {
		return h.storeErr("writing the registry", err)
	}
	return &protocol.Response{Result: mustJSON(protocol.ActivateResult{App: e.App, Slug: e.Slug, State: e.State})}
}

// release implements the release verb: drop the entry entirely — the
// rollback path a client drives when init fails before activation, and the
// deallocate step phase 5's rm sequences after teardown. Owned entries only.
func (h *Handler) release(s *Session, req *protocol.Request) *protocol.Response {
	var ref protocol.EntryRef
	if err := json.Unmarshal(req.Args, &ref); err != nil {
		return respErr(1, fmt.Sprintf("malformed release request: %v", err), "upgrade wt: this coordinator expects an app and slug")
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	reg, err := h.st.ReadRegistry()
	if err != nil {
		return h.storeErr("reading the registry", err)
	}
	idx := -1
	for i := range reg.Entries {
		if reg.Entries[i].App == ref.App && reg.Entries[i].Slug == ref.Slug {
			idx = i
			break
		}
	}
	if idx == -1 {
		return respErr(1, fmt.Sprintf("no registry entry for app %q slug %q", ref.App, ref.Slug),
			"allocate the worktree first, then re-run")
	}
	if perr := h.checkOwner(s, &reg.Entries[idx]); perr != nil {
		return &protocol.Response{Error: perr}
	}
	reg.Entries = append(reg.Entries[:idx], reg.Entries[idx+1:]...)
	if err := h.st.WriteRegistry(reg); err != nil {
		return h.storeErr("writing the registry", err)
	}
	return &protocol.Response{Result: mustJSON(protocol.ReleaseResult{App: ref.App, Slug: ref.Slug, Removed: true})}
}

// AgeReserving is the coordinator's own timer's work: a reserving entry
// older than timeout is aged out — the slot is released, covering a client
// that died mid-sequence (ARCHITECTURE.md §11.2). A resident process needs
// no scheduler for this; the server's sweeper calls it on an interval. It
// returns how many entries were aged out.
func (h *Handler) AgeReserving(now time.Time, timeout time.Duration) (int, error) {
	h.mu.Lock()
	defer h.mu.Unlock()
	reg, err := h.st.ReadRegistry()
	if err != nil {
		return 0, err
	}
	cutoff := now.Add(-timeout)
	kept := reg.Entries[:0]
	aged := 0
	for _, e := range reg.Entries {
		if e.State == store.StateReserving {
			if created, perr := time.Parse(time.RFC3339Nano, e.CreatedAt); perr == nil && created.Before(cutoff) {
				aged++
				continue
			}
		}
		kept = append(kept, e)
	}
	if aged == 0 {
		return 0, nil
	}
	reg.Entries = kept
	return aged, h.st.WriteRegistry(reg)
}

// storeErr turns a store failure into the wire error with the exit code and
// remedy. An unparseable registry is reported, never truncated and
// recreated (02-coordination.md §14).
//
// The 06-fleet.md §5 row "registry unparseable | rebuild from every
// descriptor this view can see" is answered here, in phase 8, and the
// answer is that the rebuild still cannot happen: the registry is the only
// source of repository locations on the machine (doctorRepos states the
// same bound), so a rebuild could only discover the descriptors of the one
// repository the caller happens to stand in. Rebuilding from that one
// repo's worktrees would silently drop every other repo's entries — and
// the rebuild cannot know which entries were this view's, so every
// descriptor-visible worktree would be re-registered under the caller's
// ownership, stealing other clients' slots and resources. A7's "rebuilding
// the registry from descriptors must always be safe" is exactly what that
// would violate, so the refusal stands, naming the restore.
func (h *Handler) storeErr(action string, err error) *protocol.Response {
	var ve *store.VersionError
	if errors.As(err, &ve) {
		return &protocol.Response{Error: &protocol.Error{Code: 3, Msg: fmt.Sprintf("%s: %v", action, err), Remedy: "upgrade wtd, then re-run"}}
	}
	msg := fmt.Sprintf("%s: %v", action, err)
	remedy := "check the coordinator's store (WT_HOME) is readable and writable, then re-run"
	if strings.Contains(err.Error(), "not a readable store file") {
		remedy = "restore the store file from a backup; an unparseable registry is never truncated and recreated — rebuilding it from descriptors is not possible, because the registry is the only source of repository locations and a rebuild from one repo's worktrees would silently drop every other repo's (and every other client's) entries"
	}
	return respErr(1, msg, remedy)
}

// respErr builds the wire error.
func respErr(code int, msg, remedy string) *protocol.Response {
	return &protocol.Response{Error: &protocol.Error{Code: code, Msg: msg, Remedy: remedy}}
}

// mustJSON encodes a result; the payloads here are plain data, so a marshal
// failure is a programming defect.
func mustJSON(v any) json.RawMessage {
	data, err := json.Marshal(v)
	if err != nil {
		panic(fmt.Sprintf("coord: marshaling a response: %v", err))
	}
	return data
}

// registryEntry finds one entry by app and slug.
func registryEntry(reg store.RegistryFile, app, slug string) *store.Entry {
	for i := range reg.Entries {
		if reg.Entries[i].App == app && reg.Entries[i].Slug == slug {
			return &reg.Entries[i]
		}
	}
	return nil
}

// registrySlotHeld reports whether any entry of the app holds the slot —
// every state holds it: reserving, active and tearing-down all keep the
// slot claimed (ARCHITECTURE.md §11.2).
func registrySlotHeld(reg store.RegistryFile, app string, slot int) bool {
	for i := range reg.Entries {
		if reg.Entries[i].App == app && reg.Entries[i].Slot == slot {
			return true
		}
	}
	return false
}

// registrySlotEntry returns the entry holding an app's slot, if any.
func registrySlotEntry(reg store.RegistryFile, app string, slot int) *store.Entry {
	for i := range reg.Entries {
		if reg.Entries[i].App == app && reg.Entries[i].Slot == slot {
			return &reg.Entries[i]
		}
	}
	return nil
}

// findBand returns an app's registered band, if any.
func findBand(bands store.BandsFile, app string) *store.Band {
	for i := range bands.Bands {
		if bands.Bands[i].App == app {
			return &bands.Bands[i]
		}
	}
	return nil
}

// hasPortResources reports whether the spec declares any port resource.
func hasPortResources(s *spec.Spec) bool {
	for i := range s.Resources {
		if s.Resources[i].Type == "port" {
			return true
		}
	}
	return false
}
