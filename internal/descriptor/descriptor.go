// Package descriptor is the per-worktree allocation record: its type and its
// reader. Phase 5's emitter marshals the same type this package reads; this
// phase defines both halves of the contract and the reader only
// (05-delivery.md §2.3).
//
// The descriptor sits at the worktree root, undotted, in the format the
// spec's emit.descriptor declares, and carries the schema version, app, slug,
// slot, path, the standalone flag, a description, the resolved resources, the
// isolation state, the structured shared block of name-and-impact pairs, and
// an extras map round-tripped untouched. The view field of revision 1's
// 05-delivery.md §2.3 was deleted with view identity (plan.md §2) and does
// not exist here.
package descriptor

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"

	"github.com/goccy/go-yaml"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// Version is the only descriptor schema version this binary understands.
const Version = 1

// SupportedVersions is what a newer-version refusal names.
var SupportedVersions = []int{Version}

// Descriptor is the whole per-worktree allocation record.
type Descriptor struct {
	Version     int                      `yaml:"version" json:"version"`
	App         string                   `yaml:"app" json:"app"`
	Slug        string                   `yaml:"slug" json:"slug"`
	Slot        int                      `yaml:"slot" json:"slot"`
	Path        string                   `yaml:"path" json:"path"`
	Standalone  bool                     `yaml:"standalone" json:"standalone"`
	Description string                   `yaml:"description" json:"description"`
	Resources   map[string]spec.Resolved `yaml:"resources" json:"resources"`
	State       map[string]*Isolation    `yaml:"state" json:"state"`
	Shared      []Shared                 `yaml:"shared" json:"shared"`
	Extras      map[string]any           `yaml:"extras" json:"extras"`
}

// Isolation is one entry of the state block: the isolation decision for one
// resource, per 05-delivery.md §2.3's example (`db: {isolated: false}`). The
// pointer records presence, so `{isolated: false}` and `seeded: null` — the
// two shapes the example shows — remain distinguishable.
type Isolation struct {
	Isolated *bool `yaml:"isolated" json:"isolated"`
}

// Shared is one name-and-impact pair of the structured shared block
// (05-delivery.md §2.3): a resource that is not isolated, with its blast
// radius, readable by an agent directly from inside the worktree.
type Shared struct {
	Name   string `yaml:"name" json:"name"`
	Impact string `yaml:"impact" json:"impact"`
}

// VersionError is the refusal to read a descriptor whose schema version this
// binary does not understand (05-delivery.md §8): a newer version is refused
// naming the upgrade; any other unsupported version is refused as itself.
type VersionError struct {
	Found     int
	Supported []int
}

func (e *VersionError) Error() string {
	if e.Found > Version {
		return fmt.Sprintf("descriptor schema version %d is newer than this binary understands (supported versions: %v); upgrade wt, then re-run", e.Found, e.Supported)
	}
	return fmt.Sprintf("unsupported descriptor schema version %d (supported versions: %v)", e.Found, e.Supported)
}

// Read parses the descriptor at path, in the given format — yaml or json,
// selected by emit.descriptor.format in the spec, never sniffed. It refuses
// unknown fields, so a field from a newer schema version is refused whole
// rather than silently ignored.
//
// Two behaviours from 05-delivery.md §8 are load-bearing here: a descriptor
// whose schema version is newer than this binary understands is refused to be
// read, naming the upgrade, and a descriptor that will not parse is reported
// with `wt init` named as the rebuild (the caller's job — nothing in this
// phase writes, so the "never overwritten" half is free).
func Read(path, format string) (*Descriptor, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("reading %s: %w", path, err)
	}
	var d Descriptor
	switch format {
	case "yaml":
		err = yaml.UnmarshalWithOptions(data, &d, yaml.DisallowUnknownField())
	case "json":
		dec := json.NewDecoder(bytes.NewReader(data))
		dec.DisallowUnknownFields()
		err = dec.Decode(&d)
	default:
		return nil, fmt.Errorf("unknown descriptor format %q; supported formats: [yaml json]", format)
	}
	if err != nil {
		return nil, fmt.Errorf("parsing %s as %s: %w", path, format, err)
	}
	if d.Version == 0 {
		return nil, fmt.Errorf("parsing %s: version is required", path)
	}
	if d.Version != Version {
		return nil, &VersionError{Found: d.Version, Supported: SupportedVersions}
	}
	// A port resource's value must come back as the int the emitter wrote,
	// so both formats agree on the shape phase 5's consumers read. YAML and
	// JSON decode the bare number into different concrete types (uint64 for
	// goccy's any, float64 for encoding/json), so every numeric kind is
	// normalised.
	for name, r := range d.Resources {
		if r.Type != "port" {
			continue
		}
		switch v := r.Value.(type) {
		case int:
			// already the target shape
		case int64:
			r.Value = int(v)
		case uint64:
			r.Value = int(v)
		case float64:
			r.Value = int(v)
		}
		d.Resources[name] = r
	}
	return &d, nil
}
