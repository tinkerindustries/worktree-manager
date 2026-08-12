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
