# CLAUDE.md

This repository's working instructions. The tripwire below is generated: it is replaced on regeneration, and everything above it is this repository's own.

# --- managed by wt; edits below are overwritten ---
# wt-field: app=plain-app
# wt-field: band api=8200
# wt-field: descriptor=wt-env.json
# wt-field: resources=api, db, cache, shared_db
# wt-field: shared=https://api.example.com/v1, shared_db
This repository uses per-worktree environments.
- Never hardcode a port or a path: read them with `wt show`.
- Something else creates the worktree; `wt init` attaches to it.
See docs/wt.md for the full reference.
# --- end ---
