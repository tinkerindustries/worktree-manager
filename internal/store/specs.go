package store

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// SpecsFile is the coordinator's per-app spec cache: one spec per app,
// the latest valid one the coordinator saw on an allocate call. The
// client sends the spec with every allocate call (ARCHITECTURE.md §8.1),
// and the coordinator records it so that reclamation — coordinator-side
// teardown of a dead ephemeral client's entries — can run without any
// client around to supply it: a container's entry path is not visible
// from the host, so the walk-up spec lookup cannot reach it, and the
// cached spec is what the teardown computes dependent projects and purge
// refusals from. The cache is a convenience, never an authority: every
// client-initiated call still sends its own spec, and the cache is only
// read when no spec can be found any other way. SchemaVersion is the
// database's own meta.schema_version.
type SpecsFile struct {
	Versioned
	Specs map[string]spec.Spec `json:"specs"`
}

// ReadSpecs loads the spec cache; a fresh store is an empty cache.
func (s *Store) ReadSpecs() (SpecsFile, error) {
	v, err := s.metaVersion()
	if err != nil {
		return SpecsFile{}, err
	}
	if v > SchemaVersion {
		return SpecsFile{}, &VersionError{Name: DBFileName, File: v, Current: SchemaVersion}
	}
	rows, err := New(s.db).ListSpecs(context.Background())
	if err != nil {
		return SpecsFile{}, err
	}
	f := SpecsFile{Versioned: Versioned{SchemaVersion: v}, Specs: map[string]spec.Spec{}}
	for _, r := range rows {
		var sp spec.Spec
		if err := json.Unmarshal([]byte(r.Spec), &sp); err != nil {
			return SpecsFile{}, fmt.Errorf("the spec cache holds an unreadable spec for app %q: %w", r.App, err)
		}
		f.Specs[r.App] = sp
	}
	return f, nil
}

// UpsertSpec records the latest valid spec the coordinator saw for one
// app. Best-effort at the call site — a cache write failure is logged,
// never a refusal — but the write itself is one statement.
func (s *Store) UpsertSpec(app string, sp spec.Spec) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return execUpsertSpec(New(s.db), app, sp)
}

// upsertSpec is the shared body of the store-level and transaction-level
// upserts.
func execUpsertSpec(q *Queries, app string, sp spec.Spec) error {
	data, err := json.Marshal(sp)
	if err != nil {
		return fmt.Errorf("encoding the spec of app %q: %w", app, err)
	}
	return q.UpsertSpec(context.Background(), UpsertSpecParams{App: app, Spec: string(data)})
}

// DeleteSpec removes one app's cached spec.
func (s *Store) DeleteSpec(app string) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return New(s.db).DeleteSpec(context.Background(), app)
}
