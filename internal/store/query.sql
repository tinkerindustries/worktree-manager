-- Query file for the coordinator's SQLite store. The generated Go is
-- committed and sqlc is a build-time tool (go.mod `tool` directive); the
-- wrappers in this package convert between these rows and the public store
-- types (Entry, Band, ClientEntry, ...). One writer serialises in the
-- coordinator (the Handler mutex); these are the statements underneath it.

-- name: GetMeta :one
SELECT value FROM meta WHERE key = ?;

-- name: SetMeta :exec
INSERT INTO meta (key, value) VALUES (?, ?)
ON CONFLICT(key) DO UPDATE SET value = excluded.value;

-- name: ListEntries :many
SELECT app, slug, slot, owner, owner_kind, ephemeral, path, path_visible,
       state, teardown_note, resources, secrets, descriptor_path, description,
       created_at, last_seen
FROM entries
ORDER BY app, slug;

-- name: GetEntry :one
SELECT app, slug, slot, owner, owner_kind, ephemeral, path, path_visible,
       state, teardown_note, resources, secrets, descriptor_path, description,
       created_at, last_seen
FROM entries
WHERE app = ? AND slug = ?;

-- name: UpsertEntry :exec
INSERT INTO entries (app, slug, slot, owner, owner_kind, ephemeral, path,
                     path_visible, state, teardown_note, resources, secrets,
                     descriptor_path, description, created_at, last_seen)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
ON CONFLICT(app, slug) DO UPDATE SET
  slot = excluded.slot,
  owner = excluded.owner,
  owner_kind = excluded.owner_kind,
  ephemeral = excluded.ephemeral,
  path = excluded.path,
  path_visible = excluded.path_visible,
  state = excluded.state,
  teardown_note = excluded.teardown_note,
  resources = excluded.resources,
  secrets = excluded.secrets,
  descriptor_path = excluded.descriptor_path,
  description = excluded.description,
  created_at = excluded.created_at,
  last_seen = excluded.last_seen;

-- name: DeleteEntry :exec
DELETE FROM entries WHERE app = ? AND slug = ?;

-- name: UpdateEntryState :exec
UPDATE entries SET state = ?, teardown_note = ? WHERE app = ? AND slug = ?;

-- name: TouchEntry :exec
UPDATE entries SET last_seen = ? WHERE app = ? AND slug = ?;

-- name: ListBands :many
SELECT app, resource, base, span FROM bands ORDER BY app, resource;

-- name: UpsertBand :exec
INSERT INTO bands (app, resource, base, span) VALUES (?, ?, ?, ?)
ON CONFLICT(app, resource) DO UPDATE SET base = excluded.base, span = excluded.span;

-- name: DeleteBandApp :exec
DELETE FROM bands WHERE app = ?;

-- name: ListReservations :many
SELECT id, note FROM reservations ORDER BY id;

-- name: AddReservation :execresult
INSERT INTO reservations (note) VALUES (?);

-- name: AddReservationPort :exec
INSERT INTO reservation_ports (reservation_id, port) VALUES (?, ?);

-- name: AddReservationName :exec
INSERT INTO reservation_names (reservation_id, name) VALUES (?, ?);

-- name: ListReservationPorts :many
SELECT reservation_id, port FROM reservation_ports ORDER BY reservation_id, port;

-- name: ListReservationNames :many
SELECT reservation_id, name FROM reservation_names ORDER BY reservation_id, name;

-- name: ListClients :many
SELECT identity, kind, last_seen, ephemeral FROM clients ORDER BY kind, identity;

-- name: UpsertClient :exec
INSERT INTO clients (identity, kind, last_seen, ephemeral) VALUES (?, ?, ?, ?)
ON CONFLICT(identity, kind) DO UPDATE SET last_seen = excluded.last_seen, ephemeral = excluded.ephemeral;

-- name: DeleteClient :exec
DELETE FROM clients WHERE identity = ? AND kind = ?;

-- name: ListSpecs :many
SELECT app, spec FROM specs ORDER BY app;

-- name: UpsertSpec :exec
INSERT INTO specs (app, spec) VALUES (?, ?)
ON CONFLICT(app) DO UPDATE SET spec = excluded.spec;

-- name: DeleteSpec :exec
DELETE FROM specs WHERE app = ?;

-- name: DeleteReservationPortsByNote :exec
DELETE FROM reservation_ports WHERE reservation_id IN (SELECT id FROM reservations WHERE note = ?);

-- name: DeleteReservationNamesByNote :exec
DELETE FROM reservation_names WHERE reservation_id IN (SELECT id FROM reservations WHERE note = ?);

-- name: DeleteReservationsByNote :exec
DELETE FROM reservations WHERE note = ?;
