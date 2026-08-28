// Package driver is M3's six-operation contract: the interface every
// allocatable resource type implements, the registry that maps a resource
// type to its driver, and the sequencing (apply in dependency order, teardown
// in reverse, continuing past failures) that phase 5's init and rm lean on.
//
// The contract is docs/design/03-drivers.md §2: derive, probe, verify and
// blastRadius are required; apply and teardown are optional, independently of
// each other. Three of the four combinations are real in this phase — port
// has neither, namespace has teardown but no apply, state-path has both —
// and the conformance suite (conformance_test.go) pins the contract once per
// driver, so phase 8's cidr and machine join as rows of that table rather
// than as new test files (plan.md §4).
//
// Two rules bind every operation. A driver never creates must discover: the
// coordinator never creates a container, so the namespace driver finds the
// project's objects again by asking the daemon for everything carrying
// `com.docker.compose.project=<name>` — that generalises to the whole
// contract (ARCHITECTURE.md §7.1). And a teardown handle always comes from
// the registry, never from anything on disk in the worktree: people run
// `git worktree remove` by hand first, so every handle a driver needs is
// derivable from slot and spec alone, which is what makes the registry's
// denormalised resources field sufficient (03-drivers.md §2.2).
//
// Drivers are stateless. Everything an operation needs beyond the resolved
// value arrives in Env, built fresh per call by the coordinator, and no
// driver holds state between calls.
package driver

import (
	"fmt"
	"strings"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
	"github.com/mrgeoffrich/worktree-manager/internal/spec"
	"github.com/mrgeoffrich/worktree-manager/internal/store"
)

// Driver is the six-operation contract. Apply and Teardown are optional,
// independently of each other: HasApply and HasTeardown declare which half of
// the optional pair a driver implements, and the no-op implementations
// return zero values so the contract's call sites can treat an absent
// operation as a successful no-op.
type Driver interface {
	// Type is the resource type this driver serves: "port", "namespace",
	// "state-path", and later "cidr" and "machine".
	Type() string

	// HasApply reports whether this driver implements apply.
	HasApply() bool
	// HasTeardown reports whether this driver implements teardown.
	HasTeardown() bool
	// GatesAllocation reports whether a Held probe result skips a candidate
	// slot at allocation. Ports gate allocation; a namespace hit means a
	// previous teardown was incomplete, not that the slot is taken
	// (03-drivers.md §4.2), so the namespace driver does not gate.
	GatesAllocation() bool

	// Derive computes the value for one slot. spec.Resolve is the single
	// derivation — a driver calls it with ctx and returns its own resource's
	// row, so port stride/group arithmetic and template evaluation exist
	// once and the driver cannot contradict them.
	Derive(r *spec.Resource, s *spec.Spec, ctx spec.Context) (any, error)

	// Probe reports whether value is free right now. Unavailable is a third
	// result, distinct from success and failure: an unavailable probe does
	// not block allocation (plan.md §3).
	Probe(r *spec.Resource, value any, env Env) ProbeResult

	// Apply creates value, where creation is the tool's job. Optional.
	Apply(r *spec.Resource, value any, env Env) (ApplyResult, error)

	// Teardown destroys value. Optional. The handle arrives from the
	// registry, never from the working tree (03-drivers.md §2.2).
	Teardown(r *spec.Resource, value any, env Env) error

	// Verify reports drift, changing nothing. The finding that catches the
	// silent-attach failure — a compose file pinning name: — lives here.
	Verify(r *spec.Resource, value any, env Env) ([]Finding, error)

	// BlastRadius is prose for the shared block: what sharing this resource
	// costs every worktree. Phase 5 renders it into the descriptor; the
	// conformance suite pins that it is non-empty.
	BlastRadius(r *spec.Resource, s *spec.Spec) string
}

// Registry maps a resource type to its driver. It is built once at
// coordinator startup; phase 8 adds cidr and machine to the same registry.
type Registry map[string]Driver

// NewRegistry builds the registry from the compiled-in drivers. A resource
// type with no driver has no row and is skipped by the sequencing — cidr and
// machine are declared types with no driver until phase 8, and both have no
// teardown, so skipping them is the design's answer rather than a defect.
func NewRegistry(drivers ...Driver) Registry {
	reg := make(Registry, len(drivers))
	for _, d := range drivers {
		reg[d.Type()] = d
	}
	return reg
}

// Driver returns the driver for a resource type, or nil when the type has no
// driver in this phase.
func (r Registry) Driver(t string) Driver { return r[t] }

// Env is everything an operation needs beyond the resolved value: the spec it
// came from, the machine context, the ledger's host-global reservations and
// the docker seam. Operations run in the coordinator's namespaces, so the
// host's docker socket is reachable and operations report unavailable far
// less often than 03-drivers.md §2.3 assumed (plan.md §2 supersedes it).
//
// The coordinator builds Env fresh per call: Home from the coordinator's
// process, Reservations from bands.json at that moment, Docker the one real
// runner. Tests build their own with fakes.
type Env struct {
	// Spec is the committed wt.yaml the values were derived from. Teardown
	// needs it for the dependent projects of a namespace and the purge
	// refusal's seed.from; verify needs it for the files a compose namespace
	// governs.
	Spec *spec.Spec
	// App, Slug and Slot identify the allocation the operation belongs to.
	// Teardown resolves seed.from templates against the same context the
	// values were derived with, so the identity rides along.
	App  string
	Slug string
	Slot int
	// Bases are the app's port band bases, the same map spec.Resolve's
	// context carries. Derive feeds them through; the state-path driver
	// needs them to resolve a seed.from that references a port resource.
	Bases map[string]int
	// Resolved is the denormalised resource table the values came from, so
	// a template like seed.from can reference another resource by name.
	Resolved map[string]spec.Resolved
	// Home is the coordinator's home directory, the {home} the templates
	// resolved against.
	Home string
	// Worktree is the worktree root as the registry records it —
	// informational for verify, which is the one operation permitted to read
	// inside the tree (03-drivers.md §8). Teardown never uses it: the
	// directory is routinely gone by the time teardown runs.
	Worktree string
	// SeedModes maps a state-path resource name to the seed mode chosen for
	// this run, overriding the spec's seed.default. Phase 5's init passes
	// the modes the user chose; absent an override the spec's default
	// applies (03-drivers.md §4.4).
	SeedModes map[string]string
	// PurgeFlags are the CLI purge flags the caller passed (e.g.
	// "--purge-db"). A state-path resource whose purge.flag was passed is
	// purged on teardown; every other state-path is left alone — teardown
	// only happens when the purge flag is given (03-drivers.md §4.4).
	PurgeFlags []string
	// KeepFlags are the CLI keep flags the caller passed (e.g.
	// "--keep-vm"). A machine resource whose keep_flag was passed is left
	// up on teardown — the entry drops but the VM stays (B4.3).
	KeepFlags []string
	// Reservations are the ledger's host-global reservations at call time.
	// The namespace driver refuses to tear down a project whose name matches
	// one (03-drivers.md §4.2, B8.2).
	Reservations []Reservation
	// Docker is the docker CLI seam. The real implementation shells out to
	// the docker binary; tests install fakes. nil means no docker is
	// reachable, which every docker-touching operation reports as
	// unavailable.
	Docker Docker
	// Machine is the platform's VM runner seam (Colima on macOS, WSL2 on
	// Windows). The real implementation lives in internal/platform; tests
	// install fakes, exactly like Docker. nil means no runner is
	// installed, which every machine-touching operation reports as
	// unavailable.
	Machine platform.MachineRunner
	// MachineLogDir is the directory the machine driver writes a starting
	// instance's output into, one file per instance
	// (machine-<instance>.log). The coordinator sets it to
	// <store root>/logs; an empty value means no destination is
	// configured, and the machine driver still runs the grace-period wait
	// (1a) but the child's output past that point is not kept anywhere.
	MachineLogDir string
}

// Reservation is one host-global reservation as the ledger declares it:
// ports no app may allocate from, compose project names no teardown may
// reach, and the required note naming what holds the range. Nothing infers a
// production stack — a person declares it once per machine (plan.md §8, R6).
//
// It is the ledger's own type. A driver reads a reservation it was handed
// and never writes one, and two declarations of the same three fields would
// let the rail and the ledger disagree about what was reserved.
type Reservation = store.Reservation

// ProbeResult is the probe's three-way answer. Unavailable is distinct from
// success and failure and the distinction is load-bearing at the call site:
// an unavailable probe does not block allocation, whereas an unavailable
// teardown does block freeing the slot (plan.md §3, 03-drivers.md §2.3).
type ProbeResult int

const (
	// ProbeFree means the value is not held by anything the probe can see.
	ProbeFree ProbeResult = iota
	// ProbeHeld means the value is in use right now.
	ProbeHeld
	// ProbeUnavailable means the probe cannot run — the docker daemon is
	// unreachable, the bind cannot be attempted. The caller proceeds on the
	// registry alone and says so.
	ProbeUnavailable
)

// String names the result for reports.
func (r ProbeResult) String() string {
	switch r {
	case ProbeFree:
		return "free"
	case ProbeHeld:
		return "held"
	case ProbeUnavailable:
		return "unavailable"
	}
	return "unknown"
}

// ApplyResult is what an apply reports beyond success: the notes phase 7
// renders into the briefing. A seeded state store is a snapshot taken at
// creation that diverges as work continues, not a live mirror — the driver
// supplies the fact that seeding occurred and when (03-drivers.md §4.4,
// B6.2).
type ApplyResult struct {
	// Notes are bounded-coverage statements: what was seeded, from where,
	// when, and what the operation could not do. A silent degrade reads as
	// success, so anything less than a full job is stated here.
	Notes []string
}

// Note appends one note to the apply result.
func (a *ApplyResult) Note(format string, args ...any) {
	a.Notes = append(a.Notes, fmt.Sprintf(format, args...))
}

// Finding is one row of a verify report: drift or a stated fact, level-tagged
// so a phase-6 doctor can order them. Verify changes nothing; a check that
// cannot run from here is reported as a finding, never as a silent pass.
type Finding struct {
	// Resource is the resource name the finding belongs to.
	Resource string `json:"resource"`
	// Kind names what the driver found, as a stable slug ("port-bound",
	// "path-missing"). A caller that must treat one finding differently
	// from another of the same level — the coordinator reads a bound port
	// on a running worktree as the expected state, and an unprobeable one
	// as unverified — keys on this rather than on the message text.
	Kind string `json:"kind"`
	// Level is "info", "warning" or "error".
	Level string `json:"level"`
	// Message is the finding, naming the remedy where one exists.
	Message string `json:"message"`
}

// Finding levels.
const (
	LevelInfo    = "info"
	LevelWarning = "warning"
	LevelError   = "error"
)

// RefusalError is a safety refusal: an operation the driver will not
// perform, no matter how the caller asks — purging a state path that is or
// contains the shared source, or tearing down a namespace matching a
// host-global reservation (ARCHITECTURE.md §12.1). The sequencing collects
// refusals separately from failures, and the coordinator maps one to exit
// code 3 (refused by a safety check).
type RefusalError struct{ Reason string }

func (e *RefusalError) Error() string { return e.Reason }

// Survivor names one object a teardown could not remove: what it is, which
// resource was being torn down, and why it survived. The coordinator
// reports exactly the survivors and keeps the slot held while any remain
// (B2.3, 03-drivers.md §5).
type Survivor struct {
	Kind     string `json:"kind"`     // container, network, volume, path
	Name     string `json:"name"`     // the object's id or name
	Resource string `json:"resource"` // the resource being torn down
	Reason   string `json:"reason"`
}

// TeardownError is what a teardown returns when it did not get everything,
// or when it got everything but has something worth saying about how
// (item 2). The driver continues past a failure rather than stopping at
// the first one — stopping early would leave more behind than continuing
// does (03-drivers.md §5) — and reports what survived. ErrUnavailable is
// not a TeardownError: an unreachable daemon means nothing was attempted,
// which the coordinator reports differently (exit 4).
type TeardownError struct {
	Resource  string
	Survivors []Survivor
	// Notes are bounded-coverage statements that are not failures — a
	// namespace's forced removal (or, failing that, disconnection) of a
	// foreign container attached to its network before the network itself
	// could be removed (item 2). They are carried even when Survivors is
	// empty, because "nothing survived" and "nothing happened worth
	// mentioning" are different claims: a widened teardown that reached
	// outside the project label must never be silent about it.
	Notes []string
}

func (e *TeardownError) Error() string {
	if len(e.Survivors) == 0 {
		return fmt.Sprintf("teardown of %s reported %d note(s), nothing left behind: %s",
			e.Resource, len(e.Notes), strings.Join(e.Notes, "; "))
	}
	parts := make([]string, len(e.Survivors))
	for i, s := range e.Survivors {
		parts[i] = fmt.Sprintf("%s %s", s.Kind, s.Name)
	}
	return fmt.Sprintf("teardown of %s left %d object(s) behind: %s",
		e.Resource, len(e.Survivors), strings.Join(parts, ", "))
}
