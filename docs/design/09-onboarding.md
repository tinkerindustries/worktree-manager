# M9 — Onboarding

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** repo adoption, spec authoring, artefact generation, B17.4, C1/C2/C3/C4/C7/C8/C9 as decisions, and the requirements' open question 6.
**Not part of the binary.** This module is a Claude skill, per overview §2.3.

## 1. What this module answers

How a repository goes from having no worktree isolation to having it, once.

Everything else in this design is a repeated operation performed by a deterministic binary. This is a one-time, judgment-heavy, conversational task that ends by writing code into someone's repository, which is the opposite kind of work.

## 2. The division of labour

`wt` performs no inference. It exposes facts and refuses specs it cannot honour; the skill decides.

| Primitive | Returns |
|---|---|
| `wt ports scan` | what is listening on this machine now |
| `wt bands list` | current ledger, including reservations |
| `wt bands suggest --spec <file>` | required size computed from the spec, and free bases that fit |
| `wt bands reserve <app> <bases>` | claims them |
| `wt spec validate <file>` | schema and constraint check, with reasons |
| `wt spec explain <file> --slot N` | exactly what that spec allocates at slot N, allocating nothing |
| `wt doctor --json`, `wt list --json` | state |

`spec explain` is the one that makes the conversation concrete. Showing a developer the resource table for slot 1 and slot 2 side by side is what surfaces a missed collision before anything is built.

## 3. The sequence

### Phase 1 — Audit

Find every resource two concurrent worktrees would contend for.

Scannable, and the skill should scan rather than ask:

- committed default ports, in config defaults, `ports:` entries, `.env.example`, and constants;
- compose files: whether any pins `name:` (D2), and which volumes and networks are not project-scoped — B2.1 makes the audit about finding what *is not* project-scoped, since the project name isolates the rest for free;
- the package manager, and whether its dependencies live in the tree (T9);
- a separate test stack (T3);
- `$HOME` paths in source — `UserHomeDir`, `~/`, `%USERPROFILE%` (T6);
- unix sockets and lockfiles;
- what is currently listening, via `wt ports scan`.

Not scannable, and the reason this is a skill:

- which shared resources matter, and the blast radius of each (B11.4);
- whether the shared state is something the worktree's user *wants* to reach, which is C1's actual driver;
- leases and leader election (T7), which grep finds inconsistently;
- a co-resident production stack (T8);
- external namespaces — a third-party API tenant, a cloud resource, a piece of hardware — which nothing in the repository mentions.

The phase ends with a table the developer confirms row by row: each resource marked isolate, share, or reserve. Confirmation matters more than completeness here, because a resource the developer silently disagrees about will be wrong in the spec and correct in their head.

### Phase 2 — Policy

Part C's decisions, as an interview. Each has a driver question the requirements already identified:

| Decision | Question |
|---|---|
| C1 which dimensions isolate by default | is the shared state something the worktree's user wants to reach? |
| C2 seeding mode | is the work *on* the store's schema, or merely *through* it? |
| C3 descriptor format | what does this repo already parse? |
| C4 config delivery | does every entry point read the environment, or do some resolve natively? |
| C7 base branch | does this repo have stacked or dependent work? |
| C8 cleanup posture | how expensive is an idle worktree? |
| C9 slot ceiling | resources per slot, times how many run at once |

C5 is no longer a repo decision. Locking is the tool's, settled in M2 §8.

Part C's framing is that these are choices to make consciously and record, not defaults to standardise. So this phase produces a short decision record alongside the spec — the answers and the reasons. Six months later the spec says what was chosen and only the record says why.

### Phase 3 — Bands

`wt bands suggest`, developer confirms, `wt bands reserve`.

This step is machine-local by design (M3 §3.2), which gives onboarding a tail worth naming explicitly: a colleague who clones an already-adopted repo, or the same developer on a second machine, has to reserve bands there too. It is small and repeatable and it is not part of adoption, so it needs its own documented one-liner rather than a re-run of this skill.

### Phase 4 — Spec

Write it, `wt spec validate`, then `wt spec explain --slot 1` and `--slot 2` and read both tables against the phase 1 inventory.

### Phase 5 — Generate

- the descriptor reader in the repo's own language, where C4 said native (M5 §4.2);
- the two skills, the `SessionStart` tripwire, and the guard hook where C6 said so (M7);
- the reference doc and the `CLAUDE.md` tripwire (A17);
- the committed `.gitignore` line for the descriptor (M5 §2.2), which is this phase's job precisely so that `init` never has to touch a tracked file.

### Phase 6 — Patch entry points

B17.4: find the files inside the repo that hardcode a port and make them descriptor-aware. Harness needed exactly two — `web/vite.config.ts` and `scripts/test.sh` — and documented why each.

B14.2 widens it: every entry point resolves the same environment, including the ones that are not shell commands. GUI binaries, MCP subprocesses, editor hooks. bacio's desktop binary needed a `--db` flag it had never had, plus `--env`, purely so it could be steered like the CLI.

This is the phase most likely to be left incomplete, and the one where the skill has to enumerate entry points rather than sample them. A single missed entry point produces a worktree that is isolated for every path the developer tested and shared for the one they did not.

### Phase 7 — Prove it

Two worktrees, side by side, at the same time.

Make two worktrees by whatever normally makes them, `wt init` both, start both, confirm both healthy simultaneously, confirm the two resource tables are disjoint, then tear both down and confirm `wt doctor` is clean.

Everything before this phase is a claim. This is the only step that tests whether the phase 1 audit was complete, and a failure here sends the work back to phase 1 rather than to a patch — a resource that collides at this point is a resource the inventory missed.

### Phase 8 — Import what already exists

Where the repo has worktrees already, or a per-repo implementation being retired, adopt rather than recreate. B1.5's back-derivation earns its keep here: an existing worktree's ports imply its slot, so the entry can be reconstructed without disturbing anything running.

Retire the old implementation after the import, not before. A half-migrated repo with two allocators is worse than either.

## 4. What onboarding cannot infer

Stated plainly because the failure mode is delayed. A resource missed in phase 1 does not fail during onboarding. It fails weeks later, in a second worktree, as a bug that looks unrelated — a port in use, a container attached to the wrong project, a row that appears in a database nobody wrote to from here.

The requirements' open question 6 asked whether a tool could infer the trait list by scanning. It can draft the scannable half. The half that decides whether the result is correct — blast radius, whether shared state should be reachable, what lives outside the repository — comes from the developer, and no amount of scanning replaces the confirmation step in phase 1.

## 5. Drift

M7 asks how skill drift is detected when a spec changes after generation. The answer belongs here, since this module writes the artefacts.

Every generated file carries a managed block, using the same convention as M5 §3.1's `.env`, recording the spec fields it was generated from. `wt doctor` compares the recorded inputs against the current spec and reports a mismatch, naming the file and the field that moved.

That makes a moved port band or a renamed resource a `doctor` finding rather than a stale sentence in a skill that nobody reads closely. It also means hand edits outside the managed block survive regeneration, which they need to — a generated skill will be edited.

## 6. Failure modes

| Situation | Behaviour |
|---|---|
| Repo already has per-repo worktree tooling | adopt alongside it, import in phase 8, retire it after — never mid-flight |
| A production stack's ports cannot be cleared | reserve them host-globally (M2 §7); do not design around them |
| Spec validates but phase 7 collides | back to phase 1; the audit was incomplete and the spec is a symptom |
| Developer declines to confirm a phase 1 row | leave it out of the spec and into the hand-authored `shared` block, which is the honest place for "we know about this and are not isolating it" |
| No band space free | `doctor`'s ledger view, and a conversation about what on the machine is reserved and no longer needs to be |

## 7. Open questions

- Where does the phase 2 decision record live — a doc in the repo, or comments in the spec? The spec is machine-read and wants to stay terse; the reasons are what get lost.
- Should this skill be safely re-runnable against an adopted repo, to pick up a newly added resource? Phases 1, 4, 5 and 7 are idempotent; phase 3 and phase 6 are not.
- How much of phase 6 can be verified rather than asserted? Enumerating entry points is currently an audit, and audits go stale — M5 already asks where a repo declares its entry points.
- Does the generated reference doc belong in the repo or does it duplicate `wt`'s own documentation? The repo-specific facts are the point, but the slot model and teardown ordering are the same everywhere.
