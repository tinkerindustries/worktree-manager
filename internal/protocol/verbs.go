package protocol

import (
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// Phase-3 verbs, carried by the generic Request/Response envelope of
// protocol.go. Their payloads are the wire contract both binaries share, so
// they live here rather than in the coordinator or the client.
//
// allocate, activate and release are the entry lifecycle behind phase 5's
// init and rm (02-coordination.md §13's allocate/activate/update/remove
// surface); bands.reserve and bands.list are the ledger verbs this phase
// ships to clients (plan.md §5 phase 3).

// AllocateArgs is the allocate request: the parsed spec (the client sends
// the spec with each call — ARCHITECTURE.md §8.1), the slug, the path as the
// creating client saw it, where the descriptor went, the ten-words-or-fewer
// description, and the seed credentials recorded for the owning client
// alone.
//
// SlotHint is phase 5's rebuild-from-descriptor half: when a descriptor
// exists and no registry entry does (the registry was deleted, corrupted,
// or is another machine's), init sends the descriptor's slot and the
// coordinator rebuilds the entry at that slot — the descriptor beats the
// registry (ARCHITECTURE.md §8.6 rule 1), and an existing entry for this
// (app, slug) is still authoritative (rule 5), so the hint applies only
// when no entry exists. Zero means "allocate the lowest free slot".
type AllocateArgs struct {
	Spec           spec.Spec         `json:"spec"`
	Slug           string            `json:"slug"`
	Path           string            `json:"path"`
	DescriptorPath string            `json:"descriptor_path,omitempty"`
	Description    string            `json:"description,omitempty"`
	Secrets        map[string]string `json:"secrets,omitempty"`
	SlotHint       int               `json:"slot_hint,omitempty"`
}

// SharedRow is one name-and-impact pair of the descriptor's shared block,
// computed by the coordinator — the only side that imports the drivers and
// can supply the blast-radius prose — and carried to the client's emitter,
// which copies it into the descriptor it writes (descriptor.BuildShared's
// callback half is the seam the drivers plug into).
type SharedRow struct {
	Name   string `json:"name"`
	Impact string `json:"impact"`
}

// AllocateResult is the coordinator's answer: the slot every resource
// derives from, the denormalised resource table, the state the entry was
// committed in (reserving), whether the coordinator can stat the recorded
// path, and the owner's own secrets echoed back — the owner is the only
// client they are ever served to (ARCHITECTURE.md §12.2). ProbeNote carries
// the bounded-coverage statement when the chosen slot's probe was
// unavailable: allocation proceeded on the registry alone, and the output
// says so (03-drivers.md §4.1).
//
// Path, Existed and Shared serve phase 5's init: Path is the entry's
// recorded path, which the client compares with its own to refuse a slug
// that collides with a different path under the same app (04-lifecycle.md
// §2.3); Existed reports whether an entry for this (app, slug) was already
// there, which selects between the four attach outcomes; Shared is the
// shared block the emitter writes into the descriptor.
type AllocateResult struct {
	App         string                   `json:"app"`
	Slug        string                   `json:"slug"`
	Slot        int                      `json:"slot"`
	State       string                   `json:"state"`
	Resources   map[string]spec.Resolved `json:"resources"`
	PathVisible bool                     `json:"path_visible"`
	Secrets     map[string]string        `json:"secrets,omitempty"`
	ProbeNote   string                   `json:"probe_note,omitempty"`
	// Skipped names the slots the probe held and why — a held port is never
	// remediated, and the allocator says which slot it skipped (B1.6,
	// 03-drivers.md §4.1).
	Skipped []string `json:"skipped,omitempty"`
	// Path is the entry's recorded worktree path, present only when an
	// entry already existed.
	Path string `json:"path,omitempty"`
	// Existed reports whether an entry for this (app, slug) was already in
	// the registry (init's four attach outcomes).
	Existed bool `json:"existed,omitempty"`
	// Shared is the descriptor's shared block, computed coordinator-side.
	Shared []SharedRow `json:"shared,omitempty"`
}

// EntryRef names one registry entry: the app and slug pair that identifies
// it. Mutating calls against it run the ownership check (ARCHITECTURE.md
// §4.3, §8.6 rule 6).
type EntryRef struct {
	App  string `json:"app"`
	Slug string `json:"slug"`
}

// ActivateResult is the coordinator's answer to activate: the entry's new
// state.
type ActivateResult struct {
	App   string `json:"app"`
	Slug  string `json:"slug"`
	State string `json:"state"`
}

// ReleaseResult is the coordinator's answer to release: the entry is gone.
type ReleaseResult struct {
	App     string `json:"app"`
	Slug    string `json:"slug"`
	Removed bool   `json:"removed"`
}

// ReserveBandArgs is the bands.reserve request. Two forms, mutually
// exclusive. App mode carries the parsed spec and one base per port
// resource, so the coordinator can compute the band's required size from
// the spec — the onboarding skill chooses only where the bases sit, not how
// large they are (02-coordination.md §6.2). Host mode carries the ports no
// app may allocate from, the compose project names no teardown may reach,
// and the required note naming what holds the range (plan.md §8, R6):
// nothing infers a production stack, a person declares it once per machine.
type ReserveBandArgs struct {
	Spec  spec.Spec      `json:"spec,omitempty"`
	Bases map[string]int `json:"bases,omitempty"`
	Host  bool           `json:"host,omitempty"`
	Ports []int          `json:"ports,omitempty"`
	Names []string       `json:"names,omitempty"`
	Note  string         `json:"note,omitempty"`
}

// ReserveBandResult reports what the ledger now holds. Spans carries, per
// port resource, the number of consecutive ports its base must cover — the
// required size computed from the spec (slot ceiling × ports per slot).
type ReserveBandResult struct {
	App   string         `json:"app,omitempty"`
	Bases map[string]int `json:"bases,omitempty"`
	Spans map[string]int `json:"spans,omitempty"`
	Host  bool           `json:"host,omitempty"`
	Ports []int          `json:"ports,omitempty"`
	Names []string       `json:"names,omitempty"`
	Note  string         `json:"note,omitempty"`
}

// BandInfo is one app's band as bands.list reports it.
type BandInfo struct {
	App   string         `json:"app"`
	Bases map[string]int `json:"bases"`
}

// ReservationInfo is one host-global reservation as bands.list reports it.
type ReservationInfo struct {
	Ports []int    `json:"ports"`
	Names []string `json:"names,omitempty"`
	Note  string   `json:"note"`
}

// BandsListResult is the whole ledger: the app bands and the machine-wide
// reservations.
type BandsListResult struct {
	Bands        []BandInfo        `json:"bands"`
	Reservations []ReservationInfo `json:"reservations"`
}

// --- Phase-7 verbs: ports.scan and bands.suggest -------------------------
//
// The two primitives the onboarding skill reads that phase 3 did not build:
// what is listening on this machine right now, and where an app's band
// could sit. Neither classifies anything: a listener is a fact, a
// suggestion is a range that fits, and whether either belongs to a
// production stack is the skill's judgement (plan.md §8, R6).

// PortsScanEntry is one listener as ports.scan reports it: the port, the
// owning pid, and the command name listener discovery reported. Port 0
// means discovery could not report the port (an unusual lsof dialect); the
// entry is still a fact — the process is listening on something.
type PortsScanEntry struct {
	Port    int    `json:"port"`
	PID     int    `json:"pid"`
	Command string `json:"command"`
}

// PortsScanResult is the whole scan: the listeners sorted by port, plus
// the bounded-coverage notes — what the scan could not see and why (a
// silent degrade reads as success, plan.md §3).
type PortsScanResult struct {
	Listeners []PortsScanEntry `json:"listeners"`
	Notes     []string         `json:"notes,omitempty"`
}

// SuggestBandArgs is the bands.suggest request: the spec whose port
// resources need bases. The client sends the parsed spec exactly as
// bands.reserve does — the coordinator computes the required size from it
// and proposes where the bases could sit against the current ledger.
type SuggestBandArgs struct {
	Spec spec.Spec `json:"spec"`
}

// BandSuggestion is one proposed base: the resource, the base, the span
// the band must cover (slot ceiling × ports per slot, the coordinator's
// required-size computation), and the inclusive range the base's band
// occupies.
type BandSuggestion struct {
	Resource string `json:"resource"`
	Base     int    `json:"base"`
	Span     int    `json:"span"`
	Low      int    `json:"low"`
	High     int    `json:"high"`
}

// SuggestBandResult is the coordinator's answer: one suggestion per port
// resource, in spec order, plus the bounded-coverage notes — a resource
// with no free base anywhere in the port space, or a range skipped.
type SuggestBandResult struct {
	App         string           `json:"app"`
	Suggestions []BandSuggestion `json:"suggestions"`
	Notes       []string         `json:"notes,omitempty"`
}

// --- Phase-5 verbs: materialise (init's step 3) and rm (reap + teardown) --
//
// materialise is the coordinator half of init's seven-step sequence
// (ARCHITECTURE.md §9.1): driver apply in dependency order over the
// entry's resources. rm is the whole teardown verb: reap, then the driver
// teardowns, then the entry drop.

// MaterialiseArgs is the materialise request: the spec and the entry whose
// resources are applied in dependency order.
type MaterialiseArgs struct {
	App  string    `json:"app"`
	Slug string    `json:"slug"`
	Spec spec.Spec `json:"spec"`
	// SeedModes overrides the spec's seed.default per state-path resource
	// (03-drivers.md §4.4); absent an override the spec's default applies.
	SeedModes map[string]string `json:"seed_modes,omitempty"`
}

// MaterialiseOutcome is one resource's apply outcome: the resource and the
// bounded-coverage notes its driver returned (what was seeded, from where,
// and what could not be done — a silent degrade reads as success, plan.md
// §3).
type MaterialiseOutcome struct {
	Resource string   `json:"resource"`
	Notes    []string `json:"notes,omitempty"`
}

// MaterialiseResult is the coordinator's answer to materialise. On success
// the entry stays reserving and the client activates it. On an apply
// failure with a clean reverse-order rollback the entry also stays
// reserving and the client drops it with release — the rollback that
// covers init's steps up to activation (ARCHITECTURE.md §9.1). On an apply
// failure whose rollback itself failed, the entry moves to tearing-down
// with the survivors noted and the slot stays held (B2.3): resources are
// out there, and the client must not release what survived.
type MaterialiseResult struct {
	App         string               `json:"app"`
	Slug        string               `json:"slug"`
	Outcomes    []MaterialiseOutcome `json:"outcomes"`
	State       string               `json:"state"` // "reserving" or "tearing-down"
	Failed      string               `json:"failed,omitempty"`
	Err         string               `json:"err,omitempty"`
	RolledBack  []string             `json:"rolled_back,omitempty"`
	RollbackErr string               `json:"rollback_err,omitempty"`
}

// RmArgs is the rm request: the entry (app, slug), the spec (teardown
// needs it for the dependent projects and the purge refusal), whether the
// reaper is opted out, whether this is a preview, and the CLI purge flags.
type RmArgs struct {
	App  string    `json:"app"`
	Slug string    `json:"slug"`
	Spec spec.Spec `json:"spec"`
	// KeepProcesses opts the reaper out: nothing is signalled and the
	// survivors are the caller's business.
	KeepProcesses bool `json:"keep_processes,omitempty"`
	// DryRun previews: the reaper lists what it would signal and nothing is
	// torn down.
	DryRun     bool     `json:"dry_run,omitempty"`
	PurgeFlags []string `json:"purge_flags,omitempty"`
}

// ReapAction is one process the reaper signalled — or, under --dry-run, one
// it would have. Signal names the step that ran: "TERM", "KILL" (after the
// three-second wait), or "would-signal" under --dry-run.
type ReapAction struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Port    int    `json:"port"`
	Signal  string `json:"signal"`
	// Err reports a signal that failed — the process may have exited during
	// the three-second wait, or the signal could not be delivered.
	Err string `json:"err,omitempty"`
}

// ReapHolder is one process bound to a worktree port that was reported and
// never signalled: it is not one of the spec's own binaries, or its port is
// reserved — in both cases it is probably the developer's own instance
// (B7.1, ARCHITECTURE.md §12.1).
type ReapHolder struct {
	PID     int    `json:"pid"`
	Command string `json:"command"`
	Port    int    `json:"port"`
	Reason  string `json:"reason"`
}

// ReapReport is the reaper's bounded-coverage statement: what it could and
// could not do, and what it did. A silent degrade reads as success, so
// every limit is stated here with the remedy (plan.md §3).
type ReapReport struct {
	// Available is false when the reap could not run at all — the
	// coordinator is inside a container whose namespaces discovery would
	// see, or discovery failed. The note names the remedy; teardown
	// completes regardless and the user is told to kill manually (B7.1).
	Available bool `json:"available"`
	// Note carries the unavailable reason, or the --keep-processes opt-out
	// statement.
	Note string `json:"note,omitempty"`
	// Holders are the processes bound to the worktree's ports that were
	// reported and never signalled, and why.
	Holders []ReapHolder `json:"holders,omitempty"`
	// Signalled are the spec-named binaries the reaper stopped (or would
	// have stopped, under --dry-run).
	Signalled []ReapAction `json:"signalled,omitempty"`
	// SkippedPorts are the worktree ports that were never touched because
	// they fall in the spec's reserved block or the host-global
	// reservations — the exclusion list is reused, never a second one
	// (B8.3).
	SkippedPorts []int `json:"skipped_ports,omitempty"`
	// KeptProcesses reports the --keep-processes opt-out.
	KeptProcesses bool `json:"kept_processes,omitempty"`
}

// RmResult is the coordinator's answer to rm. EntryFound is false when the
// registry holds no entry for the ref — the caller then decides between the
// two no-entry paths (04-lifecycle.md §7.3). On a clean teardown Removed is
// true and the entry is gone; otherwise TeardownNote names what survived
// and the slot stays held.
type RmResult struct {
	EntryFound bool       `json:"entry_found"`
	Reap       ReapReport `json:"reap"`
	// Path is the entry's recorded worktree path — the client's target for
	// the `git worktree remove` half, and the answer to where the tree is
	// when the caller is outside it.
	Path string `json:"path,omitempty"`
	// Resources are the entry's resource names, in teardown order — what
	// was (or, under --dry-run, would be) torn down.
	Resources    []string `json:"resources,omitempty"`
	Removed      bool     `json:"removed"`
	TeardownNote string   `json:"teardown_note,omitempty"`
}

// --- Phase-6 verbs: the fleet surface -----------------------------------
//
// list, doctor, reconcile and clients.list are phase 6's fleet verbs
// (06-fleet.md, ARCHITECTURE.md §9.3). The coordinator is the only
// component that can see every repo and the only one that holds host
// privilege, so the cross-repo reads and the reclamation writes are
// coordinator verbs; the client renders.

// ListArgs is the list request. Wide asks for the owning client's seed
// credentials — served to the owning client alone and redacted for every
// other client either way (ARCHITECTURE.md §12.2): a --wide form may show
// them to the owner, never to anyone else.
type ListArgs struct {
	Wide bool `json:"wide,omitempty"`
}

// ListEntry is one registry entry as list reports it. The flags are the
// distinct markers of 06-fleet.md §3 and ARCHITECTURE.md §9.3, and the
// whole point of the phase is that the first two stay distinct:
//
//   - stale: the coordinator can stat the recorded path and the directory
//     is gone — actionable from here;
//   - unverifiable: the path exists only inside a container the coordinator
//     cannot stat — never called stale, because calling it that would
//     invite destroying live work;
//   - reclaimable: the entry's ephemeral owner has aged out past the
//     reclamation interval, so the entry may be reclaimed by handle;
//   - foreign: the entry belongs to another client.
//
// Secrets are present only when the caller is the owning client and asked
// with --wide; every other combination is redacted structurally.
type ListEntry struct {
	App         string                   `json:"app"`
	Slug        string                   `json:"slug"`
	Slot        int                      `json:"slot"`
	Description string                   `json:"description,omitempty"`
	State       string                   `json:"state"`
	Path        string                   `json:"path"`
	PathVisible bool                     `json:"path_visible"`
	Owner       string                   `json:"owner"`
	OwnerKind   string                   `json:"owner_kind"`
	Ephemeral   bool                     `json:"ephemeral"`
	CreatedAt   string                   `json:"created_at"`
	LastSeen    string                   `json:"last_seen"`
	Resources   map[string]spec.Resolved `json:"resources"`
	Secrets     map[string]string        `json:"secrets,omitempty"`
	Flags       []string                 `json:"flags,omitempty"`
}

// ListResult is the whole registry as list reports it, across every repo.
type ListResult struct {
	Entries []ListEntry `json:"entries"`
}

// DoctorFinding is one row of a doctor report: a level, the message, and —
// for every finding that reports a problem — the exact command that fixes
// it (06-fleet.md §4, a hard rule). Info-level rows are observations
// (unverifiable, port free) and carry no remedy because there is nothing
// to fix.
type DoctorFinding struct {
	App     string `json:"app,omitempty"`
	Slug    string `json:"slug,omitempty"`
	Level   string `json:"level"` // info | warning | error
	Message string `json:"message"`
	Remedy  string `json:"remedy,omitempty"`
}

// DoctorResult is the whole report: the findings plus the bounded-coverage
// notes — what doctor could not check and why (a silent degrade reads as
// success, plan.md §3).
type DoctorResult struct {
	Findings []DoctorFinding `json:"findings"`
	Notes    []string        `json:"notes,omitempty"`
}

// ReconcileArgs is the reconcile request: the app, the spec (reap and
// teardown need it, exactly as rm does), and the refs the caller believes
// are its own or reclaimable. The coordinator re-checks every ref against
// the ownership and aged-out rules — the caller's claim is never trusted.
type ReconcileArgs struct {
	App  string     `json:"app"`
	Spec spec.Spec  `json:"spec"`
	Refs []EntryRef `json:"refs"`
}

// ReconcileOutcome is one entry's repair outcome:
//
//   - torn-down: reap plus driver teardown by handle ran and the entry
//     dropped (or moved to tearing-down with the note, when something
//     survived — the note says so);
//   - rolled-back: a reserving entry past its timeout was dropped, the
//     allocation rolled back;
//   - skipped: not this caller's to repair (a live client's entry, or a
//     reserving entry still within its timeout); the note says why;
//   - not-found: no registry entry for the ref.
type ReconcileOutcome struct {
	App    string `json:"app"`
	Slug   string `json:"slug"`
	Action string `json:"action"`
	Note   string `json:"note,omitempty"`
}

// ReconcileResult is the whole batch outcome.
type ReconcileResult struct {
	Outcomes []ReconcileOutcome `json:"outcomes"`
}

// ClientInfo is one row of the client table as clients.list reports it:
// identity, kind, the coordinator's own measurement of when it was last
// seen (never a timestamp a client wrote), how many registry entries the
// client owns, and — for an ephemeral client whose last seen is older than
// the reclamation interval — that it has aged out and its entries are
// reclaimable.
type ClientInfo struct {
	Identity  string `json:"identity"`
	Kind      string `json:"kind"`
	Ephemeral bool   `json:"ephemeral"`
	LastSeen  string `json:"last_seen"`
	Entries   int    `json:"entries"`
	AgedOut   bool   `json:"aged_out,omitempty"`
}

// ClientsListResult is the whole client table.
type ClientsListResult struct {
	Clients []ClientInfo `json:"clients"`
}
