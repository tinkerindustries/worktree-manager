package store

import (
	"errors"
	"os"
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

// ClientsFile is clients.json.
type ClientsFile struct {
	Versioned
	Clients []ClientEntry `json:"clients"`
}

// ReadClients loads the client table; a missing file is an empty table —
// the first connection writes it.
func (s *Store) ReadClients() (ClientsFile, error) {
	var f ClientsFile
	if err := s.Load(ClientsFileName, &f); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			f = ClientsFile{Versioned: Versioned{SchemaVersion: SchemaVersion}}
			return f, nil
		}
		return ClientsFile{}, err
	}
	return f, nil
}

// WriteClients persists the client table atomically.
func (s *Store) WriteClients(f ClientsFile) error {
	f.Versioned = Versioned{SchemaVersion: SchemaVersion}
	return s.Save(ClientsFileName, &f)
}
