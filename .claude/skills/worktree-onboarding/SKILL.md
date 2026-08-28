---
name: worktree-onboarding
description: Adopt a repository into worktree-manager: audit its resources, choose the policy, reserve port bands, write wt.yaml, generate the artefacts, patch the entry points, and prove the result with two worktrees side by side. Use when a repository has no wt.yaml and should get per-worktree environments. This is a once-per-repo, judgment-heavy, conversational task — never run it unattended.
---

# onboarding — adopt a repository

Onboarding is the one-time, judgment-heavy task that ends with a
repository committed to per-worktree environments: a `wt.yaml` at its
root, a band reserved on this machine, the generated artefacts in the
tree, the entry points patched, and — the only step that proves any of it
— two worktrees running side by side. Everything before that step is a
claim.

The division of labour is fixed (09-onboarding.md §2): the binaries
expose facts and refuse specs they cannot honour; this skill decides what
the facts mean. The tool never infers that a listener is a production
stack, that a resource should be shared, or that a port band is the right
one — you do, with the developer, and the decisions are recorded.

## The eight phases

1. **Audit** — find every resource two concurrent worktrees would contend
   for.
2. **Policy** — the Part C decisions, as an interview.
3. **Bands** — where the ports sit, machine-locally.
4. **Spec** — write `wt.yaml`, validate it, read both slot tables.
5. **Generate** — the artefacts, the settings, the ignore line.
6. **Patch entry points** — make every entry point read the allocation.
7. **Prove it** — two worktrees, side by side, healthy at once.
8. **Import what already exists** — adopt, never recreate.

## Phase 1 — Audit

Scan rather than ask (09-onboarding.md §3). The scannable half:

- committed default ports: config defaults, `ports:` entries,
  `.env.example`, constants;
- compose files: whether any pins `name:` (a pinned name makes the second
  worktree attach to the first's containers, and nothing fails), and
  which volumes and networks are not project-scoped — the project name
  isolates the rest for free, so the audit is about what is *not*
  project-scoped;
- the package manager, and whether its dependencies live in the tree;
- a separate test stack;
- `$HOME` paths in source — `UserHomeDir`, `~/`, `%USERPROFILE%`;
- unix sockets and lockfiles;
- what is listening right now: `wt ports scan --json` — the listeners the
  developer has not declared.

The half that decides whether the result is correct comes from the
developer, row by row:

- which shared resources matter, and the blast radius of each;
- whether the shared state is something the worktree's user *wants* to
  reach (this is the actual driver of the isolate-by-default decision);
- leases and leader election — grep finds them inconsistently;
- a co-resident production stack — its ports get a host-global
  reservation (`wt bands reserve --host`), never a design-around;
- external namespaces — a third-party API tenant, a cloud resource, a
  piece of hardware — which nothing in the repository mentions.

The phase ends with a table the developer confirms row by row, each
resource marked **isolate**, **share** or **reserve**. Confirmation
matters more than completeness: a resource the developer silently
disagrees about will be wrong in the spec and correct in their head.

## Phase 2 — Policy

The Part C decisions, as an interview (09-onboarding.md §2). Each has a
driver question the requirements already identified:

| Decision | Question |
|---|---|
| C1 which dimensions isolate by default | is the shared state something the worktree's user wants to reach? |
| C2 seeding mode | is the work *on* the store's schema, or merely *through* it? |
| C3 descriptor format | what does this repo already parse? |
| C4 config delivery | does every entry point read the environment, or do some resolve natively? |
| C7 base branch | does this repo have stacked or dependent work? |
| C8 cleanup posture | how expensive is an idle worktree? |
| C9 slot ceiling | resources per slot, times how many run at once |

(C5 — locking — is the tool's, not the repo's; C6 — the enforcement hook
— is asked here too: does this repo have unattended dispatch, and does
the developer want the guard enforced, or prompt-level rules only?)

These are choices to make consciously and record, not defaults to
standardise. Write the decision record to `docs/wt-decision-record.md`
(the spec says what was chosen; the record says why — six months later
the spec is the contract and the record is the memory). The spec stays
terse.

## Phase 3 — Bands

- `wt bands suggest --json` — the coordinator computes the required size
  (slot ceiling × ports per slot); the suggestions collide with no
  existing band and no host reservation.
- Show the developer the suggestions and `wt ports scan --json` side by
  side: the reserved listeners they have not declared are the ones to
  avoid, and anything the scan shows that is not in a band and not
  reserved is a candidate for `wt bands reserve --host` (with a note
  naming what holds the range — an unlabelled reservation is one nobody
  can later judge).
- The developer confirms the base(s); `wt bands reserve --base <name>=<port>...`
  claims them. One base per port resource; group-form resources share one
  base.

This step is machine-local by design. A colleague who clones the already
adopted repo reserves the band on their own machine — small, repeatable,
and not part of adoption; the reference doc's facts block records the
base, and the one-liner is `wt bands reserve --base api=<base>`.

## Phase 4 — Spec

Write `wt.yaml` at the repository root from the phase-1 table and the
phase-2 answers. Then:

- `wt spec validate` — every refusal names the field and the reason;
  fix and re-validate.
- `wt spec explain --slot 1 --slug <slug> --base <name>=<port>...` and
  the same for slot 2, and read both tables against the phase-1
  inventory. This is the conversation made concrete: two tables side by
  side is what surfaces a missed collision before anything is built.

One judgment call belongs here and nowhere else: the `removal:` block,
which decides what each of `wt rm`'s three safety checks does on a hit —
`refuse` or `warn`. The defaults suit a personal repository (refuse on
uncommitted changes, warn on unpushed commits and an open pull request),
because a branch that was never pushed and a machine without `gh` are
ordinary there and should not make a worktree unremovable. Ask instead
whether this repository's branches always carry a pull request and always
have an upstream. When the answer is yes, write it down:

```yaml
removal:
  unpushed: refuse
  open_pr: refuse
```

That is a policy call about the repository, which is why it is spec
configuration and not something either binary infers. `wt cleanup` and
the coordinator's sweep are not affected by the block: they require a
merged pull request whatever it says.

The second call here is `worktrees:`, where this repository's worktrees
go. Say nothing and they go to `.claude/worktrees/<slug>` — the directory
Claude Code's own `isolation: "worktree"` creates trees in, so the manual
flow and the automatic one land in one place. Override it only for a
repository with a reason: a heavy tree the developer wants out of the
repository directory, or a house convention that predates the tooling.

```yaml
worktrees:
  path: "../{app}-worktrees/{slug}"
```

The template takes `{slug}` (required — without it every worktree
resolves to the same directory), `{app}` and `{home}`; a relative path
resolves against the main checkout, never cwd. Check it with `wt spec
path --slug <slug>`, which is what the generated create skill runs. A
repository keeping its trees inside itself needs the directory
gitignored, or the main checkout is untracked-dirty from the first
worktree on — `wt init` writes the line to `info/exclude` itself, and
adoption's phase 6 is where it is committed to `.gitignore` instead.

## Phase 5 — Generate

The artefacts (07-agent-surface.md), generated per repo — they name this
repo's facts. Creating and removing a worktree are not among them: Claude
Code's own `WorktreeCreate` and `WorktreeRemove` hooks, registered once
per machine by `wt claude install`, answer for every repository, so a
per-repo create or remove skill would be a second implementation of what
the hooks already do. The file inventory and the exact managed-block format are
in `references/primitives.md`; the acceptance fixtures render the same
files through `internal/artefact`, so the fixture is the executable
reference for what the generate phase must produce.

Rules that bind every generated file:

- **Every file closes with the managed block** — `# --- managed by wt;
  edits below are overwritten ---` ... `# --- end ---`, with one
  `# wt-field: name=value` record per spec field it came from (app,
  descriptor filename, resources, one band record per port resource,
  shared names). This is the `.env` block's own convention — there is
  exactly one marker convention in the system.
- **The body is stable; the facts live in the block.** Anything the file
  needs at runtime reads its own block (the tripwire reads the descriptor
  filename from its own block). Regeneration replaces only the block, so
  hand edits outside it survive — a generated file will be edited, and
  the edits are worth more than the regeneration.
- Write the hooks' entries into `.claude/settings.json`, merging with
  anything already there (never clobber other settings). The SessionStart
  tripwire is part of the adopted surface and is committed; the PreToolUse
  guard hook is added **only when the developer confirmed enforcement in
  phase 2** — committing it makes the enforcement choice for everyone who
  clones, so the choice has to be the developer's.
- Commit the `.gitignore` line for the descriptor (`wt-env.yaml` or
  whatever `emit.descriptor.filename` names, plus the build hook's output
  and the `.env` if the repo does not already ignore it) — this phase's
  job precisely so that `wt init` never has to touch a tracked file. When
  the worktrees live inside the repository (the default), the directory
  holding them goes in too, anchored: `/.claude/worktrees/`.
- Commit everything generated: the adopted surface is part of the repo.

The generated artefacts:

1. `.claude/hooks/wt-session-start.sh` — the tripwire: classifies cwd
   via git, prints one sentence (no environment → `wt init`; descriptor
   present → one-line summary), never allocates.
2. `.claude/hooks/wt-guard.sh` — the opt-in enforcement hook: calls
   `wt guard --json` (local, no socket), maps a denial to the hook
   protocol, fails open and says so. It sets `WT_GUARD_CACHE` — the
   per-session classification cache (the phase-7 decision): a worktree
   removed mid-session drops the cache and the next call reclassifies,
   failing open with a one-time note.
3. `docs/wt.md` — the reference doc: why the tooling exists, the slot
   model, what `init` writes, what stays shared and why, the registry
   and resolution chain, the teardown ordering, the documented bypass,
   the co-resident production stack, and the facts block.
4. `CLAUDE.md` — the tripwire section (appended to the repo's own text
   if there is one): this repo uses per-worktree environments; never
   hardcode a port or a path, read them with `wt show`; something else
   creates the worktree, `wt init` attaches to it.

## Phase 6 — Patch entry points

Find every file inside the repo that hardcodes a port or a state path and
make it read the allocation instead — the descriptor (through the
generated reader, where the repo chose one), the `.env` managed block, or
the hook environment. Enumerate entry points; do not sample. A single
missed entry point produces a worktree that is isolated for every path
the developer tested and shared for the one they did not — the most likely
place for this phase to be left incomplete, and the one where the failure
is delayed: it shows up weeks later as a bug that looks unrelated.

## Phase 7 — Prove it

Two worktrees, side by side, at the same time.

- Make two worktrees by whatever normally makes them (the
  `WorktreeCreate` hook, or `git worktree add` by hand), at the paths
  `wt spec path --slug <slug>` gives — proving the repository's own convention, not a
  path chosen for the test.
- `wt init` both; `wt start` both.
- Confirm both healthy simultaneously, and the two resource tables
  disjoint (`wt show --json` in each).
- Tear both down (`wt rm`), and confirm `wt doctor` is clean.

Everything before this phase is a claim; this is the only step that tests
whether the phase-1 audit was complete. A collision here sends the work
back to phase 1, not to a patch: a resource that collides at this point
is a resource the inventory missed.

## Phase 8 — Import what already exists

Where the repo has worktrees already, or a per-repo implementation being
retired, adopt rather than recreate: an existing worktree's descriptor
(its ports, its slot) lets the entry be reconstructed without disturbing
anything running — the descriptor beats the registry, so `wt init` in an
existing worktree rebuilds its entry at its recorded slot. Retire the old
implementation after the import, never before: a half-migrated repo with
two allocators is worse than either. Where nothing pre-exists, say so and
stop — the skip is stated, never silent.

## Failure modes

| Situation | Behaviour |
|---|---|
| A production stack's ports cannot be cleared | reserve them host-globally; do not design around them |
| Spec validates but phase 7 collides | back to phase 1 — the audit was incomplete and the spec is a symptom |
| Developer declines to confirm a phase-1 row | leave it out of the spec and into the hand-authored `shared` block — the honest place for "we know about this and are not isolating it" |
| No band space free | `wt bands list --json` for the ledger view, and a conversation about what on the machine is reserved and no longer needs to be |
| The skill is re-run against an adopted repo | phases 1, 4, 5 and 7 are idempotent — regeneration refreshes the facts blocks and preserves hand edits; phases 3 and 6 are re-confirmed, never blindly re-run |

## References

- `references/primitives.md` — the command surface the skill reads: every
  verb, its flags, the exit codes, and the JSON shapes.
- `references/plain-app-walkthrough.md` — the fixture walkthrough: what
  each phase produced for plain-app, and what the acceptance tests prove.
- The design of record: `docs/design/09-onboarding.md` (this module),
  `docs/design/07-agent-surface.md` (the artefacts), `docs/ARCHITECTURE.md`
  §4.6, §5, §9.5, §9.6. Read them; never edit them.
