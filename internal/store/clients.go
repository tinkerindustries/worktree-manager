package store

import (
	"context"
)

// ClientEntry is one row of the client table (ARCHITECTURE.md §8.2,
// §10.2): the identity the coordinator assigned or verified, its kind, the
// coordinator's own measurement of when it was last seen — never a
// timestamp a client wrote — and the ephemeral flag that marks its entries
// reclaimable (phase 6 owns reclamation).
type ClientEntry struct {
	Identity  string `json:"identity"`
	Kind      string `json:"kind"`
	LastSeen  string `json:"last_seen"` // RFC3339 with fractional seconds, measured by the coordinator
	Ephemeral bool   `json:"ephemeral"`
}

// ClientsFile is the whole client table. SchemaVersion is the database's
// own meta.schema_version.
type ClientsFile struct {
	Versioned
	Clients []ClientEntry `json:"clients"`
}

// ReadClients loads the client table; a fresh store is an empty table —
// the first connection writes it.
func (s *Store) ReadClients() (ClientsFile, error) {
	v, err := s.metaVersion()
	if err != nil {
		return ClientsFile{}, err
	}
	if v > SchemaVersion {
		return ClientsFile{}, &VersionError{Name: DBFileName, File: v, Current: SchemaVersion}
	}
	rows, err := New(s.db).ListClients(context.Background())
	if err != nil {
		return ClientsFile{}, err
	}
	f := ClientsFile{Versioned: Versioned{SchemaVersion: v}, Clients: make([]ClientEntry, 0, len(rows))}
	for _, r := range rows {
		f.Clients = append(f.Clients, ClientEntry{
			Identity: r.Identity, Kind: r.Kind, LastSeen: r.LastSeen, Ephemeral: r.Ephemeral != 0,
		})
	}
	return f, nil
}

// UpsertClient inserts or refreshes one client row. The (identity, kind)
// key is the primary key, so reconnection updates the row in place —
// observe's read-modify-write of the whole table becomes this one
// statement.
func (s *Store) UpsertClient(c ClientEntry) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return execUpsertClient(New(s.db), c)
}

// upsertClient is the shared body of the store-level and
// transaction-level upserts.
func execUpsertClient(q *Queries, c ClientEntry) error {
	return q.UpsertClient(context.Background(), UpsertClientParams{
		Identity: c.Identity, Kind: c.Kind, LastSeen: c.LastSeen, Ephemeral: boolInt(c.Ephemeral),
	})
}

// DeleteClient removes one client row.
func (s *Store) DeleteClient(identity, kind string) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	return New(s.db).DeleteClient(context.Background(), DeleteClientParams{Identity: identity, Kind: kind})
}
