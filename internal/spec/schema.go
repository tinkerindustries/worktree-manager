// Package spec owns the wt.yaml schema: the types, the parser, the
// validator and the template evaluator. It is a library both binaries link,
// and every later phase builds against it.
//
// The schema shape follows docs/design/03-drivers.md §3.1 for version, app,
// slots, resources, reserved and shared. hooks and emit are fixed in full by
// phase 0 (plan.md §5 phase 0): 03-drivers leaves both as {}, and the fields
// are drawn from docs/design/04-lifecycle.md §5 and docs/design/05-delivery.md
// §2 and §3. Only the field set is frozen here; the semantics of hooks and
// emission land in phases 4 and 5.
package spec

import (
	"fmt"
	"strings"
)

// SpecFilename is the name of the committed spec, constant everywhere. The
// generated descriptor reader (phase 5) depends on this name and on the
// walk-up resolution rule in FindSpecPath.
const SpecFilename = "wt.yaml"

// Version is the only spec schema version this binary understands.
const Version = 1

// SupportedVersions is what an unknown-version refusal names.
var SupportedVersions = []int{Version}

// Length caps on resolved resource names, per type. Each cap is a named
// constant with the constraint it came from. Where the design documents
// state a constraint it is quoted; where none is stated the cap is a phase-0
// choice, recorded here and in the phase-0 report.
const (
	// SlugMaxLen is the slug cap from docs/design/01-identity.md §4.1:
	// "Length is capped at 32 characters", two constraints binding (DNS
	// labels at 63 octets, sun_path at 104/108 bytes).
	SlugMaxLen = 32

	// NamespaceMaxLen is the resolved compose project name cap. M1 §4.1:
	// a project name derived from the slug becomes part of network and
	// container names, "resolvable as DNS labels and so limited to 63
	// octets".
	NamespaceMaxLen = 63

	// StatePathMaxLen is the resolved state-path cap. No document states
	// one; this is a phase-0 choice: PATH_MAX on macOS (1024), the design's
	// primary platform — M1 §4.1 cites macOS's 104-byte sun_path as its
	// tightest bound, so macOS is the binding platform for paths.
	StatePathMaxLen = 1024

	// CIDRMaxLen is the resolved cidr string cap. No document states one;
	// this is a phase-0 choice: the longest possible IPv4 CIDR string,
	// "255.255.255.255/32" (18 bytes), doubled for headroom.
	CIDRMaxLen = 64

	// MachineMaxLen is the resolved machine name cap. No document states
	// one for machine names; this is a phase-0 choice: M1 §4.1's 63-octet
	// DNS-label bound is the design's stated length limit for derived
	// names, and a machine name (Colima profile, WSL2 distro) feeds
	// docker contexts and profile paths the design does not otherwise
	// bound, so the design's stated limit is applied unchanged.
	MachineMaxLen = 63

	// ResourceNameMaxLen caps a resource's own name. No document states
	// one; this is a phase-0 choice: resource names are template variables
	// and stay slug-sized, matching the slug cap.
	ResourceNameMaxLen = 32
)

// Defaults, all stated in the design documents.
const (
	// DefaultSlotMax is the slot ceiling default: M2 §5.3, "the ceiling
	// comes from the spec, default 32".
	DefaultSlotMax = 32

	// StrideSlotCeiling is the stride-form ceiling: M2 §6.1, stride
	// "caps slots at 99". A stride spec with slots.max above 99 is
	// refused at validation.
	StrideSlotCeiling = 99

	// DefaultNamespaceKind is "compose": every spec example in
	// 03-drivers.md §3 and §4.2 declares kind: compose, and plain exists
	// only as an open question (§8).
	DefaultNamespaceKind = "compose"

	// DefaultCIDRExhaustion is "shared-pool": M3 §4.3, "B5.2 requires a
	// fallback to the shared pool rather than an outright failure".
	DefaultCIDRExhaustion = "shared-pool"

	// DefaultStatePathIsolation is "isolated": M3 §3.4, "default:
	// isolated with no flag is the ordinary case".
	DefaultStatePathIsolation = "isolated"

	// DefaultSeedMode is "seeded": M3 §4.4's example, "C2's three modes
	// ... seeded from live, empty, or shared".
	DefaultSeedMode = "seeded"

	// DefaultMachineDriver is "auto": M3 §4.5, "The driver selects by
	// platform through M8 and accepts an override". No override value is
	// named anywhere in the design, so auto is the only accepted value
	// until a phase that needs another one extends the schema.
	DefaultMachineDriver = "auto"

	// DefaultMachineMaxConcurrent is 4: M3 §4.5's example and the stated
	// reason — "past roughly four the pool exhausts".
	DefaultMachineMaxConcurrent = 4
)

// Spec is the whole wt.yaml document.
type Spec struct {
	Version   int        `yaml:"version"`
	App       string     `yaml:"app"`
	Slots     Slots      `yaml:"slots"`
	Resources []Resource `yaml:"resources"`
	Reserved  Reserved   `yaml:"reserved"`
	Shared    []Shared   `yaml:"shared"`
	Hooks     Hooks      `yaml:"hooks"`
	Reaper    Reaper     `yaml:"reaper"`
	Emit      Emit       `yaml:"emit"`
}

// Reaper is the coordinator-side reaper's configuration (04-lifecycle.md
// §6's first rail): the binaries the reaper may signal. Only binaries the
// spec names are signalled — a process that merely grabbed a port is
// reported and never signalled, because it is probably the developer's own
// instance. The list defaults to empty, so a spec that names nothing
// signals nothing: that is the safe direction and it must remain the
// default. Names are matched against the base name of each holder's
// command as listener discovery reports it.
type Reaper struct {
	Binaries []string `yaml:"binaries,omitempty"`
}

// Slots is the slot namespace ceiling. Slots run from 1 to Max; slot 0 is
// the primary checkout and is never allocated (ARCHITECTURE.md §8.4). The
// pointer records presence: absent means the default of 32 (M2 §5.3), an
// explicit value below 1 is refused.
type Slots struct {
	Max *int `yaml:"max"`
}

// Resource is one entry of the resources list. It is a flat struct carrying
// the union of every resource type's fields; the Type field selects the
// interpretation, and validation refuses a field that is not valid for the
// declared type (a state-path carrying `pool:` is refused whole, per the
// refuse-rather-than-partially-honour rule in plan.md §3).
//
// Pointer fields record presence, so validation can tell "form absent"
// (stride, the default) from "form: group" and refuse a field that does not
// belong to the declared type.
type Resource struct {
	Type string `yaml:"type"`
	Name string `yaml:"name"`

	// port
	Form   *string `yaml:"form,omitempty"` // stride (default) | group
	Size   *int    `yaml:"size,omitempty"` // port group size, or the cidr mask
	Offset *int    `yaml:"offset,omitempty"`

	// namespace
	Kind  *string  `yaml:"kind,omitempty"` // compose (default) | plain
	Files []string `yaml:"files,omitempty"`

	// cidr
	Pool         *string `yaml:"pool,omitempty"`
	OnExhaustion *string `yaml:"on_exhaustion,omitempty"` // shared-pool (default) | fail

	// state-path
	Template *string `yaml:"template,omitempty"`
	Default  *string `yaml:"default,omitempty"` // isolated (default) | shared
	Flag     *string `yaml:"flag,omitempty"`
	Seed     *Seed   `yaml:"seed,omitempty"`
	Purge    *Purge  `yaml:"purge,omitempty"`

	// machine
	Driver        *string `yaml:"driver,omitempty"` // auto (default)
	MaxConcurrent *int    `yaml:"max_concurrent,omitempty"`
	KeepFlag      *string `yaml:"keep_flag,omitempty"`
}

// Seed is a state-path resource's seeding declaration, M3 §4.4.
type Seed struct {
	From    string   `yaml:"from"`  // template; the shared source
	Modes   []string `yaml:"modes"` // subset of seeded, empty, shared
	Default *string  `yaml:"default,omitempty"`
}

// Purge is a state-path resource's purge declaration, M3 §4.4.
type Purge struct {
	Flag string `yaml:"flag,omitempty"`
}

// Reserved is the spec's committed defaults — properties of the repository,
// unlike the host-global band reservations, which live in the ledger
// (ARCHITECTURE.md §8.4).
type Reserved struct {
	Ports []int `yaml:"ports"`
}

// Shared is one hand-authored entry of the shared block, M3 §6. The other
// half of the block — every resource with default: shared — is generated in
// a later phase; the schema accepts only the hand-authored form.
type Shared struct {
	Name   string `yaml:"name"` // a template over the same variables
	Impact string `yaml:"impact"`
}

// Hooks is the six repo-declared commands the client sequences, in run
// order. The names come from 04-lifecycle.md §5 (install, prepull, build,
// seed, health) plus start, which the §4 sequence table requires and which
// wt start is defined as (ARCHITECTURE.md §6.1: "run the hooks that bring
// the stack up").
type Hooks struct {
	Install *Hook `yaml:"install,omitempty"`
	Prepull *Hook `yaml:"prepull,omitempty"`
	Build   *Hook `yaml:"build,omitempty"`
	Start   *Hook `yaml:"start,omitempty"`
	Seed    *Hook `yaml:"seed,omitempty"`
	Health  *Hook `yaml:"health,omitempty"`
}

// Hook is one hook: a command string over the same template variables the
// resource templates use, an optional timeout, and optional sticky
// parameters (04-lifecycle.md §5.2).
type Hook struct {
	Run     string           `yaml:"run"`
	Timeout string           `yaml:"timeout,omitempty"` // Go duration string; absent means no timeout
	Params  map[string]Param `yaml:"params,omitempty"`
}

// Param is one hook parameter. A sticky parameter is chosen on first run,
// persisted in the descriptor and reused (04-lifecycle.md §5.2). The field
// set is frozen here; the behaviour is phase 5.
type Param struct {
	Sticky  bool   `yaml:"sticky,omitempty"`
	Default string `yaml:"default,omitempty"`
}

// Emit declares the three delivery channels (05-delivery.md §1): the
// descriptor, the optional .env block and the optional generated reader.
type Emit struct {
	Descriptor Descriptor `yaml:"descriptor"`
	Env        *EnvEmit   `yaml:"env,omitempty"`
	Reader     *Reader    `yaml:"reader,omitempty"`
}

// Descriptor is the always-written per-worktree file (05-delivery.md §2):
// its filename, undotted, at the worktree root, and its format.
type Descriptor struct {
	Filename string `yaml:"filename"`
	Format   string `yaml:"format"` // yaml | json — the only two formats
}

// EnvEmit is the optional .env managed block (05-delivery.md §3): the file
// path, whether a first init seeds from the main checkout (B17.3), and the
// managed keys, each an environment variable name mapped to a template.
type EnvEmit struct {
	Path string            `yaml:"path"`
	Seed bool              `yaml:"seed,omitempty"`
	Keys map[string]string `yaml:"keys"`
}

// Reader is the optional generated descriptor reader (05-delivery.md §4).
// language may only be go — PLAN-SCOPE.md rules out every other language.
// path, package and env_var are what the phase-5 generator needs: where the
// file goes, its Go package name, and the app-scoped environment variable
// that names a descriptor (05-delivery.md §4.1, "BACIO_ENV, not WT_ENV").
type Reader struct {
	Language string `yaml:"language"`
	Path     string `yaml:"path"`
	Package  string `yaml:"package"`
	EnvVar   string `yaml:"env_var"`
}

// Parse decodes a wt.yaml document. It refuses a version it does not
// understand, naming the version it found and the versions it supports, and
// refuses unknown fields, so a typo cannot be silently ignored.
func Parse(data []byte) (*Spec, error) {
	var s Spec
	if err := unmarshalStrict(data, &s); err != nil {
		return nil, parseFieldError(err)
	}
	if s.Version == 0 {
		return nil, &FieldError{Field: "version", Reason: "required"}
	}
	if s.Version != Version {
		return nil, &FieldError{
			Field:  "version",
			Reason: fmt.Sprintf("unsupported version %d; supported versions: %v", s.Version, SupportedVersions),
		}
	}
	return &s, nil
}

// Validate checks the whole spec. Every failure is a FieldError naming the
// field and the reason. Validation is spec-only; it resolves templates
// against a canonical worst-case context so that a resolved name that can
// exceed its type's cap is refused here, at validation, rather than at
// allocation (M3 §7).
func Validate(s *Spec) error {
	if err := validateStatic(s); err != nil {
		return err
	}
	// Cap check: resolve with the longest legal slug, the highest legal
	// slot and fixed home/worktree paths, so the outcome does not depend
	// on the machine the validator runs on. The length-only pass resolves
	// ports to their longest possible string and tolerates cidr
	// exhaustion, which is an allocation concern (phase 4), not a
	// validation failure.
	ctx := Context{
		Slug:     strings.Repeat("a", SlugMaxLen),
		Slot:     effectiveSlotMax(s),
		Home:     "/home/wt",
		Worktree: "/home/wt/worktrees/" + strings.Repeat("a", SlugMaxLen),
	}
	if _, err := resolve(s, ctx, modeCaps); err != nil {
		return err
	}
	return nil
}
