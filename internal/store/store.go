// Package store owns the coordinator's state: root resolution, the
// permission model and the SQLite database at <root>/wt.db that holds the
// registry, the band ledger, the client table and the per-app spec cache.
//
// The rules are the coordinator model's: WT_HOME when set, else
// $HOME/.wt, read by wtd alone and never by a client — that is what stops
// it becoming the container mount point revision 2 deleted. Directory
// 0700, database and WAL/SHM sidecars 0600, written by the coordinator as
// the user. The database opens with WAL journaling, a 5s busy timeout,
// foreign keys on and synchronous=FULL, under umask 077 so the sidecars
// the driver creates are born 0600 — entry secrets live in the database,
// so their modes are verified after open, never assumed.
//
// schema_version lives in the meta table. A database whose schema_version
// is newer than this build is refused, naming the upgrade — the same rule
// the per-file envelope carried (02-coordination.md §11); the one lenient
// read, ReadRegistryList, still lists a newer database and never writes
// it back. There is no migration: the four JSON files of phases 0–9 are
// never read, and wtd logs one startup warning naming registry.json when
// it is present.
package store

import (
	"context"
	"database/sql"
	_ "embed"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/tinkerindustries/worktree-manager/internal/platform"
	_ "modernc.org/sqlite"
)

// SchemaVersion is the version of the store database's schema. A database
// whose meta.schema_version is newer is refused rather than partially
// honoured (02-coordination.md §11).
const SchemaVersion = 1

// DBFileName is the coordinator's database file inside the store.
const DBFileName = "wt.db"

// The four phase-0-to-9 store files. They are no longer read — the clean
// break (PLAN-SCOPE.md non-goal 1) — and the names survive only so wtd's
// startup warning can name registry.json.
const (
	RegistryFileName = "registry.json"
	BandsFileName    = "bands.json"
	ClientsFileName  = "clients.json"
	SpecsFileName    = "specs.json"
)

// metaSchemaVersion is the meta key holding the database's schema version.
const metaSchemaVersion = "schema_version"

// schemaSQL is the committed schema, the single source of the table
// shapes; applied idempotently at open.
//
//go:embed schema.sql
var schemaSQL string

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

// Versioned is the schema-version envelope the whole-collection reads
// carry, so a caller can see which schema version the rows it just read
// came from. In the database the version lives in the meta table; the
// reads stamp it onto the returned file structs, exactly as the JSON
// envelope did.
type Versioned struct {
	SchemaVersion int `json:"schema_version"`
}

// VersionError is the refusal for a store database written by a newer
// schema: it may be listed (via ReadRegistryList), and it is never
// written back (02-coordination.md §11).
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
	db   *sql.DB
}

// Open resolves and prepares the store root and opens (or creates) the
// database at <root>/wt.db. The root is created 0700 (Windows: the ACL
// refusal — the equivalent ACL cannot be set with the standard library,
// and writing credentials world-readable is refused instead, 08-platform.md
// §4.6) and probed writable, so an unwritable root fails here with a
// clear error naming the path and the ownership problem rather than at
// the end of a long operation.
//
// The database is opened under umask 077 — the SQLite driver creates
// wt.db and its WAL and SHM sidecars at the process umask, and entry
// secrets live in them — and the three modes are verified afterwards,
// refused with a clear error when any of them is not 0600. A database
// that cannot be opened is refused with the same reasoning today's
// unparseable registry carried: the registry is the only source of
// repository locations on the machine, so rebuild-from-descriptors cannot
// be safe.
func Open(root string) (*Store, error) {
	if err := platform.EnsurePrivateDir(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, DBFileName)
	db, err := openDB(path)
	if err != nil {
		return nil, fmt.Errorf("the store database at %s cannot be opened: %w — the registry is the only source of repository locations on this machine, so rebuilding it from descriptors cannot be safe; check that the file is a worktree-manager database and that it is readable, then re-run", path, err)
	}
	if err := platform.VerifyPrivateFileModes(path, path+"-wal", path+"-shm"); err != nil {
		db.Close()
		return nil, err
	}
	return &Store{root: root, db: db}, nil
}

// openDB opens the database and initialises it, both inside
// platform.WithPrivateUmask: the driver creates wt.db and its WAL and SHM
// sidecars at the process umask, so the flip must cover the open itself.
func openDB(path string) (*sql.DB, error) {
	var db *sql.DB
	var err error
	if werr := platform.WithPrivateUmask(func() error {
		db, err = sql.Open("sqlite", dsn(path))
		if err != nil {
			return err
		}
		return initDB(db)
	}); werr != nil {
		if db != nil {
			db.Close()
		}
		return nil, werr
	}
	return db, nil
}

// dsn is the driver DSN: WAL journaling, busy_timeout 5000,
// foreign_keys on and synchronous=FULL, the four pragmas the store opens
// under. The path is URL-escaped because SQLite parses the DSN as a URI
// (a store root whose path contains '?' or '#' must still open).
func dsn(path string) string {
	return "file:" + url.PathEscape(filepath.ToSlash(path)) +
		"?_pragma=journal_mode(WAL)&_pragma=busy_timeout(5000)&_pragma=foreign_keys(1)&_pragma=synchronous(FULL)"
}

// initDB applies the schema and seeds meta.schema_version. The committed
// schema (schema.sql) is written with plain CREATE TABLE statements — it
// is the codegen input and the design of record — so applying it to an
// existing database trips the "already exists" refusal for every table
// and index it already holds; that refusal IS the idempotence here. An
// existing database is never altered: there is no migration, and a
// database from a newer schema keeps whatever columns it has, refused at
// the read path rather than downgraded here.
func initDB(db *sql.DB) error {
	for _, stmt := range schemaStatements() {
		if _, err := db.Exec(stmt); err != nil {
			if strings.Contains(err.Error(), "already exists") {
				continue
			}
			return err
		}
	}
	_, err := db.Exec("INSERT OR IGNORE INTO meta (key, value) VALUES (?, ?)", metaSchemaVersion, SchemaVersion)
	return err
}

// schemaStatements splits the committed schema into its statements. The
// schema holds no string literals, so splitting on the ";\n" terminator
// is exact.
func schemaStatements() []string {
	var out []string
	for _, s := range strings.Split(schemaSQL, ";\n") {
		if strings.TrimSpace(s) == "" {
			continue
		}
		out = append(out, s)
	}
	return out
}

// Root returns the store root path.
func (s *Store) Root() string { return s.root }

// Close releases the database handle and its WAL and SHM sidecars. The
// coordinator owns one store for its whole life, so in production this
// runs once at shutdown; the reason it exists at all is that a handle
// left open is a leak on every platform, and on Windows an open file
// cannot be deleted — a store not closed is a store whose directory
// cannot be removed. Close is idempotent.
func (s *Store) Close() error {
	if s.db == nil {
		return nil
	}
	db := s.db
	s.db = nil
	return db.Close()
}

// metaVersion reads the database's own schema version from the meta
// table.
func (s *Store) metaVersion() (int, error) {
	v, err := New(s.db).GetMeta(context.Background(), metaSchemaVersion)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return 0, fmt.Errorf("the store database at %s carries no schema_version row — it is not a worktree-manager database (or its meta table was edited); move wt.db aside and restart wtd, which creates a fresh one", filepath.Join(s.root, DBFileName))
		}
		return 0, err
	}
	n, err := strconv.Atoi(v)
	if err != nil {
		return 0, fmt.Errorf("the store database's schema_version %q is not a number: %w", v, err)
	}
	return n, nil
}

// checkSchemaVersion refuses a database whose meta.schema_version is
// newer than this build understands, naming the upgrade. Every strict
// read and every mutation calls it; ReadRegistryList is the one lenient
// path.
func (s *Store) checkSchemaVersion() error {
	v, err := s.metaVersion()
	if err != nil {
		return err
	}
	if v > SchemaVersion {
		return &VersionError{Name: DBFileName, File: v, Current: SchemaVersion}
	}
	return nil
}
