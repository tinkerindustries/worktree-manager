package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/spec"
)

// RegistryFileName is the registry's filename inside the store: one file for
// every entry across every repo, so a multi-entry operation stays atomic
// under a single rename (02-coordination.md §2 — allocations happen a few
// times a day, and the write rate does not justify finer contention).
const RegistryFileName = "registry.json"

// Entry states, per ARCHITECTURE.md §11.2.
const (
	// StateReserving means the slot is claimed and the resources are not yet
	// materialised; the coordinator ages a reserving entry out on its own
	// timer, which also covers a client that died mid-sequence.
	StateReserving = "reserving"
	// StateActive means fully materialised.
	StateActive = "active"
	// StateTearingDown is a resting state, not a transient one: teardown
	// started and something survived, so the slot stays held and the entry
	// keeps a note listing what survived (phase 4's teardown writes that
	// note).
	StateTearingDown = "tearing-down"
)

// Entry is one row of the registry (ARCHITECTURE.md §8.3). The table in
// that section supersedes 02-coordination.md §4 — there is no view field;
// Owner, Ephemeral and PathVisible replaced view identity (revision 2,
// plan.md §2).
//
// Resources is denormalised deliberately: a cross-repo list cannot depend on
// each repo's spec being readable, so the registry is self-describing.
// Secrets is a named field rather than a free-form section so that redaction
// is structural — a field the code knows to be a secret cannot be served by
// a response encoder written before that secret existed.
type Entry struct {
	App  string `json:"app"`
	Slug string `json:"slug"`
	Slot int    `json:"slot"`
	// Owner is the client identity that created the entry and OwnerKind its
	// kind — together they name a client, and the authorisation check reads
	// them (ARCHITECTURE.md §4.3, §8.6 rule 6). The refusal message names
	// both, which is why the kind is recorded alongside the key.
	Owner     string `json:"owner"`
	OwnerKind string `json:"owner_kind"`
	// Ephemeral is set when the creating client declared itself disposable;
	// it drives reclamation, which is phase 6's work — this phase records it.
	Ephemeral bool `json:"ephemeral"`
	// Path is the path as the creating client saw it — informational only
	// (ARCHITECTURE.md §8.6 rule 4: resolution never reads it, so a worktree
	// can be moved). PathVisible records whether the coordinator can stat
	// that path; a path that exists only inside a container is recorded,
	// never checked, and never counted as stale.
	Path        string `json:"path"`
	PathVisible bool   `json:"path_visible"`
	State       string `json:"state"`
	// TeardownNote is written when a teardown leaves resources behind and
	// the entry moves to tearing-down: exactly what survived, so a re-run
	// (and phase 6's doctor) knows what is outstanding without re-deriving
	// it. Cleared when a later teardown frees the entry.
	TeardownNote string `json:"teardown_note,omitempty"`
	// Resources are the derived values, denormalised (ARCHITECTURE.md §8.3).
	Resources map[string]spec.Resolved `json:"resources"`
	// DescriptorPath is where the descriptor went and Description what the
	// worktree is for in ten words or fewer.
	DescriptorPath string `json:"descriptor_path,omitempty"`
	Description    string `json:"description,omitempty"`
	// Secrets are seed credentials, served to the owning client alone and
	// redacted for everyone else (ARCHITECTURE.md §12.2).
	Secrets map[string]string `json:"secrets,omitempty"`
	// CreatedAt and LastSeen are the coordinator's own clock, RFC3339 with
	// fractional seconds; LastSeen moves on every owner mutation. They feed
	// staleness, reclamation and migration.
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
	Versioned
}

// RegistryFile is registry.json.
type RegistryFile struct {
	Versioned
	Entries []Entry `json:"entries"`
}

// ReadRegistry loads the registry for mutation. A file written by a newer
// schema version is refused naming the upgrade — writing it back would be a
// silent downgrade (02-coordination.md §11). A missing file is an empty
// registry.
func (s *Store) ReadRegistry() (RegistryFile, error) {
	return loadFile(s, RegistryFileName, func() RegistryFile {
		return RegistryFile{Versioned: Versioned{SchemaVersion: SchemaVersion}}
	})
}

// ReadRegistryList loads the registry for reading: "a registry written by a
// newer schema version is read well enough to list" (task, §6). The strict
// version check of Load is skipped — unknown fields are dropped by the
// decoder — and the returned file carries the file's own schema_version, so
// the caller can see that it is newer and refuse to write it back. An
// unparseable registry is reported, never truncated and recreated.
func (s *Store) ReadRegistryList() (RegistryFile, error) {
	var f RegistryFile
	path := filepath.Join(s.root, RegistryFileName)
	data, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f = RegistryFile{Versioned: Versioned{SchemaVersion: SchemaVersion}}
			return f, nil
		}
		return RegistryFile{}, err
	}
	if err := json.Unmarshal(data, &f); err != nil {
		return RegistryFile{}, fmt.Errorf("%s is not a readable store file: %w", RegistryFileName, err)
	}
	return f, nil
}

// WriteRegistry persists the registry atomically. A file whose own
// schema_version claims a newer schema is refused rather than written back —
// the "lists but does not write" rail, structural at the write path
// (02-coordination.md §11).
func (s *Store) WriteRegistry(f RegistryFile) error {
	if f.SchemaVersion > SchemaVersion {
		return &VersionError{Name: RegistryFileName, File: f.SchemaVersion, Current: SchemaVersion}
	}
	f.Versioned = Versioned{SchemaVersion: SchemaVersion}
	// Every entry carries its own schema_version too (ARCHITECTURE.md §8.3:
	// created_at, last_seen, schema_version — staleness, reclamation,
	// migration).
	for i := range f.Entries {
		f.Entries[i].Versioned = Versioned{SchemaVersion: SchemaVersion}
	}
	return s.Save(RegistryFileName, &f)
}
