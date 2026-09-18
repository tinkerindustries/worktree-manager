package descriptor

// write.go is the write side of the descriptor: phase 5's emitter, on the
// same type phase 1's reader parses (05-delivery.md §2). Nothing here is a
// verb — init (phase 5's other half) sequences these functions; this file
// only marshals the allocation into a file.
//
// The descriptor is always written, whatever else a repo chooses
// (05-delivery.md §1), at the worktree root, undotted, in the format
// emit.descriptor declares, and atomically — temp file in the same
// directory, fsync, rename — for the same reason as the registry
// (ARCHITECTURE.md §8.5).

import (
	"encoding/json"
	"fmt"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// Write marshals d in the given format — yaml or json, selected by
// emit.descriptor.format in the spec, never sniffed — and writes it
// atomically at path. YAML emission quotes every string scalar via
// spec.EmitYAML, so a worktree slugged `no`, `on`, `off`, `yes` or `y`
// round-trips as the string, never as a YAML 1.1 boolean
// (ARCHITECTURE.md §8.2).
//
// The descriptor is per-worktree, per-machine state; like the store files
// it is written 0600.
func Write(path, format string, d *Descriptor) error {
	var data []byte
	var err error
	switch format {
	case "yaml":
		data, err = spec.EmitYAML(d)
	case "json":
		data, err = json.MarshalIndent(d, "", "  ")
		if err == nil {
			data = append(data, '\n')
		}
	default:
		return fmt.Errorf("unknown descriptor format %q; supported formats: [yaml json]", format)
	}
	if err != nil {
		return fmt.Errorf("emitting the descriptor for %s: %w", path, err)
	}
	if err := platform.AtomicWrite(path, data, 0o600); err != nil {
		return fmt.Errorf("writing the descriptor: %w", err)
	}
	return nil
}

// BuildShared computes the descriptor's shared block, both halves
// (03-drivers.md §6, ARCHITECTURE.md §8.5):
//
//   - the generated half: every resource with `default: shared` contributes
//     itself, with its resolved path and its impact text. The impact text is
//     the driver's blast-radius prose, supplied by the caller — in the
//     running system that is the coordinator, which is the only side that
//     imports the drivers; the client-side emitter copies the block from
//     the allocation result.
//   - the hand-authored half: the spec's `shared:` section, whose names are
//     templates resolved with spec.Substitute against the same context and
//     resolved table as the resources.
//
// Hand-listing the generated half would drift the moment a default changed,
// and a stale shared block is worse than none because it is believed — so a
// shared resource with no resolved value, or an impact text that came back
// empty, is an error rather than a silent omission.
func BuildShared(s *spec.Spec, ctx spec.Context, resolved map[string]spec.Resolved, impact func(r *spec.Resource) string) ([]Shared, error) {
	if ctx.App == "" {
		ctx.App = s.App
	}
	var out []Shared

	for i := range s.Resources {
		r := &s.Resources[i]
		if r.Default == nil || *r.Default != "shared" {
			continue
		}
		v, ok := resolved[r.Name]
		if !ok {
			return nil, fmt.Errorf("shared block: resource %q has default: shared but no resolved value", r.Name)
		}
		text := ""
		if impact != nil {
			text = impact(r)
		}
		if strings.TrimSpace(text) == "" {
			return nil, fmt.Errorf("shared block: resource %q has default: shared but no impact text was supplied", r.Name)
		}
		out = append(out, Shared{Name: fmt.Sprint(v.Value), Impact: text})
	}

	for i := range s.Shared {
		name, err := spec.Substitute(s.Shared[i].Name, ctx, resolved)
		if err != nil {
			return nil, fmt.Errorf("shared block: resolving shared[%d].name: %w", i, err)
		}
		out = append(out, Shared{Name: name, Impact: s.Shared[i].Impact})
	}
	return out, nil
}

// BuildState computes the descriptor's isolation state block
// (05-delivery.md §2.3): one entry per state-path resource recording its
// isolation decision. The decision is the spec's: `default: shared` is not
// isolated, everything else is. The seeding markers (when a state-path
// driver seeded the path) are phase 5's other half's job — init owns the
// ordering — this function only carries the spec's isolation into the
// record.
func BuildState(s *spec.Spec) map[string]*Isolation {
	state := make(map[string]*Isolation)
	for i := range s.Resources {
		r := &s.Resources[i]
		if r.Type != "state-path" {
			continue
		}
		isolated := true
		if r.Default != nil && *r.Default == "shared" {
			isolated = false
		}
		state[r.Name] = &Isolation{Isolated: &isolated}
	}
	if len(state) == 0 {
		return map[string]*Isolation{}
	}
	return state
}
