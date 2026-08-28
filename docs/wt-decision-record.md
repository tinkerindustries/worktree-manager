# Adoption decision record

`wt.yaml` is the contract; this file is the memory. It records why each
choice was made, in the order the onboarding skill asks them.

Adopted 2026-08-28.

## Phase 1 — what two worktrees contend for

The audit found exactly two contended resources and a longer list of
machine-global ones that are deliberately not isolated.

| Resource | Finding | Decision |
|---|---|---|
| The development coordinator's port | `wtd` defaults to `127.0.0.1:7833`; two worktrees both want it | isolate |
| The development coordinator's store | `$HOME/.wt/wt.db` is shared with the installed coordinator that manages every adopted repository on this machine, so a branch carrying a schema change could refuse or migrate live state | isolate |
| `internal/coord/fleet_test.go`'s drift fixture | bound a fixed `127.0.0.1:7001`, so a second worktree running the suite failed on a bind the test never meant to exercise | fixed in the test, not modelled in the spec |
| Go build and module caches | shared by design and concurrency-safe | share |
| Docker acceptance tests | container names and compose projects are randomised per run, ports probed free | share, no contention |
| `~/.local/bin/wt`, `~/.local/bin/wtd` | `dist/install.sh` replaces the machine's installation whatever tree it runs from | share, named in `shared:` |
| `~/.claude/settings.json` | `wt claude install` merges into the user's own file | share, named in `shared:` |
| The launchd registration | one per user; a worktree cannot have its own | share, named in `shared:` |
| The menu bar app | `.build/` is per-tree; the UserDefaults domain is machine-global and read-only in v1 | share, no contention |

The installed coordinator's port already held a host reservation on 7834
before adoption, which is the co-resident-stack treatment the skill asks
for. Nothing here designs around it.

## Phase 2 — policy

**C1, which dimensions isolate by default.** Both declared resources
isolate. Neither is shared state a worktree's user wants to reach: a
development coordinator that talked to the real store would act on every
other repository's worktrees.

**C2, seeding mode.** The `store` resource has no `seed:` block, so a new
worktree gets an empty store. Seeding it from `~/.wt/wt.db` was rejected:
a development build would then start life holding real registry entries
and could tear down worktrees belonging to other repositories.

**C3, descriptor format.** JSON. `internal/generate`'s reader is
stdlib-only by construction, and the standard library parses JSON and not
YAML.

**C4, config delivery.** The `.env` managed block and the hook
environment. `WT_HOME` is the single variable that redirects both
binaries: the development `wtd` writes its `endpoint.json` there, and a
client reading the same `WT_HOME` finds it. `WT_COORD_ADDR` and
`WT_COORD_PORT` are for the hooks and for a human running `wtd` by hand.

The sharp edge: exporting `WT_HOME` into an interactive shell points the
*installed* `wt` at the development coordinator too. That is usually what
you want inside a worktree, and it is worth knowing when a `wt list`
comes back empty.

**C6, the enforcement hook.** Committed. This repository runs unattended
agents, which is the case the guard exists for.

**C7, base branch.** `main`. No stacked work.

**C8, cleanup posture.** An idle worktree costs a directory and a stopped
coordinator, so the default sweep is enough.

**C9, slot ceiling.** 8. One port per slot, and nothing about this
repository suggests more than a handful of trees at once.

**Removal policy.** The defaults: refuse on uncommitted changes, warn on
unpushed commits and on an open pull request. Every branch here does get
a pull request, but a warn-level check costs nothing when the branch is
in the expected state and keeps a tree removable on a machine without
`gh`.

## Phase 3 — the band

`wt bands suggest` proposed base **1**. Ports 1–8 are privileged and
unusable without root, so the suggestion was overridden. The band sits at
**7840**, giving slots 1–8 the ports 7841–7848 — adjacent to the
installed coordinator's reserved 7834, and clear of every band and
reservation in the ledger and every listener in `wt ports scan`.

That `bands suggest` starts from base 1 is worth its own look. On a fresh
machine its first suggestion is always unusable.

## Phase 4 — the spec

**Where the development store lives.** `{worktree}/.wt-dev/`, not
`{home}/...`. A state path is deleted on teardown only when the caller
purges it, so a store under `{home}` would outlive every worktree that
made one and accumulate silently. Inside the tree it goes when the tree
goes, and no `purge:` flag is needed.

The cost is that `wt spec explain` cannot show the two slots as disjoint:
it has no worktree for a hypothetical slug, so `{worktree}` is cwd in
both tables. Uniqueness comes from each worktree being its own directory,
which phase 7 is what actually proves.

**The reaper allowlist.** `wtd`. A leftover development coordinator still
holding a worktree's port is the one process teardown may signal. The
installed coordinator shares the binary name but never holds a band port
— 7834 is host-reserved — so it is never a target. Phase 7 confirmed
this: teardown reaped both development coordinators and left the
installed one running.

## Phase 7 — what was proved

Two worktrees at `wt spec path`'s own locations, initialised and started
at the same time:

- slot 1 on port 7841, slot 2 on port 7842, each with its own store,
  database and `endpoint.json`;
- three `wtd` processes listening at once — 7834, 7841, 7842 — with the
  installed coordinator untouched;
- both torn down with `wt rm`, each reaping its own coordinator by pid;
- `wt doctor` clean of any worktree-manager finding afterwards.

## Phase 8 — what already existed

Nothing to import. The repository had one worktree at adoption time, the
one this work was done in, and it predates the spec, so it holds no
descriptor to adopt. It can be attached at any time by running `wt init`
in it, which will allocate it a slot and start a development coordinator.
