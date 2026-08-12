# M7 — Agent Surface

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** A17, T13, C6, C7, D1, the tripwire hook from M4 §2.4, and all of T11 except the mechanism M4 took — so B11.1 to B11.6, B11.8, B11.9 (generated-slug half), B11.12 to B11.15, and B13.3.
**Depends on:** M4 for the verbs, M5 for the descriptor, M1 for classification and containment, M9 for generating everything here.

## 1. What this module answers

How a human or an agent gets the lifecycle right without holding it in their head, and how an agent working inside a worktree is kept inside it.

Five artefacts, all generated per repo by M9:

| Artefact | Requirement |
|---|---|
| `worktree-create` skill | B11.1 |
| `worktree-remove` skill | B11.1 |
| `SessionStart` tripwire hook | M4 §2.4 |
| `PreToolUse` enforcement hook | D1, C6 — opt-in |
| One reference doc plus one `CLAUDE.md` tripwire | A17 |

## 2. Generated, not generic

The requirements' open question 4 asked whether the skills should be generic. They should not.

The three existing pairs work because they are concrete: they name the repo's own commands, its ports, its shared resources. A generic pair that read the descriptor at runtime would be thinner and weaker, because a skill's value is largely in what it says before anything runs.

What varies and what does not is now clean, which is what makes generation cheap. The lifecycle is fixed — something creates a worktree, `wt init` attaches, `wt rm` tears down. Only the facts vary, and every fact is in the spec or the descriptor. So these are templates with the repo's facts substituted, not documents written from scratch.

## 3. The create skill

M4 dropped worktree creation, so this skill carries what the binary no longer does.

Its sequence: pre-flight, make the worktree, `wt init`, report, stop.

**Pre-flight (B11.8).** Repo root, default branch, clean tree, then `git pull --ff-only`. Any failure stops. Never auto-stash, never auto-checkout, never silently merge — the user's uncommitted work is worth more than the skill finishing.

**Naming.** A memorable `<adjective>-<animal>` slug when none is supplied, both words at most eight characters, stated up front so the user can `cd` there (B11.14). A generated slug that collides regenerates up to three times and then asks; a supplied slug that collides stops and asks immediately (B11.9). Branch and directory naming follow the repo's convention (B13.3) — bare `<slug>`, `claude/<slug>` to namespace agent-created branches, or `manual-<slug>` to distinguish hand-made from dispatch-made.

**Making the worktree.** Prefer the mechanism the harness already has — Claude Code's `isolation: "worktree"` — over `git worktree add`, so the manual flow is genuinely the manual equivalent of the automatic one rather than a parallel convention. Either way the path convention is `.claude/worktrees/<slug>` (B11.13).

**Base branch (C7).** A repo declares whether it always branches from the default branch or reads `base_branch` off a ticket. With T13, the skill accepts an issue key in place of a slug and branches from `origin/<base_branch>` (B13.1).

**Ticket reachability (B13.2).** Where the repo has an issue tracker and the chosen isolation mode would make that ticket unreachable from inside the new worktree — an empty isolated store means the issue does not exist there — the skill warns loudly before creating anything. It is the case where the isolation the user asked for silently removes the thing they were about to work on.

**Description (B11.16).** A short description, ten words or fewer, prompted once if not supplied, plus an optional long form. It is required by `wt init` so that `list` says what each worktree is *for*.

**Report (B11.3).** Path, branch, dependency-install status, environment status or `skipped — --no-env`, the URLs read back from the descriptor, the cwd, and the shared-resource list repeated back. That last item is not decoration: writes to shared resources escape the worktree, and whoever works there needs to know before making one.

**`--no-env` (B11.5).** For docs-only work, with a hard rule that skipping is always stated explicitly. An un-allocated worktree collides with everything, and the failure looks like an unrelated bug — a port in use, or compose reusing another worktree's containers.

**Stop there (B11.2).** The skill is scaffolding, not an execution agent. It hands back a ready worktree and stops: no `ExitPlanMode`, no code changes, no PRs.

**Triggers (B11.1).** The description enumerates the natural-language triggers — "spin up a worktree", "I want to work on this in parallel" — and the anti-triggers, of which the important one is "already inside a worktree and wants to keep working there".

## 4. The remove skill

M4 §7 moved the defensive checks into `wt rm`, which changes this skill's job from performing them to not fighting them.

`wt rm` checks for uncommitted changes, unpushed commits and an open PR, and exits 3 when any of them hit. The skill's rule is to stop and ask on exit 3, and never to look for a way around it. An agent that responds to a refusal by adding `--force` has defeated the check rather than satisfied it.

The rails it still owns are the ones no exit code can express:

- **Never `--force` a `git worktree remove`** (B11.11). Git refusing is signal that a check missed something.
- **Never delete the worktree you are standing in**, which `wt` also refuses, and never the remote branch, because the PR points at it.
- **Never pick a target on its own.** "The most recent worktree" is not an inference to make.
- **Never clean up on a failure path** (B11.12). Failed smoke, no PR, mid-investigation — leaving it alive is correct. This is the genuinely agent-side judgment in the whole module, because it depends on what the session was trying to do, which no tool can see.

Anti-triggers matter more here than in the create skill. "The run failed" and "done with worktree" are close in wording and opposite in intent.

## 5. The briefing block

B11.6 wants a block that can be pasted into an agent session. It contains:

- what is isolated, with values;
- what is shared, and the blast radius of each, rendered from the descriptor's structured `shared` block (M5 §2.3);
- the exact path prefix every `Read`, `Edit` and `Write` must start with;
- how to reach this environment from outside the worktree (`--env <path>`);
- which commands are not for this session;
- the snapshot-versus-live caveat where a state path was seeded (B6.2) — a seeded store is a snapshot taken at creation that diverges as work continues, and is not a live mirror;
- which binary is which (D5) — the uniquely-named workspace build for smoke-testing, the known-good system binary on `PATH` for every close-out and bookkeeping call.

Every value is read from `wt show --json` when the block is rendered. A11 is the rule and this is where it is easiest to break: a briefing that says "the UI is on 3100" is correct once.

## 6. Enforcement

### 6.1 Why a hook and not a rule

D1 is the sharpest lesson in the requirements and the reasoning generalises past Claude Code.

Prompt-level worktree confinement is a one-time cwd snapshot and does not hold. `Read`, `Edit` and `Write` take absolute paths, so cwd is irrelevant to them, and a worktree lives *inside* the repo it branches from, so a parent-repo path is always valid and always exists. A worker can therefore do every edit, commit and push in the primary checkout while its own startup check truthfully reported that it was in a worktree.

Nothing about that depends on the model being careless. The path is valid, the file is there, and the operation succeeds.

### 6.2 What the hook does

Classify cwd through M1: not a repository allows everything, the primary checkout denies every edit, a linked worktree allows edits under its own root.

Resolve `file_path` with M1 §3.1's symlink-safe, boundary-safe containment test, and **deny with a reason naming the correct root verbatim**, so the model self-corrects and retries rather than guessing.

Deny `Read` of a `CLAUDE.md` outside the worktree. The parent copy is stale — the worktree was fast-forwarded onto `origin/<base>` — and reading it nudges the model into adopting the wrong path prefix.

Deny direct mutation of a live shared store named in the descriptor's `shared` block, so changes cannot bypass whatever audit path the app has. Fail open on ambiguous shell constructs — pipes, `$(...)`, environment variables, globs. The goal is to make the obvious bypass loud, not to be a general bash sandbox, and a guard that tries to be one will produce false denials that train the agent to work around it.

Emit only one deny per call, so the tool surface stays uncluttered.

### 6.3 Implementation

The classification and containment logic already exists in M1, so the hook should call `wt guard --json` rather than reimplement it. One implementation, one set of edge cases.

The cost is a subprocess on every tool call. `wt guard` therefore reads the descriptor and git only, never the registry, and the hook caches the classification for the session keyed on cwd — the classification cannot change while a session runs unless the worktree is removed underneath it.

If that budget turns out not to hold, the fallback is embedding the logic in the hook script, and accepting a second implementation of the one test that must not be wrong. Recorded as an open question rather than assumed away.

### 6.4 The process-layer complement

C6 records that enforcement is a policy choice: bacio's hook denies, while harness and mini-infra use prompt-level rules. The hook exists because the prompt-level version demonstrably failed, but it is Claude-Code-specific machinery and a repo without unattended dispatch may not need it.

Two rules raise the floor without enforcing anything, and belong in the briefing regardless:

- the worker's first task records the worktree-root prefix verbatim;
- `git branch --show-current` is re-run immediately before `commit` and again before `push`.

## 7. The tripwire hook

From M4 §2.4. On `SessionStart`, classify cwd. In a linked worktree of an adopted repo with no descriptor, print that this worktree has no environment and name `wt init`. Detection and a sentence, no allocation — the requirements reject auto-creating an environment, and this respects that.

Where a descriptor does exist, print a one-line summary: slug, slot, and the URLs. It costs nothing and it answers "which environment am I on?" at the start of the session rather than after a collision.

## 8. Documentation

A17: one reference doc, one `CLAUDE.md` tripwire.

The reference doc covers why the tooling exists — the concrete collisions, named — the slot model and the resource table, what `init` writes, what stays shared and why, the registry, the resolution chain, the teardown ordering, and the documented bypass for when the helper cannot run (B4.5, with M3 §4.5 supplying the exact command so the doc cannot drift).

Where a production stack co-resides on the machine, B8.2 requires the doc to state explicitly that the tooling never touches it. M2 §7 and M3 §4.2 make that true; this is where it gets said.

The tripwire is the one rule an agent needs in its head, and A17's bar for inclusion is worth restating: would a model that has not read the linked doc do the wrong thing? For this design that is three facts — this repo uses per-worktree environments; never hardcode a port or path, read them with `wt show`; something else creates the worktree, `wt init` attaches to it.

## 9. Resuming an existing branch

B11.15 wants a documented recipe for the review-feedback case, since §3's flow branches from the default branch and is the wrong tool for resuming work that already exists.

With `wt` out of the creation business this is entirely a skill concern: make a worktree on the existing branch by whatever mechanism, then `wt init`. Worth writing down anyway, because the create skill's pre-flight and base-branch logic actively get in the way, and the failure mode is a worktree branched from `main` that silently discards the review feedback it was meant to address.

## 10. Failure modes

| Situation | Behaviour |
|---|---|
| `wt init` fails during create | report it; the worktree survives un-initialised (M4 §4), and say so explicitly rather than implying an environment exists |
| `wt rm` exits 3 | stop and ask; never work around it |
| `wt rm` exits 4 | report what could not be checked; do not proceed on a missing safety check |
| Guard cannot classify cwd | fail open and say so once — a guard that denies on its own errors makes the session unusable |
| Descriptor missing when rendering a briefing | refuse to render; a briefing with guessed values is worse than none |

## 11. Open questions

- Is a subprocess per tool call an acceptable guard budget, and does session-scoped caching hold when a worktree is removed mid-session?
- How is skill drift detected when a repo's spec changes after generation? Nothing currently notices that a skill names a port band that moved.
- These artefacts are Claude-Code-shaped. What does the surface look like for an agent that has no hooks and no skills — is the briefing block the whole answer?
- Should the guard deny writes to the *spec* itself from inside a worktree? Editing it there changes allocation for every worktree, and the change is invisible until the next `init`.
- Does the tripwire belong in the repo's committed settings, where every user of the repo gets it, or in the user's own, where it is a personal choice? Committing it makes an opt-in decision on behalf of everyone who clones.
