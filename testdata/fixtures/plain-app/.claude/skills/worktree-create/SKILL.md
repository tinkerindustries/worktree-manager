---
name: worktree-create
description: Create a worktree of this repository and attach it with wt init. Use when the user asks to spin up a worktree, work in parallel, or start a fresh branch. Do not use when already inside a worktree and wanting to keep working there.
---

# worktree-create

Create one worktree of this repository and attach it to the environment
tooling with `wt init`. The repo's facts — the descriptor filename, the
port band bases, the shared resources — are recorded in the managed block
at the end of this file; regeneration refreshes the block and preserves
edits to this text.

## Sequence

1. Pre-flight.
2. Make the worktree.
3. `wt init`.
4. Report.
5. Stop.

## Pre-flight

- Confirm the repo root and the default branch: `git rev-parse
  --show-toplevel`, `git branch --show-current`.
- Confirm the tree is clean: `git status --porcelain` must print nothing.
- Pull: `git pull --ff-only` on the default branch.
- Any failure stops here. Never auto-stash, never auto-checkout, never
  silently merge — the user's uncommitted work is worth more than this
  skill finishing.

## Naming

- No slug supplied: generate a memorable `<adjective>-<animal>` slug,
  both words at most eight characters, kebab-case, at most 32 characters
  in total. Adjectives: brisk, calm, eager, fleet, grand, hasty, lucky,
  nimble, quick, rusty, swift, zesty. Animals: badger, cougar, falcon,
  heron, jaguar, lemur, otter, panda, raven, tiger, walrus, zebra. State
  the slug up front so the user can `cd` there.
- A generated slug that collides (the directory exists, or the branch
  exists) regenerates up to three times and then asks the user.
- A slug the user supplied that collides stops and asks immediately.

## Making the worktree

- The path is the repo's, not this skill's: run `wt spec path --slug
  <slug>` from the main checkout and use what it prints. The template it
  resolves is `worktrees.path` in `wt.yaml`, recorded as `worktrees` in
  the managed block below; the default is `.claude/worktrees/<slug>`,
  which is where Claude Code's own isolation puts a tree. Never derive
  the path by hand, and never resolve a relative template against the
  cwd — a worktree made from inside a worktree would land underneath it.
- Prefer the mechanism this session already uses for isolation
  (`isolation: "worktree"` in Claude Code); otherwise `git worktree add
  -b <branch> <path> <base>`.
- Branch names follow the repo's convention: bare `<slug>`,
  `claude/<slug>` to namespace agent-created branches, or
  `manual-<slug>` for hand-made ones.
- Base branch: the default branch, unless the repo's policy reads a base
  branch off a ticket — then branch from `origin/<base_branch>`.
- If the chosen isolation would make the work item unreachable from
  inside the new worktree (an isolated store means the ticket does not
  exist there), warn loudly before creating anything.

## Description

Ask once for a description, ten words or fewer — it is required by
`wt init`, so that `wt list` says what each worktree is for — plus an
optional long form.

## wt init

- Run `wt init --description "<the description>"` in the worktree.
- On failure: report it; the worktree survives un-initialised — say so
  explicitly rather than implying an environment exists.
- `--no-env` for docs-only work: an un-allocated worktree collides with
  everything, and the failure looks like an unrelated bug, so skipping the
  environment is always stated explicitly.

## Report

Report: the path, the branch, dependency-install status, environment
status (or `skipped — --no-env`), the URLs read back from the descriptor,
the cwd, and the shared-resource list repeated back — writes to shared
resources escape the worktree, and whoever works there needs to know
before making one.

The briefing block is rendered from `wt show --json`. If the descriptor
is missing, refuse to render it: a briefing with guessed values is worse
than none.

## Stop there

This skill is scaffolding, not an execution agent: hand back a ready
worktree and stop. No `ExitPlanMode`, no code changes, no pull requests.

## Triggers

Use when the user says "spin up a worktree", "I want to work on this in
parallel", or asks for a fresh branch. Do not use when already inside a
worktree and wanting to keep working there.

## Resuming an existing branch

The flow above branches from the default branch and is the wrong tool for
resuming work that already exists. For the review-feedback case: make a
worktree on the existing branch by whatever mechanism, then `wt init` —
never branch from the default branch and silently discard the feedback
the existing branch was meant to address.

# --- managed by wt; edits below are overwritten ---
# wt-field: app=plain-app
# wt-field: band api=8200
# wt-field: descriptor=wt-env.json
# wt-field: resources=api, db, cache, shared_db
# wt-field: shared=https://api.example.com/v1, shared_db
# wt-field: worktrees=.claude/worktrees/{slug}
# --- end ---
