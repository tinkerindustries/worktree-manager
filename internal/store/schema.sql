CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);

CREATE TABLE entries (
  app TEXT NOT NULL, slug TEXT NOT NULL, slot INTEGER NOT NULL,
  owner TEXT NOT NULL, owner_kind TEXT NOT NULL, ephemeral INTEGER NOT NULL,
  path TEXT NOT NULL, path_visible INTEGER NOT NULL,
  state TEXT NOT NULL, teardown_note TEXT,
  resources TEXT NOT NULL,        -- JSON
  secrets TEXT,                   -- JSON
  descriptor_path TEXT, description TEXT,
  created_at TEXT NOT NULL, last_seen TEXT NOT NULL,
  PRIMARY KEY (app, slug)
);
CREATE UNIQUE INDEX entries_slot ON entries (app, slot);

CREATE TABLE bands (app TEXT NOT NULL, resource TEXT NOT NULL,
                    base INTEGER NOT NULL, span INTEGER NOT NULL,
                    PRIMARY KEY (app, resource));
CREATE TABLE reservations (id INTEGER PRIMARY KEY, note TEXT NOT NULL);
CREATE TABLE reservation_ports (reservation_id INTEGER NOT NULL, port INTEGER NOT NULL);
CREATE TABLE reservation_names (reservation_id INTEGER NOT NULL, name TEXT NOT NULL);
CREATE TABLE clients (identity TEXT NOT NULL, kind TEXT NOT NULL,
                      last_seen TEXT NOT NULL, ephemeral INTEGER NOT NULL,
                      PRIMARY KEY (identity, kind));
CREATE TABLE specs (app TEXT PRIMARY KEY, spec TEXT NOT NULL);
