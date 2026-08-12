# wt — this repository's per-worktree environments

Why the tooling exists, what it does, and the rules that keep two
worktrees of this repository from colliding. The concrete facts — the
descriptor filename, the port band bases, the shared resources — are
recorded in the managed block at the end of this document, and `wt show`
reads them live.

## Why

Two worktrees of one repository run side by side, and every resource the
application touches has to come from somewhere. Without the tooling, the
second worktree either fails to bind a port or silently attaches to the
first worktree's state, and the failure looks like an unrelated bug. This
repository adopted `wt` so that each worktree's allocation is explicit,
recorded in a descriptor it can read.

## The slot model

Slot 0 is the primary checkout: never managed, keeps the committed
defaults. Slots 1 and up are worktrees; every resource a worktree holds is
derived from its slot and recorded in its descriptor. Two worktrees'
resource tables are disjoint by construction.

## What `wt init` writes

`wt init` attaches to a worktree something else created: it allocates the
slot, materialises the state paths (seeding per the spec's modes), writes
the descriptor and the `.env` managed block, and runs the repo's hooks.
It is idempotent, and re-running it repairs a worktree that drifted.

## What stays shared and why

The resources marked shared are reached unisolated by every worktree and
the main checkout. Writes to them escape the worktree — see the managed
block's shared list, and the descriptor's shared block for the blast
radius of each.

## The registry and the resolution chain

The coordinator keeps a per-machine registry of entries. The application
never reads it: its generated reader (or the `.env` block) resolves the
descriptor from cwd, falling back to the legacy defaults in the primary
checkout and refusing loudly in a linked worktree with no descriptor.

## Teardown ordering

`wt rm` (or `wt reconcile`) tears the resources down by handle from the
registry, never from the working tree: reap the processes bound to the
worktree's ports, tear down in dependency order, drop the entry. The
directory can be deleted first; the handle is the registry's.

## The documented bypass

When the helper cannot run, the manual commands are the hooks in
`wt.yaml` and the values in the descriptor — read them with `wt show
--json`, never hardcode a port or a path. The managed block records the
manual start command.

## The co-resident production stack

If a production stack co-resides on this machine, the tooling never
touches it: its ports are reserved host-globally (`wt bands reserve
--host`, with a note naming what holds the range), and label-based
teardown refuses a compose project name that matches a reservation.

# --- managed by wt; edits below are overwritten ---
# wt-field: app=plain-app
# wt-field: band api=8200
# wt-field: descriptor=wt-env.json
# wt-field: resources=api, db, cache, shared_db
# wt-field: shared=https://api.example.com/v1, shared_db
This repository's facts, recorded when these artefacts were generated:
- descriptor: wt-env.json (json)
- api: port, band base 8200
- db: state-path, seeded mode seeded
- cache: state-path, seeded mode empty
- shared_db: state-path, shared by default
- shared: https://api.example.com/v1, shared_db
- manual start (the spec's start hook): nohup {worktree}/plain-app-server >{worktree}/wt-server.log 2>&1 &
# --- end ---
