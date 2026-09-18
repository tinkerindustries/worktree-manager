package store

import (
	"context"
	"time"

	"github.com/tinkerindustries/worktree-manager/internal/spec"
)

// Tx is one store transaction. The coordinator composes a sequence of
// row-level mutations that must not half-apply — a teardown writing a
// state change plus its note and last-seen, a reclamation deleting a
// client and its entries — with WithTx, and each mutation runs against
// the transaction's connection, so the sequence commits or rolls back as
// one. Transactions go underneath the Handler mutex and the per-entry
// claims, never instead of either (PLAN-SCOPE.md, the mutex fence): the
// mutex guards sequences that span store calls and driver decisions,
// which no single transaction covers.
type Tx struct {
	q *Queries
}

// WithTx runs fn inside one database transaction, committing when it
// returns nil and rolling back otherwise. The database's schema version
// is checked before the transaction starts — a newer database is never
// written, in a transaction or out of one.
func (s *Store) WithTx(fn func(*Tx) error) error {
	if err := s.checkSchemaVersion(); err != nil {
		return err
	}
	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	t := &Tx{q: New(tx)}
	if err := fn(t); err != nil {
		tx.Rollback()
		return err
	}
	return tx.Commit()
}

// UpsertEntry inserts or replaces one entry inside the transaction.
func (t *Tx) UpsertEntry(e Entry) error { return execUpsertEntry(t.q, e) }

// DeleteEntry removes one entry inside the transaction.
func (t *Tx) DeleteEntry(app, slug string) error {
	return t.q.DeleteEntry(context.Background(), DeleteEntryParams{App: app, Slug: slug})
}

// UpdateEntryState sets one entry's state and teardown note inside the
// transaction.
func (t *Tx) UpdateEntryState(app, slug, state, note string) error {
	return t.q.UpdateEntryState(context.Background(), UpdateEntryStateParams{
		App: app, Slug: slug, State: state, TeardownNote: nullString(note),
	})
}

// TouchEntry moves one entry's LastSeen inside the transaction.
func (t *Tx) TouchEntry(app, slug string, ts time.Time) error {
	return t.q.TouchEntry(context.Background(), TouchEntryParams{
		App: app, Slug: slug, LastSeen: ts.UTC().Format(time.RFC3339Nano),
	})
}

// UpsertBand replaces one app's band inside the transaction.
func (t *Tx) UpsertBand(b Band) error { return t.upsertBand(b) }

// UpsertClient inserts or refreshes one client row inside the
// transaction.
func (t *Tx) UpsertClient(c ClientEntry) error { return execUpsertClient(t.q, c) }

// DeleteClient removes one client row inside the transaction.
func (t *Tx) DeleteClient(identity, kind string) error {
	return t.q.DeleteClient(context.Background(), DeleteClientParams{Identity: identity, Kind: kind})
}

// UpsertSpec records one app's spec inside the transaction.
func (t *Tx) UpsertSpec(app string, sp spec.Spec) error { return execUpsertSpec(t.q, app, sp) }

// DeleteSpec removes one app's cached spec inside the transaction.
func (t *Tx) DeleteSpec(app string) error {
	return t.q.DeleteSpec(context.Background(), app)
}
