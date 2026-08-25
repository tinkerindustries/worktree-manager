package spec

import (
	"fmt"
	"net"
	"regexp"
	"slices"
	"strings"
	"time"
)

// paramNameRe is the hook parameter-name pattern; parameters become flags in
// phase 5, so names are flag-like.
var paramNameRe = regexp.MustCompile(`^[a-z][a-z0-9_-]*$`)

// validateStatic checks everything about a spec that does not depend on a
// resolution context. Template reference and cycle checks are here too; the
// resolved-value length caps are checked by Validate's worst-case resolve,
// because a resolved name only exists once templates are evaluated.
func validateStatic(s *Spec) error {
	if s.App == "" {
		return &FieldError{Field: "app", Reason: "required — the machine-global name that keys the slot namespace (M2 §5.1)"}
	}

	max := effectiveSlotMax(s)
	if s.Slots.Max != nil && *s.Slots.Max < 1 {
		return &FieldError{Field: "slots.max", Reason: fmt.Sprintf("%d is below 1; slots run from 1 upward (slot 0 is the primary checkout)", *s.Slots.Max)}
	}

	names := make(map[string]bool, len(s.Resources))
	for i := range s.Resources {
		r := &s.Resources[i]
		if err := validateResourceName(i, r, names); err != nil {
			return err
		}
		names[r.Name] = true
	}
	for i := range s.Resources {
		r := &s.Resources[i]
		if err := validateResourceFields(i, r, max); err != nil {
			return err
		}
	}

	// Stride ceiling: a port resource in stride form caps slots at 99
	// (M2 §6.1), enforced at validation because it is a property of the
	// allocation form rather than a free choice.
	if max > StrideSlotCeiling {
		for i := range s.Resources {
			r := &s.Resources[i]
			if r.Type == "port" && portForm(r) != "group" {
				return &FieldError{
					Field: "slots.max",
					Reason: fmt.Sprintf(
						"%d exceeds the stride-form ceiling of %d (M2 §6.1), and port resource %q is in stride form; lower slots.max or use group form",
						max, StrideSlotCeiling, r.Name),
				}
			}
		}
	}

	// Template references: every variable must be a builtin or a resource,
	// on every template-bearing resource.
	resourceSet := make(map[string]bool, len(s.Resources))
	for i := range s.Resources {
		resourceSet[s.Resources[i].Name] = true
	}
	for i := range s.Resources {
		r := &s.Resources[i]
		if r.Template != nil {
			if err := validateTemplateVars(*r.Template, resField(i, "template"), resourceSet); err != nil {
				return err
			}
		}
	}
	if cyc := findTemplateCycle(s); cyc != nil {
		idx := resourceIndex(s, cyc[0])
		return &FieldError{
			Field:  resField(idx, "template"),
			Reason: fmt.Sprintf("template cycle: %s", strings.Join(cyc, " → ")),
		}
	}

	if err := validateReserved(&s.Reserved); err != nil {
		return err
	}
	for i := range s.Shared {
		if err := validateShared(i, &s.Shared[i], resourceSet); err != nil {
			return err
		}
	}
	if err := validateHooks(&s.Hooks, resourceSet); err != nil {
		return err
	}
	if err := validateReaper(&s.Reaper); err != nil {
		return err
	}
	if err := validateRemoval(&s.Removal); err != nil {
		return err
	}
	if err := validateWorktrees(&s.Worktrees); err != nil {
		return err
	}
	return validateEmit(&s.Emit, resourceSet)
}

func resField(i int, field string) string {
	return fmt.Sprintf("resources[%d].%s", i, field)
}

func validateResourceName(i int, r *Resource, names map[string]bool) error {
	if r.Name == "" {
		return &FieldError{Field: resField(i, "name"), Reason: "required"}
	}
	if len(r.Name) > ResourceNameMaxLen {
		return &FieldError{Field: resField(i, "name"), Reason: fmt.Sprintf("%q is %d characters; cap is %d", r.Name, len(r.Name), ResourceNameMaxLen)}
	}
	if !resourceNameRe.MatchString(r.Name) {
		return &FieldError{Field: resField(i, "name"), Reason: fmt.Sprintf("%q is not a valid resource name (must match %s)", r.Name, resourceNameRe)}
	}
	if isBuiltinVar(r.Name) {
		return &FieldError{Field: resField(i, "name"), Reason: fmt.Sprintf("%q is a reserved template variable ({app}, {slug}, {slot}, {home}, {worktree})", r.Name)}
	}
	if names[r.Name] {
		return &FieldError{Field: resField(i, "name"), Reason: fmt.Sprintf("duplicate resource name %q", r.Name)}
	}
	return nil
}

// portForm returns the resource's port form, applying the stride default.
func portForm(r *Resource) string {
	if r.Form != nil {
		return *r.Form
	}
	return "stride"
}

// exhaustionMode returns the cidr resource's exhaustion behaviour,
// applying the shared-pool default (M3 §4.3: "B5.2 requires a fallback to
// the shared pool rather than an outright failure").
func exhaustionMode(r *Resource) string {
	if r.OnExhaustion != nil {
		return *r.OnExhaustion
	}
	return DefaultCIDRExhaustion
}

// validateResourceFields checks the per-type field set and the per-type
// constraints. A field that is not valid for the declared type is refused,
// so a state-path carrying `pool:` cannot be silently ignored.
func validateResourceFields(i int, r *Resource, slotMax int) error {
	field := func(f string) string { return resField(i, f) }
	switch r.Type {
	case "port":
		for _, f := range []string{"kind", "files", "pool", "on_exhaustion", "template", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"} {
			if err := absent(r, f, field(f)); err != nil {
				return err
			}
		}
		if r.Form != nil && *r.Form != "stride" && *r.Form != "group" {
			return &FieldError{Field: field("form"), Reason: fmt.Sprintf("unknown port form %q; supported forms: [stride group]", *r.Form)}
		}
		if portForm(r) == "group" {
			if r.Size != nil && *r.Size < 1 {
				return &FieldError{Field: field("size"), Reason: fmt.Sprintf("group size %d is below 1", *r.Size)}
			}
		} else {
			if r.Size != nil {
				return &FieldError{Field: field("size"), Reason: "only valid for group-form port resources"}
			}
			if r.Offset != nil {
				return &FieldError{Field: field("offset"), Reason: "only valid for group-form port resources"}
			}
		}
	case "namespace":
		for _, f := range []string{"form", "size", "offset", "pool", "on_exhaustion", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"} {
			if err := absent(r, f, field(f)); err != nil {
				return err
			}
		}
		if r.Kind != nil && *r.Kind != "compose" && *r.Kind != "plain" {
			return &FieldError{Field: field("kind"), Reason: fmt.Sprintf("unknown namespace kind %q; supported kinds: [compose plain]", *r.Kind)}
		}
		if r.Kind != nil && *r.Kind == "plain" && len(r.Files) > 0 {
			return &FieldError{Field: field("files"), Reason: "only valid for kind: compose — plain namespaces govern no compose files"}
		}
		if err := requireTemplate(i, r); err != nil {
			return err
		}
	case "cidr":
		for _, f := range []string{"form", "offset", "kind", "files", "template", "default", "flag", "seed", "purge", "driver", "max_concurrent", "keep_flag"} {
			if err := absent(r, f, field(f)); err != nil {
				return err
			}
		}
		if r.Pool == nil || *r.Pool == "" {
			return &FieldError{Field: field("pool"), Reason: "required"}
		}
		_, poolNet, err := net.ParseCIDR(*r.Pool)
		if err != nil {
			return &FieldError{Field: field("pool"), Reason: fmt.Sprintf("%q is not a valid CIDR: %v", *r.Pool, err)}
		}
		// The block arithmetic is 32-bit throughout, so an IPv6 pool
		// parses here and then has nowhere to go.
		if poolNet.IP.To4() == nil {
			return &FieldError{Field: field("pool"), Reason: fmt.Sprintf("%q is not an IPv4 CIDR; the cidr driver allocates IPv4 blocks only", *r.Pool)}
		}
		poolBits, _ := poolNet.Mask.Size()
		if r.Size == nil {
			return &FieldError{Field: field("size"), Reason: "required — the per-slot mask, e.g. 22 for a /22 per worktree"}
		}
		if *r.Size < poolBits || *r.Size > 32 {
			return &FieldError{Field: field("size"), Reason: fmt.Sprintf("%d is outside %d..32 for pool %s", *r.Size, poolBits, *r.Pool)}
		}
		if r.OnExhaustion != nil && *r.OnExhaustion != "shared-pool" && *r.OnExhaustion != "fail" {
			return &FieldError{Field: field("on_exhaustion"), Reason: fmt.Sprintf("unknown on_exhaustion %q; supported values: [shared-pool fail]", *r.OnExhaustion)}
		}
	case "state-path":
		for _, f := range []string{"form", "size", "offset", "kind", "files", "pool", "on_exhaustion", "driver", "max_concurrent", "keep_flag"} {
			if err := absent(r, f, field(f)); err != nil {
				return err
			}
		}
		if err := requireTemplate(i, r); err != nil {
			return err
		}
		if r.Default != nil && *r.Default != "isolated" && *r.Default != "shared" {
			return &FieldError{Field: field("default"), Reason: fmt.Sprintf("unknown default %q; supported values: [isolated shared]", *r.Default)}
		}
		if r.Seed != nil {
			if r.Seed.From == "" {
				return &FieldError{Field: field("seed.from"), Reason: "required — the shared source the seed modes copy or point at"}
			}
			if len(r.Seed.Modes) == 0 {
				return &FieldError{Field: field("seed.modes"), Reason: "required — a non-empty subset of [seeded empty shared]"}
			}
			seen := map[string]bool{}
			for _, m := range r.Seed.Modes {
				if m != "seeded" && m != "empty" && m != "shared" {
					return &FieldError{Field: field("seed.modes"), Reason: fmt.Sprintf("unknown seed mode %q; supported modes: [seeded empty shared]", m)}
				}
				if seen[m] {
					return &FieldError{Field: field("seed.modes"), Reason: fmt.Sprintf("duplicate seed mode %q", m)}
				}
				seen[m] = true
			}
			def := DefaultSeedMode
			if r.Seed.Default != nil {
				def = *r.Seed.Default
			}
			if !seen[def] {
				return &FieldError{Field: field("seed.default"), Reason: fmt.Sprintf("seed default %q is not one of the declared modes %v", def, r.Seed.Modes)}
			}
		}
	case "machine":
		for _, f := range []string{"form", "size", "offset", "kind", "files", "pool", "on_exhaustion", "default", "flag", "seed", "purge"} {
			if err := absent(r, f, field(f)); err != nil {
				return err
			}
		}
		if err := requireTemplate(i, r); err != nil {
			return err
		}
		if r.Driver != nil && *r.Driver != "auto" {
			return &FieldError{Field: field("driver"), Reason: fmt.Sprintf("unknown machine driver %q; the only driver is auto (M3 §4.5)", *r.Driver)}
		}
		if r.MaxConcurrent != nil && *r.MaxConcurrent < 1 {
			return &FieldError{Field: field("max_concurrent"), Reason: fmt.Sprintf("%d is below 1", *r.MaxConcurrent)}
		}
	default:
		return &FieldError{Field: field("type"), Reason: fmt.Sprintf("unknown resource type %q; supported types: [port namespace cidr state-path machine]", r.Type)}
	}
	return nil
}

// fieldSet reports whether one optional resource field is set. The table
// is keyed by the same names the per-type field lists use, so a name in a
// list that has no entry here is a missing rule rather than a silently
// skipped one — absent refuses it.
var fieldSet = map[string]func(*Resource) bool{
	"form":           func(r *Resource) bool { return r.Form != nil },
	"size":           func(r *Resource) bool { return r.Size != nil },
	"offset":         func(r *Resource) bool { return r.Offset != nil },
	"kind":           func(r *Resource) bool { return r.Kind != nil },
	"files":          func(r *Resource) bool { return len(r.Files) > 0 },
	"pool":           func(r *Resource) bool { return r.Pool != nil },
	"on_exhaustion":  func(r *Resource) bool { return r.OnExhaustion != nil },
	"template":       func(r *Resource) bool { return r.Template != nil },
	"default":        func(r *Resource) bool { return r.Default != nil },
	"flag":           func(r *Resource) bool { return r.Flag != nil },
	"seed":           func(r *Resource) bool { return r.Seed != nil },
	"purge":          func(r *Resource) bool { return r.Purge != nil },
	"driver":         func(r *Resource) bool { return r.Driver != nil },
	"max_concurrent": func(r *Resource) bool { return r.MaxConcurrent != nil },
	"keep_flag":      func(r *Resource) bool { return r.KeepFlag != nil },
}

// absent refuses a field that is not valid for the declared resource type.
// A field name with no entry in fieldSet is refused as a defect: silently
// returning nil would disable a validation rule with no compile error.
func absent(r *Resource, field, name string) error {
	set, ok := fieldSet[field]
	if !ok {
		return &FieldError{Field: name, Reason: fmt.Sprintf("internal: no rule for field %q", field)}
	}
	if set(r) {
		return &FieldError{Field: name, Reason: "not valid for this resource type"}
	}
	return nil
}

func requireTemplate(i int, r *Resource) error {
	if r.Template == nil || *r.Template == "" {
		return &FieldError{Field: resField(i, "template"), Reason: "required"}
	}
	return nil
}

// findTemplateCycle returns one cycle as a resource-name path (a → b → a),
// or nil when the resource templates form a DAG. Only template-bearing
// resources have outgoing edges; ports and cidrs are leaves.
func findTemplateCycle(s *Spec) []string {
	byName := make(map[string]*Resource, len(s.Resources))
	for i := range s.Resources {
		byName[s.Resources[i].Name] = &s.Resources[i]
	}
	edges := make(map[string][]string, len(s.Resources))
	indeg := make(map[string]int, len(s.Resources))
	// First pass: initialise every node's in-degree to 0. The reset must
	// not run inside the edge-building pass, or a node's counted
	// in-degree is wiped just before its own edges are counted.
	for i := range s.Resources {
		indeg[s.Resources[i].Name] = 0
	}
	for i := range s.Resources {
		r := &s.Resources[i]
		if r.Template == nil {
			continue
		}
		vars, err := templateVars(*r.Template)
		if err != nil {
			return nil // reported by validateTemplateVars
		}
		for _, v := range vars {
			if isBuiltinVar(v) {
				continue
			}
			if _, ok := byName[v]; ok {
				edges[r.Name] = append(edges[r.Name], v)
				indeg[v]++
			}
		}
	}
	queue := []string{}
	for name, d := range indeg {
		if d == 0 {
			queue = append(queue, name)
		}
	}
	for len(queue) > 0 {
		n := queue[0]
		queue = queue[1:]
		for _, m := range edges[n] {
			indeg[m]--
			if indeg[m] == 0 {
				queue = append(queue, m)
			}
		}
	}
	// Leftovers hold at least one cycle, but not every leftover is on one:
	// a node merely referenced by a cycle member is left over too, and has
	// no edge back into the set. A walk that started there would report a
	// path naming no resource, so the search is a depth-first one that
	// returns a real cycle.
	order := make([]string, 0, len(s.Resources))
	for i := range s.Resources {
		order = append(order, s.Resources[i].Name)
	}
	return cycleInLeftovers(order, edges, indeg)
}

// cycleInLeftovers finds one cycle among the nodes Kahn's algorithm left
// behind, following the first back edge a depth-first search meets. order is
// declaration order, so the cycle reported for a given spec is always the
// same one.
func cycleInLeftovers(order []string, edges map[string][]string, indeg map[string]int) []string {
	const (
		white = 0
		grey  = 1
		black = 2
	)
	color := make(map[string]int, len(order))
	var stack []string
	var walk func(string) []string
	walk = func(n string) []string {
		color[n] = grey
		stack = append(stack, n)
		for _, m := range edges[n] {
			if indeg[m] == 0 {
				continue // not a leftover: cannot be on a cycle
			}
			switch color[m] {
			case white:
				if c := walk(m); c != nil {
					return c
				}
			case grey:
				for i, v := range stack {
					if v == m {
						return append(append([]string{}, stack[i:]...), m)
					}
				}
			}
		}
		stack = stack[:len(stack)-1]
		color[n] = black
		return nil
	}
	for _, n := range order {
		if indeg[n] > 0 && color[n] == white {
			if c := walk(n); c != nil {
				return c
			}
		}
	}
	return nil
}

func resourceIndex(s *Spec, name string) int {
	for i := range s.Resources {
		if s.Resources[i].Name == name {
			return i
		}
	}
	return -1
}

func validateReserved(r *Reserved) error {
	seen := map[int]bool{}
	for _, p := range r.Ports {
		if p < 1 || p > 65535 {
			return &FieldError{Field: "reserved.ports", Reason: fmt.Sprintf("%d is not a port number (1..65535)", p)}
		}
		if seen[p] {
			return &FieldError{Field: "reserved.ports", Reason: fmt.Sprintf("duplicate reserved port %d", p)}
		}
		seen[p] = true
	}
	return nil
}

func validateShared(i int, sh *Shared, resources map[string]bool) error {
	if sh.Name == "" {
		return &FieldError{Field: fmt.Sprintf("shared[%d].name", i), Reason: "required"}
	}
	if err := validateTemplateVars(sh.Name, fmt.Sprintf("shared[%d].name", i), resources); err != nil {
		return err
	}
	if sh.Impact == "" {
		return &FieldError{Field: fmt.Sprintf("shared[%d].impact", i), Reason: "required — the blast radius, read by the session inside the worktree (M3 §6)"}
	}
	return nil
}

// HookNames is the six hooks in run order: install, prepull, build, start,
// seed, health. start is the sixth, required by the init sequence table in
// 04-lifecycle.md §4 and defined by ARCHITECTURE.md §6.1 ("run the hooks
// that bring the stack up").
var HookNames = []string{"install", "prepull", "build", "start", "seed", "health"}

func validateHooks(h *Hooks, resources map[string]bool) error {
	for _, name := range HookNames {
		hook := HookByName(h, name)
		if hook == nil {
			continue
		}
		field := "hooks." + name
		if hook.Run == "" {
			return &FieldError{Field: field + ".run", Reason: "required — the command string"}
		}
		// A hook's run template may reference its own declared params
		// (04-lifecycle §5.2: `seed: ./scripts/seed.sh --profile
		// {seed_profile}`), so the hook's param names join the builtin
		// variables and resource names for this hook's run only.
		vars, err := templateVars(hook.Run)
		if err != nil {
			return &FieldError{Field: field + ".run", Reason: err.Error()}
		}
		for _, v := range vars {
			if isBuiltinVar(v) || resources[v] {
				continue
			}
			if _, isParam := hook.Params[v]; isParam {
				continue
			}
			if resourceNameRe.MatchString(v) {
				return &FieldError{Field: field + ".run", Reason: fmt.Sprintf("unknown resource %q in template", v)}
			}
			return &FieldError{Field: field + ".run", Reason: fmt.Sprintf("unknown template variable {%s}", v)}
		}
		if hook.Timeout != "" {
			if _, err := time.ParseDuration(hook.Timeout); err != nil {
				return &FieldError{Field: field + ".timeout", Reason: fmt.Sprintf("%q is not a Go duration string: %v", hook.Timeout, err)}
			}
		}
		for pname := range hook.Params {
			pfield := field + ".params." + pname
			if !paramNameRe.MatchString(pname) {
				return &FieldError{Field: pfield, Reason: fmt.Sprintf("invalid parameter name %q", pname)}
			}
		}
	}
	return nil
}

func HookByName(h *Hooks, name string) *Hook {
	switch name {
	case "install":
		return h.Install
	case "prepull":
		return h.Prepull
	case "build":
		return h.Build
	case "start":
		return h.Start
	case "seed":
		return h.Seed
	case "health":
		return h.Health
	}
	return nil
}

// validateReaper checks the reaper's allowlist. The list defaults to empty
// and empty is the safe direction — a spec that names nothing signals
// nothing — so there is nothing to require; the checks are that what is
// named is name-shaped: non-empty, no whitespace, no path separators (the
// match is against the base name of the holder's command, and a name with a
// path or whitespace could never match one).
func validateReaper(r *Reaper) error {
	for i, b := range r.Binaries {
		field := fmt.Sprintf("reaper.binaries[%d]", i)
		if strings.TrimSpace(b) == "" {
			return &FieldError{Field: field, Reason: "a binary name cannot be empty"}
		}
		if strings.ContainsAny(b, " \t/\\") {
			return &FieldError{Field: field, Reason: fmt.Sprintf("%q is not a binary name: names are matched against the holder's command base name, so no path or whitespace is allowed", b)}
		}
	}
	return nil
}

// validateRemoval refuses any value that is not one of the two policies.
// A misspelt policy is refused rather than defaulted, because the default
// is the lenient direction and a typo would silently disarm a check the
// repository meant to arm.
func validateRemoval(r *Removal) error {
	for _, check := range RemovalChecks {
		var v *string
		switch check {
		case "uncommitted":
			v = r.Uncommitted
		case "unpushed":
			v = r.Unpushed
		case "open_pr":
			v = r.OpenPR
		}
		if v == nil {
			continue
		}
		if *v != RemovalRefuse && *v != RemovalWarn {
			return &FieldError{
				Field:  "removal." + check,
				Reason: fmt.Sprintf("%q is not a removal policy; the values are %q (a hit stops rm) and %q (a hit is printed and rm continues)", *v, RemovalRefuse, RemovalWarn),
			}
		}
	}
	return nil
}

func validateEmit(e *Emit, resources map[string]bool) error {
	d := &e.Descriptor
	if d.Filename == "" {
		return &FieldError{Field: "emit.descriptor.filename", Reason: "required — the descriptor is always written (05-delivery.md §2)"}
	}
	if strings.HasPrefix(d.Filename, ".") {
		return &FieldError{Field: "emit.descriptor.filename", Reason: "must not start with a dot — the descriptor sits undotted at the worktree root so plain ls shows it (M1 §4.3)"}
	}
	if strings.Contains(d.Filename, "/") {
		return &FieldError{Field: "emit.descriptor.filename", Reason: "must be a bare filename at the worktree root"}
	}
	if d.Filename == SpecFilename {
		return &FieldError{Field: "emit.descriptor.filename", Reason: "must not be wt.yaml — the descriptor would shadow the committed spec"}
	}
	if d.Format != "yaml" && d.Format != "json" {
		return &FieldError{Field: "emit.descriptor.format", Reason: fmt.Sprintf("unknown format %q; supported formats: [yaml json]", d.Format)}
	}
	if e.Env != nil {
		if e.Env.Path == "" {
			return &FieldError{Field: "emit.env.path", Reason: "required — the .env file path"}
		}
		for k, tmpl := range e.Env.Keys {
			kfield := "emit.env.keys." + k
			if !envNameRe.MatchString(k) {
				return &FieldError{Field: kfield, Reason: fmt.Sprintf("%q is not a valid environment variable name", k)}
			}
			if err := validateTemplateVars(tmpl, kfield, resources); err != nil {
				return err
			}
		}
	}
	if e.Reader != nil {
		if e.Reader.Language != "go" {
			return &FieldError{Field: "emit.reader.language", Reason: fmt.Sprintf("unsupported language %q; supported languages: [go]", e.Reader.Language)}
		}
		if e.Reader.Path == "" {
			return &FieldError{Field: "emit.reader.path", Reason: "required — where the generated reader goes"}
		}
		if e.Reader.Package == "" {
			return &FieldError{Field: "emit.reader.package", Reason: "required — the reader's Go package name"}
		}
		if !goPackageRe.MatchString(e.Reader.Package) {
			return &FieldError{Field: "emit.reader.package", Reason: fmt.Sprintf("%q is not a valid Go package name", e.Reader.Package)}
		}
		if e.Reader.EnvVar == "" {
			return &FieldError{Field: "emit.reader.env_var", Reason: "required — the app-scoped environment variable that names a descriptor (05-delivery.md §4.1)"}
		}
		if !envNameRe.MatchString(e.Reader.EnvVar) {
			return &FieldError{Field: "emit.reader.env_var", Reason: fmt.Sprintf("%q is not a valid environment variable name", e.Reader.EnvVar)}
		}
	}
	return nil
}

// effectiveSlotMax applies the default-32 slot ceiling (M2 §5.3).
func effectiveSlotMax(s *Spec) int {
	if s.Slots.Max == nil || *s.Slots.Max < 1 {
		return DefaultSlotMax
	}
	return *s.Slots.Max
}

// validateWorktrees checks the path template a repository puts its
// worktrees at. The refusals are the ones that would otherwise surface as
// a collision much later: a template with no {slug} names one directory
// for every worktree, and an unknown variable would resolve to nothing at
// all.
func validateWorktrees(w *Worktrees) error {
	if w.Path == nil {
		return nil
	}
	t := *w.Path
	const field = "worktrees.path"
	if t == "" {
		return &FieldError{Field: field, Reason: fmt.Sprintf("must not be empty; remove the key for the default (%q)", DefaultWorktreePath)}
	}
	if strings.Contains(t, `\`) {
		return &FieldError{Field: field, Reason: `must use forward slashes: the template is one committed value read on every platform, and the separator is applied when it is resolved`}
	}
	if strings.HasPrefix(t, "~") {
		return &FieldError{Field: field, Reason: "must not start with ~: the home directory is {home}, which is the one spelling every template in this spec uses"}
	}
	vars, err := templateVars(t)
	if err != nil {
		return &FieldError{Field: field, Reason: err.Error()}
	}
	seen := map[string]bool{}
	for _, v := range vars {
		if !slices.Contains(WorktreePathVars, v) {
			return &FieldError{Field: field, Reason: fmt.Sprintf("unknown template variable {%s}; a worktree path may reference %s (a slot is allocated after the tree exists, and {worktree} is the path being named)", v, strings.Join(braced(WorktreePathVars), ", "))}
		}
		seen[v] = true
	}
	if !seen["slug"] {
		return &FieldError{Field: field, Reason: "must reference {slug}: without it every worktree of this repository resolves to the same directory"}
	}
	return nil
}

// braced renders variable names as they are written in a template, for a
// refusal that can be copied straight into the spec.
func braced(names []string) []string {
	out := make([]string, 0, len(names))
	for _, n := range names {
		out = append(out, "{"+n+"}")
	}
	return out
}
