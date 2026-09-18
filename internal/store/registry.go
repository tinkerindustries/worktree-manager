package store

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

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
	// it drives reclamation (phase 6).
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
	// (and doctor) knows what is outstanding without re-deriving it. Cleared
	// when a later teardown frees the entry.
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
	// staleness and reclamation.
	CreatedAt string `json:"created_at"`
	LastSeen  string `json:"last_seen"`
}

// RegistryFile is the whole registry: every entry across every repo. The
// reads build it from the entries table; SchemaVersion is the database's
// own meta.schema_version, so a caller can see that the rows came from a
// newer schema.
type RegistryFile struct {
	Versioned
	Entries []Entry `json:"entries"`
}

// ReadRegistry loads the registry for mutation. A database written by a
// newer schema version is refused naming the upgrade — writing it back
// would be a silent downgrade (02-coordination.md §11). A missing database
// was created and initialised at open, so an empty registry is a fresh
// store.
func (s *Store) ReadRegistry() (RegistryFile, error) {
	return s.readRegistry(true)
}

// ReadRegistryList loads the registry for reading: "a database from a
// newer schema is still read well enough to list" — the lenient read. The
// schema version check of the strict reads is skipped, and the returned
// file carries the database's own schema_version, so the caller can see
// that it is newer and refuse to write it back. A database that cannot be
// read at all is reported, never truncated and recreated.
func (s *Store) ReadRegistryList() (RegistryFile, error) {
	return s.readRegistry(false)
}

// readRegistry is both registry reads. strict refuses a database from a
// newer schema; the lenient read lists it anyway.
func (s *Store) readRegistry(strict bool) (RegistryFile, error) {
	v, err := s.metaVersion()
	if err != nil {
		return RegistryFile{}, err
	}
	if strict && v > SchemaVersion {
		return RegistryFile{}, &VersionError{Name: DBFileName, File: v, Current: SchemaVersion}
	}
	rows, err := New(s.db).ListEntries(context.Background())
	if err != nil {
		return RegistryFile{}, err
	}
	f := RegistryFile{Versioned: Versioned{SchemaVersion: v}, Entries: make([]Entry, 0, len(rows))}
	for _, r := range rows {
		e, err := entryFromRow(r)
		if err != nil {
			return RegistryFile{}, fmt.Errorf("the store database holds an unreadable entry %s/%s: %w", r.App, r.Slug, err)
		}
		f.Entries = append(f.Entries, e)
	}
	return f, nil
}

// entryFromRow decodes one entries row into the registry type. resources
// and secrets are JSON columns; the Resolved decoder normalises port
// values to int, the same rule the descriptor reader applies.
func entryFromRow(r EntryRow) (Entry, error) {
	e := Entry{
		App: r.App, Slug: r.Slug, Slot: int(r.Slot),
		Owner: r.Owner, OwnerKind: r.OwnerKind, Ephemeral: r.Ephemeral != 0,
		Path: r.Path, PathVisible: r.PathVisible != 0,
		State: r.State, TeardownNote: r.TeardownNote.String,
		DescriptorPath: r.DescriptorPath.String, Description: r.Description.String,
		CreatedAt: r.CreatedAt, LastSeen: r.LastSeen,
	}
	if err := json.Unmarshal([]byte(r.Resources), &e.Resources); err != nil {
		return Entry{}, fmt.Errorf("the resources column is not readable JSON: %w", err)
	}
	if r.Secrets.Valid {
		if err := json.Unmarshal([]byte(r.Secrets.String), &e.Secrets); err != nil {
			return Entry{}, fmt.Errorf("the secrets column is not readable JSON: %w", err)
		}
	}
	return e, nil
}

// GetEntry loads one entry by app and slug; ok is false when no entry
// exists. The row-level replacement for the read-then-scan lookup the
// whole-file registry forced on every single-entry operation.
func (s *Store) GetEntry(app, slug string) (*Entry, bool, error) {
	if err := s.checkSchemaVersion(); err != nil {
		return nil, false, err
	}
	row, err := New(s.db).GetEntry(context.Background(), GetEntryParams{App: app, Slug: slug})
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return nil, false, nil
		}
		return nil, false, err
	}
	e, err := entryFromRow(row)
	if err != nil {
		return nil, false, err
	}
	return &e, true, nil
}

// UpsertEntry inserts or replaces one entry, keyed (app, slug). The
// database's UNIQUE (app, slot) index is a second, structural guarantee
// underneath the coordinator's own mutex: a duplicate slot is refused
// here even with no mutex involved.
func (s *Store) UpsertEntry(e Entry) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return execUpsertEntry(New(s.db), e)
}

// upsertEntry is the shared body of the store-level and transaction-level
// upserts.
func execUpsertEntry(q *Queries, e Entry) error {
	resources, err := json.Marshal(e.Resources)
	if err != nil {
		return fmt.Errorf("encoding the entry's resources: %w", err)
	}
	var secrets sql.NullString
	if e.Secrets != nil {
		data, err := json.Marshal(e.Secrets)
		if err != nil {
			return fmt.Errorf("encoding the entry's secrets: %w", err)
		}
		secrets = sql.NullString{String: string(data), Valid: true}
	}
	return q.UpsertEntry(context.Background(), UpsertEntryParams{
		App: e.App, Slug: e.Slug, Slot: int64(e.Slot),
		Owner: e.Owner, OwnerKind: e.OwnerKind, Ephemeral: boolInt(e.Ephemeral),
		Path: e.Path, PathVisible: boolInt(e.PathVisible),
		State: e.State, TeardownNote: nullString(e.TeardownNote),
		Resources:      string(resources),
		Secrets:        secrets,
		DescriptorPath: nullString(e.DescriptorPath),
		Description:    nullString(e.Description),
		CreatedAt:      e.CreatedAt, LastSeen: e.LastSeen,
	})
}

// DeleteEntry removes one entry, freeing its slot. Deleting an entry that
// does not exist is not an error — the caller has already looked it up,
// and teardown is the sequence that decides what an absent entry means.
func (s *Store) DeleteEntry(app, slug string) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return New(s.db).DeleteEntry(context.Background(), DeleteEntryParams{App: app, Slug: slug})
}

// UpdateEntryState sets one entry's state and teardown note. The state
// transition and the note must land together — a tearing-down entry with
// no note would leave what survived unnamed — which is why the pair is
// one statement, and why callers that also move LastSeen wrap the two
// calls in WithTx.
func (s *Store) UpdateEntryState(app, slug, state, note string) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return New(s.db).UpdateEntryState(context.Background(), UpdateEntryStateParams{
		App: app, Slug: slug, State: state, TeardownNote: nullString(note),
	})
}

// TouchEntry moves one entry's LastSeen to t, the coordinator's own
// clock.
func (s *Store) TouchEntry(app, slug string, t time.Time) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return New(s.db).TouchEntry(context.Background(), TouchEntryParams{
		App: app, Slug: slug, LastSeen: t.UTC().Format(time.RFC3339Nano),
	})
}

// boolInt is the INTEGER encoding of a bool column.
func boolInt(b bool) int64 {
	if b {
		return 1
	}
	return 0
}

// nullString encodes an optional TEXT column; "" is stored as NULL, which
// reads back as "" — the JSON file's omitempty behaved the same way.
func nullString(s string) sql.NullString {
	return sql.NullString{String: s, Valid: s != ""}
}
