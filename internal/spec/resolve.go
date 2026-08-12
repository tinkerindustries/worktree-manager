package spec

import (
	"encoding/json"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
)

// Context is everything Resolve needs beyond the spec: the slot, the slug,
// the home directory, the worktree root, and one band base per port resource.
// Resolve is a pure function of spec and Context — it reads no environment
// and touches nothing. Phase 3's coordinator supplies the bases; until then
// spec explain takes them on the command line (plan.md §9.1).
type Context struct {
	App      string
	Slug     string
	Slot     int
	Home     string
	Worktree string
	Bases    map[string]int // port resource name → band base
}

// Resolved is one row of the resolved resource table.
type Resolved struct {
	Type  string `json:"type"`
	Value any    `json:"value"` // int for port resources, string otherwise
}

// UnmarshalJSON normalises a port resource's value to int on decode. The
// registry and the wire both round-trip Resolved through encoding/json,
// which decodes a bare number as float64, and every consumer of a port
// value — the probe, the verify, the reaper's discovery input — switches
// on int. A value that came back as float64 would silently disable the
// reaper (it would see no ports) and report verify errors; the descriptor
// reader normalises the same way for the same reason (descriptor.Read),
// and this is the registry's half of that rule.
func (r *Resolved) UnmarshalJSON(data []byte) error {
	type plain Resolved
	var p plain
	if err := json.Unmarshal(data, &p); err != nil {
		return err
	}
	*r = Resolved(p)
	if r.Type == "port" {
		switch v := r.Value.(type) {
		case float64:
			r.Value = int(v)
		case json.Number:
			if i, err := v.Int64(); err == nil {
				r.Value = int(i)
			}
		}
	}
	return nil
}

// Resolve computes the resource table for one slot. It checks the same
// per-type constraints validation does, against the actual context: a
// resolved name is validated against its type's cap before anything
// downstream uses it (03-drivers.md §3.3).
func Resolve(s *Spec, ctx Context) (map[string]Resolved, error) {
	return resolve(s, ctx, modeReal)
}

// resolveMode selects what port and cidr resources resolve to. modeReal is
// the real derivation; modeCaps is the length-only pass validation uses for
// its worst-case cap check, where bases are unknown and cidr exhaustion is
// an allocation concern (phase 4) rather than a validation failure.
type resolveMode int

const (
	modeReal resolveMode = iota
	modeCaps
)

func resolve(s *Spec, ctx Context, mode resolveMode) (map[string]Resolved, error) {
	if ctx.App == "" {
		ctx.App = s.App
	}
	if err := validateContext(s, ctx); err != nil {
		return nil, err
	}
	if cyc := findTemplateCycle(s); cyc != nil {
		idx := resourceIndex(s, cyc[0])
		return nil, &FieldError{
			Field:  resField(idx, "template"),
			Reason: fmt.Sprintf("template cycle: %s", strings.Join(cyc, " → ")),
		}
	}

	out := make(map[string]Resolved, len(s.Resources))
	var resolveOne func(name string) (string, error)
	resolveOne = func(name string) (string, error) {
		if v, ok := out[name]; ok {
			return fmt.Sprint(v.Value), nil
		}
		r := s.resourceByName(name)
		if r == nil {
			return "", fmt.Errorf("unknown resource %q", name)
		}
		value, err := resolveResourceValue(s, ctx, r, resolveOne, mode)
		if err != nil {
			return "", err
		}
		out[name] = Resolved{Type: r.Type, Value: value}
		return fmt.Sprint(value), nil
	}

	for i := range s.Resources {
		if _, err := resolveOne(s.Resources[i].Name); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func (s *Spec) resourceByName(name string) *Resource {
	for i := range s.Resources {
		if s.Resources[i].Name == name {
			return &s.Resources[i]
		}
	}
	return nil
}

// validateContext checks the resolution inputs: the slug rule and the slot
// ceiling, which are properties of the derivation.
func validateContext(s *Spec, ctx Context) error {
	if !slugRe.MatchString(ctx.Slug) || len(ctx.Slug) > SlugMaxLen {
		return &FieldError{
			Field:  "slug",
			Reason: fmt.Sprintf("%q is not a valid slug (must match %s, at most %d characters)", ctx.Slug, slugRe, SlugMaxLen),
		}
	}
	if ctx.Slot < 1 {
		return &FieldError{Field: "slot", Reason: fmt.Sprintf("%d is below 1; slot 0 is the primary checkout and is never allocated", ctx.Slot)}
	}
	if ctx.Slot > effectiveSlotMax(s) {
		return &FieldError{Field: "slot", Reason: fmt.Sprintf("%d exceeds slots.max %d", ctx.Slot, effectiveSlotMax(s))}
	}
	if ctx.Home == "" || ctx.Worktree == "" {
		return &FieldError{Field: "context", Reason: "home and worktree are required"}
	}
	return nil
}

// slugRe is the slug rule, needed here for template evaluation and the
// length caps: ^[a-z0-9][a-z0-9-]*$, at most 32 characters (M1 §4.1).
var slugRe = regexp.MustCompile(`^[a-z0-9][a-z0-9-]*$`)

// ValidSlug reports whether s is a legal slug. Phase 1's validateSlug is
// the same rule; it lives here because template evaluation and the length
// caps need it from phase 0.
func ValidSlug(s string) bool {
	return slugRe.MatchString(s) && len(s) <= SlugMaxLen
}

// resolveResourceValue computes one resource's value.
func resolveResourceValue(s *Spec, ctx Context, r *Resource, dep func(string) (string, error), mode resolveMode) (any, error) {
	switch r.Type {
	case "port":
		if mode == modeCaps {
			// Lengths only: the longest possible port string.
			return "65535", nil
		}
		return resolvePort(ctx, r)
	case "cidr":
		if mode == modeCaps {
			// Lengths only: the pool network with the slice mask has
			// the same string shape as any slice of it.
			_, poolNet, err := net.ParseCIDR(*r.Pool)
			if err != nil {
				return "", &FieldError{Field: "resources", Reason: fmt.Sprintf("cidr resource %q: invalid pool %q", r.Name, *r.Pool)}
			}
			return poolNet.IP.String() + "/" + strconv.Itoa(*r.Size), nil
		}
		return resolveCIDR(s, ctx, r)
	case "namespace", "state-path", "machine":
		return resolveTemplate(s, ctx, r, dep)
	}
	return nil, &FieldError{Field: "", Reason: fmt.Sprintf("unknown resource type %q", r.Type)}
}

// resolvePort derives the port from the band base: base + slot in stride
// form, base + slot×size + offset in group form (M2 §6.1).
func resolvePort(ctx Context, r *Resource) (int, error) {
	base, ok := ctx.Bases[r.Name]
	if !ok {
		return 0, &FieldError{
			Field:  "bases",
			Reason: fmt.Sprintf("no base supplied for port resource %q", r.Name),
		}
	}
	if base < 1 || base > 65535 {
		return 0, &FieldError{Field: "bases", Reason: fmt.Sprintf("base %d for port resource %q is not a port number (1..65535)", base, r.Name)}
	}
	var port int
	if portForm(r) == "group" {
		size := 1
		if r.Size != nil {
			size = *r.Size
		}
		offset := 0
		if r.Offset != nil {
			offset = *r.Offset
		}
		port = base + ctx.Slot*size + offset
	} else {
		port = base + ctx.Slot
	}
	if port < 1 || port > 65535 {
		return 0, &FieldError{
			Field:  "resources",
			Reason: fmt.Sprintf("port resource %q resolves to %d for slot %d, outside 1..65535", r.Name, port, ctx.Slot),
		}
	}
	return port, nil
}

// resolveCIDR slices the pool by slot: slot 1 takes the first block, each
// block is 2^(32-size) addresses (M3 §4.3). Exhaustion is an allocation
// concern with on_exhaustion semantics in phase 4, not a validation failure;
// this pure function refuses a slice outside the pool rather than inventing
// one.
func resolveCIDR(s *Spec, ctx Context, r *Resource) (string, error) {
	_, poolNet, err := net.ParseCIDR(*r.Pool)
	if err != nil {
		return "", &FieldError{Field: "resources", Reason: fmt.Sprintf("cidr resource %q: invalid pool %q", r.Name, *r.Pool)}
	}
	size := *r.Size
	block := uint64(1) << (32 - size)
	poolStart := ip4ToUint32(poolNet.IP)
	poolBits, _ := poolNet.Mask.Size()
	poolSize := uint64(1) << (32 - poolBits)
	offset := uint64(ctx.Slot-1) * block
	if offset+block > poolSize {
		return "", &FieldError{
			Field: "resources",
			Reason: fmt.Sprintf(
				"cidr resource %q: pool %s exhausted at slot %d (block %d of %d); on_exhaustion handling lands with the driver in phase 4",
				r.Name, *r.Pool, ctx.Slot, offset/block+1, poolSize/block),
		}
	}
	start := ip4FromUint32(poolStart + offset)
	value := start.String() + "/" + strconv.Itoa(size)
	if err := checkResolvedCap(s, r, value); err != nil {
		return "", err
	}
	return value, nil
}

func ip4ToUint32(ip net.IP) uint64 {
	ip = ip.To4()
	return uint64(ip[0])<<24 | uint64(ip[1])<<16 | uint64(ip[2])<<8 | uint64(ip[3])
}

func ip4FromUint32(v uint64) net.IP {
	return net.IPv4(byte(v>>24), byte(v>>16), byte(v>>8), byte(v))
}

// resolveTemplate substitutes one resource's template in dependency order.
// A dependency that fails to resolve (an exhausted cidr, say) is surfaced as
// that dependency's error, never misreported as an unknown variable.
func resolveTemplate(s *Spec, ctx Context, r *Resource, dep func(string) (string, error)) (string, error) {
	var depErr error
	value, err := substitute(*r.Template, func(name string) (string, bool) {
		switch name {
		case "app":
			return ctx.App, true
		case "slug":
			return ctx.Slug, true
		case "slot":
			return strconv.Itoa(ctx.Slot), true
		case "home":
			return ctx.Home, true
		case "worktree":
			return ctx.Worktree, true
		}
		v, err := dep(name)
		if err != nil {
			depErr = err
			return "", false
		}
		return v, true
	})
	if depErr != nil {
		return "", depErr
	}
	if err != nil {
		idx := resourceIndex(s, r.Name)
		return "", &FieldError{Field: resField(idx, "template"), Reason: err.Error()}
	}
	if err := checkResolvedCap(s, r, value); err != nil {
		return "", err
	}
	return value, nil
}

// checkResolvedCap validates a resolved name against its type's cap before
// anything downstream uses it.
func checkResolvedCap(s *Spec, r *Resource, value string) error {
	var capLen int
	switch r.Type {
	case "namespace":
		capLen = NamespaceMaxLen
	case "state-path":
		capLen = StatePathMaxLen
	case "cidr":
		capLen = CIDRMaxLen
	case "machine":
		capLen = MachineMaxLen
	default:
		return nil
	}
	if len(value) > capLen {
		idx := resourceIndex(s, r.Name)
		return &FieldError{
			Field:  resField(idx, "name"),
			Reason: fmt.Sprintf("resolved %s name %q is %d characters; cap is %d", r.Type, value, len(value), capLen),
		}
	}
	return nil
}
