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
type AllocateArgs struct {
	Spec           spec.Spec         `json:"spec"`
	Slug           string            `json:"slug"`
	Path           string            `json:"path"`
	DescriptorPath string            `json:"descriptor_path,omitempty"`
	Description    string            `json:"description,omitempty"`
	Secrets        map[string]string `json:"secrets,omitempty"`
}

// AllocateResult is the coordinator's answer: the slot every resource
// derives from, the denormalised resource table, the state the entry was
// committed in (reserving), whether the coordinator can stat the recorded
// path, and the owner's own secrets echoed back — the owner is the only
// client they are ever served to (ARCHITECTURE.md §12.2). ProbeNote carries
// the bounded-coverage statement when the chosen slot's probe was
// unavailable: allocation proceeded on the registry alone, and the output
// says so (03-drivers.md §4.1).
type AllocateResult struct {
	App         string                   `json:"app"`
	Slug        string                   `json:"slug"`
	Slot        int                      `json:"slot"`
	State       string                   `json:"state"`
	Resources   map[string]spec.Resolved `json:"resources"`
	PathVisible bool                     `json:"path_visible"`
	Secrets     map[string]string        `json:"secrets,omitempty"`
	ProbeNote   string                   `json:"probe_note,omitempty"`
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
