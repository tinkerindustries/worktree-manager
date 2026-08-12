// Package store owns the coordinator's state directory: root resolution,
// the permission model and the atomic file primitives. Phase 3 builds the
// registry and the band ledger on top of these primitives; this phase adds
// clients.json, the client table the coordinator records every connection
// in (ARCHITECTURE.md §8.2).
//
// The rules are the coordinator model's: WT_HOME when set, else
// $HOME/.wt, read by wtd alone and never by a client — that is what stops
// it becoming the container mount point revision 2 deleted. Directory 0700,
// files 0600, written by the coordinator as the user. Atomic writes: temp
// file in the same directory, fsync the file, rename, fsync the directory —
// the same directory matters because a rename across filesystems is not
// atomic (02-coordination.md §9, 11.1). Every store file carries a
// schema_version field, and JSON is the format because slugs admit no, on,
// off, yes and y, which a YAML 1.1 parser turns into booleans
// (ARCHITECTURE.md §8.2).
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/mrgeoffrich/worktree-manager/internal/platform"
)

// SchemaVersion is the version of the store's file schema. Every store file
// carries it; a file written by a newer schema is refused rather than
// partially honoured (02-coordination.md §11).
const SchemaVersion = 1

// ClientsFileName is the client table's filename inside the store.
const ClientsFileName = "clients.json"

// Root resolves the store root: WT_HOME when set, otherwise $HOME/.wt.
// With both unset the coordinator stops — it never invents a location
// (02-coordination.md §14). WT_HOME is read by wtd alone and never by a
// client (ARCHITECTURE.md §8.2; the environment list in CLAUDE.md).
func Root() (string, error) {
	if h := os.Getenv("WT_HOME"); h != "" {
		return h, nil
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return "", errors.New("WT_HOME is not set and the home directory cannot be determined; refusing to invent a store location (set WT_HOME or $HOME)")
	}
	return filepath.Join(home, ".wt"), nil
}

// Versioned is the envelope every store file carries, so schema_version is
// structural rather than a habit. Embed it in every typed file.
type Versioned struct {
	SchemaVersion int `json:"schema_version"`
}

// VersionError is the refusal for a store file written by a newer schema:
// it may be listed, and it is never written back (02-coordination.md §11,
// phase-3 criterion).
type VersionError struct {
	Name    string
	File    int
	Current int
}

func (e *VersionError) Error() string {
	return fmt.Sprintf("%s carries schema version %d but this coordinator understands %d; upgrade wtd, then re-run", e.Name, e.File, e.Current)
}

// Store is an opened state directory.
type Store struct {
	root string
}

// Open resolves and prepares the store root: created 0700 (Windows: the
// ACL refusal — the equivalent ACL cannot be set with the standard library,
// and writing credentials world-readable is refused instead, 08-platform.md
// §4.6), probed writable, so an unwritable root fails here with a clear
// error naming the path and the ownership problem rather than at the end of
// a long operation.
func Open(root string) (*Store, error) {
	if err := platform.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	return &Store{root: root}, nil
}

// Root returns the store root path.
func (s *Store) Root() string { return s.root }

// Load reads name, checks its schema version and decodes it into v. A
// missing file reports os.ErrNotExist and the caller decides what an absent
// file means (an empty client table, for example).
func (s *Store) Load(name string, v any) error {
	path := filepath.Join(s.root, name)
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var env Versioned
	if err := json.Unmarshal(data, &env); err != nil {
		return fmt.Errorf("%s is not a readable store file: %w", name, err)
	}
	if env.SchemaVersion > SchemaVersion {
		return &VersionError{Name: name, File: env.SchemaVersion, Current: SchemaVersion}
	}
	if err := json.Unmarshal(data, v); err != nil {
		return fmt.Errorf("%s is not a readable store file: %w", name, err)
	}
	return nil
}

// Save encodes v and writes it atomically as a 0600 store file. The caller's
// type must embed Versioned with SchemaVersion set — Save refuses a file
// that would be written without its version field.
func (s *Store) Save(name string, v any) error {
	data, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	data = append(data, '\n')
	return s.WriteFile(name, data)
}

// WriteFile writes raw bytes atomically with 0600. The temp file sits in
// the same directory as the target, is fsynced, renamed over the target,
// and the directory is fsynced — the sequence that makes the rename itself
// durable (02-coordination.md §9).
func (s *Store) WriteFile(name string, data []byte) error {
	return AtomicWrite(filepath.Join(s.root, name), data, 0o600)
}

// AtomicWrite is the store's one write path: temp file in the same
// directory, fsync the file, rename, fsync the directory. Since phase 5 the
// sequence lives in internal/platform — the client-side emitters (the
// descriptor and the .env managed block) owe the same guarantee and a
// client never imports the store — and this is the delegate that keeps the
// two halves on one implementation.
func AtomicWrite(path string, data []byte, perm os.FileMode) error {
	return platform.AtomicWrite(path, data, perm)
}
