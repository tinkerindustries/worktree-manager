# Worktree Manager — Implementation Plan

**Status:** plan of record. Nothing is implemented yet.
**Date:** 2026-08-12
**Inputs:** [`docs/ARCHITECTURE.md`](docs/ARCHITECTURE.md) revision 2, [`docs/worktree-tooling-requirements.md`](docs/worktree-tooling-requirements.md), and the nine module designs in [`docs/design/`](docs/design/).

## 1. How to read this plan

Ten phases, each one a working state rather than a layer. A phase ends when its exit criteria hold, and every criterion is something that either passes or fails when someone runs it.

The phase order comes from [`docs/ARCHITECTURE.md` §13.2](docs/ARCHITECTURE.md), which pins both ends of the sequence: nothing can be onboarded until the spec schema is settled, and the protocol has to exist before anything crosses the socket. This plan adds a phase in front of that sequence for the schema itself, and one behind it for packaging.

Phases 0 to 5 are a straight line. Phase 5 is the first release worth using: one repo, one platform, two worktrees side by side.

## 2. Which document is authoritative

`docs/ARCHITECTURE.md` revision 2 wins on every point of conflict. It replaced the coordination model, and the nine module documents predate that change.

Superseded, listed here so an implementer knows which text to distrust:

| Document | Section | What replaced it |
|---|---|---|
| `00-overview` | §6 | The coordinator, not `WT_HOME` mounted into a container |
| `01-identity` | §5 | Client identity on the socket, not view identity |
| `02-coordination` | §8, §9, §12 | One writer. No lock file, no generation counter, no view scoping |
| `03-drivers` | §2.3 | Operations run in the coordinator's namespaces, so most report `unavailable` far less often |
| `06-fleet` | §6 | Ephemeral clients and reclamation, not view adoption |
| `08-platform` | §5 | Capability probes run in the coordinator |

Three consequences worth stating directly, because they delete work the module documents describe in detail. There is no lock file and no generation counter — concurrent requests serialise in the coordinator. There is no view id — `owner` and `ephemeral` on the registry entry do that job, and `path_visible` records what the coordinator can stat. `WT_HOME` is not a container mount point; a container mounts the socket alone.

[`03-drivers.md`](docs/design/03-drivers.md) remains authoritative for the spec schema. On every other mechanism the module documents are correct.

## 3. Conventions that hold in every phase

These are contract, not style. A phase that breaks one is not finished.

- **Exit codes are 0, 1, 2, 3, 4, 5** per `ARCHITECTURE.md` §11.3. This supersedes M4 §8's set of 0 to 4; code 5 is coordinator unreachable, separated from code 4 because the remedies differ.
- **`unavailable` is a third result**, distinct from success and failure. An unavailable probe does not block allocation. An unavailable teardown does block freeing the slot.
- **Every error names the command that fixes it.** Every `doctor` finding too. A report that lists problems without remedies gets read once.
- **Bounded coverage is stated.** A skipped hook, a fallback to a shared pool, a slot cap, a truncated name: each says so and names the remedy. A silent degrade reads as success.
- **The system refuses rather than partially honouring.** A spec declaring unsupported features is refused whole. Client and coordinator whose protocol versions do not overlap refuse and name the upgrade.
- **A teardown handle comes from the registry, never from the working tree.** The directory is routinely gone by the time teardown runs.
- **Neither binary performs inference.** All judgment lives in the phase 7 skill.
- **Hooks never run in the coordinator.** They need the worktree, and running arbitrary repo commands in the one privileged process would give every adopted repo the coordinator's authority.
- **Every verb offers `--json`.** The skills consume all of them. `reconcile --dry-run` is required rather than optional.

## 4. Testing approach

Four layers, established as the code that needs them lands. `TESTING.md` carries the detail.

| Layer | Covers | Needs |
|---|---|---|
| Pure unit | Slug rules, template evaluation, band arithmetic, `.env` block editing | Nothing |
| Real-repo fixtures | Classification, root resolution, containment | Real git repos in `t.TempDir()`, never mocked git output |
| In-process coordinator | Allocation, authorisation, entry lifecycle, migration | A store path and protocol messages, no socket and no supervisor |
| Live acceptance | The two gates in §6 | Docker, a real supervisor, in phases 5 and 6 |

Classification is tested against real repositories because the bug M1 exists to prevent was a misreading of what git reports. A test built on a mock of the same misreading passes while the bug survives. The fixtures: a plain repository, a repository with two linked worktrees, a clone, a worktree whose directory has been removed, and one of each reached through a symlink.

Each driver runs the same contract conformance suite, so a new driver is a fixture rather than a new test file.

## 5. The phases

### Phase 0 — Foundations, and the spec schema

The schema is the contract between one conversational plane and two deterministic ones. Every generated artefact, every driver and every adoption decision resolves through it, and `ARCHITECTURE.md` §14.2 says it should carry the most review pressure before implementation starts. So it comes first, and it gets proved against real repositories rather than reviewed in the abstract.

**Build.** One Go module, two entry points under `cmd/wt` and `cmd/wtd`, internal packages per module. `CGO_ENABLED=0` cross-compilation to macOS, Linux and Windows in CI, with lint and `go test ./...`. The spec parser and validator as a library both binaries link: types, template evaluation by topological sort with cycle detection, per-type constraint checks, and refusal of a version it does not understand.

**Exit criteria.**

- Specs for `bacio`, `deepseek-harness` and `mini-infra` are written as fixtures and validate. Anything the schema cannot express is a schema change, made now.
- `spec explain --slot 1` and `--slot 2` produce resource tables from a pure function, and the two tables are disjoint.
- A template cycle, an unknown cross-resource reference, and a resolved name exceeding the length caps are each rejected at validation with the reason named.
- CI produces binaries for all three platforms.

**Docs.** `CLAUDE.md`, root `ARCHITECTURE.md` skeleton, `TESTING.md`.

### Phase 1 — Identity, containment, and `guard`

M1 and the client half of M8. Nothing here touches a coordinator, which is why it goes first: `guard` and `show` are useful on their own.

**Build.** Classification into not-a-repository, primary checkout, linked worktree and standalone clone. Root resolution from `--show-toplevel`, with the main checkout path reachable only through the classification result. Symlink-resolved, segment-wise containment. Slug validation. Descriptor location. `wt guard --json` and `wt show`, both local, both working with no coordinator installed.

Path realisation and the mount's case sensitivity sit in the platform package. Case sensitivity is probed at the mount, not assumed from `GOOS`, because a case-sensitive volume on macOS is supported and this is the one platform detail carrying a security property.

Drop M1 §5 entirely. View identity does not exist in revision 2.

**Exit criteria.**

- The six fixtures in §4 classify correctly, including through symlinks.
- `guard` denies a write to the primary checkout from a linked worktree, and the denial names the correct root verbatim.
- `guard` denies reading a `CLAUDE.md` outside the worktree.
- `guard` fails open on pipes, `$(...)`, variables and globs, and emits one deny per call.
- `contains("/a/b", "/a/bc")` is false. On a case-insensitive mount, a differently-cased path inside the tree is contained.

**Docs.** `TESTING.md` gains the fixture layer.

### Phase 2 — The coordinator skeleton and the protocol

Nothing crosses the socket until this exists.

**Build.** `wtd` as a resident per-user process. Unix socket on macOS, registered as a launchd LaunchAgent with socket activation. The request protocol, with version negotiation that refuses a non-overlapping pair and names the upgrade. Client identity: peer credentials for host clients, a configured token for named containers, an ephemeral declaration for disposable ones, recorded in `clients.json`. `wt daemon status` and `wt daemon install`. Exit code 5 wired through every verb except `show` and `guard`.

**Exit criteria.**

- `wt daemon status` distinguishes not-registered, registered-but-stopped, and running-but-unreachable, and names a different fix for each.
- Every verb other than `show` and `guard` exits 5 with the coordinator stopped, naming the start command. `show` and `guard` still work.
- A client one protocol version ahead of the coordinator refuses to proceed, from both directions.
- The in-process harness runs a full request without a socket, a supervisor or a container.

**Docs.** `RELEASE.md`, carrying the version compatibility policy for the three independent versions — protocol, registry schema, spec.

### Phase 3 — Coordination core

M2, minus everything the coordinator model deleted.

**Build.** The store at `~/.wt`, directory `0700` and files `0600`, written by the coordinator as the user. Atomic writes: temp file in the same directory, fsync, rename, fsync the directory. Registry entries carrying `owner`, `ephemeral`, `path_visible`, `state`, denormalised `resources` and a named `secrets` field. Lowest-free slot allocation per app, skipping exclusions and anything probing held, with an existing entry's slot authoritative. The band ledger, with registration explicit and required. Exclusions from both the spec's `reserved` block and the ledger's host-global reservations. The authorisation check on every mutating call. Entry states `reserving`, `active` and `tearing-down`, with `reserving` aged out on the coordinator's timer. Registry schema migration under the same atomic path.

Not built, and not to be added later: a lock file, a generation counter, view identity, view scoping.

**Exit criteria.**

- Two concurrent allocations against one app get different slots, tested in-process.
- A mutating call against an entry owned by another client is refused, naming the owner and its last-seen time.
- Allocation for an app with no registered band refuses and names the registration command.
- The slot ceiling message names the range, the cleanup command, and how many occupied slots the caller cannot free.
- A port in the ledger's reservations is never allocated, and neither is one in the spec's `reserved` block.
- A registry written by a newer schema version lists but does not write.

**Docs.** Root `ARCHITECTURE.md` gains the store layout and the authority rules.

### Phase 4 — Drivers: `port` and `namespace`

**Build.** The six-operation contract, with `derive`, `probe`, `verify` and `blastRadius` required and `apply` and `teardown` independently optional. `port` in both stride and group form, with the stride form capping slots at 99 and `spec validate` enforcing that. `namespace` for compose: derived project name, probe by label, teardown by label in container-network-volume order across the project and every dependent project the spec declares.

Two rails are structural rather than advisory. A resolved namespace matching a host-global reservation is refused, because label-based teardown is the one operation that could otherwise reach a co-resident production stack. A held port is never remediated: the allocator skips the slot and says which slot it skipped.

`SO_REUSEADDR` inverts between platforms. Set on unix, unset on Windows. The platform package owns it and no caller sees the branch.

**Exit criteria.**

- Teardown succeeds with the worktree directory deleted first.
- `verify` reports a compose file that pins `name:`, which is the finding that catches the silent-attach failure.
- A teardown that leaves resources behind moves the entry to `tearing-down` and does not free the slot.
- A namespace resolving to a reserved name is refused, naming the reservation.
- The probe run from the coordinator sees a port published to the host by a container.
- Teardown continues past a failure and reports everything that survived.

**Docs.** Root `ARCHITECTURE.md` gains the driver table and the discover-rather-than-create rule.

### Phase 5 — Lifecycle and delivery: a usable tool

M4 and M5. This phase produces something worth installing.

**Build.** `init` as the seven-step sequence with rollback covering everything up to activation, and the four attach outcomes that make it the repair path as well as the setup path. `start`. `rm`, with three safety checks in the client — uncommitted changes, unpushed commits with an absent upstream itself a stop, an open PR — and reap plus teardown in the coordinator. The reaper's rails, all of them, enforced coordinator-side. Hooks: install, prepull, build, seed, health, with cwd at the worktree root, output to stderr, non-zero stopping the sequence, and `--dry-run` printing the resolved command.

Delivery: the descriptor written atomically at the worktree root, undotted, in the repo's format, with YAML emission quoting every string scalar. The gitignore line via `$GIT_COMMON_DIR/info/exclude` when adoption did not commit it, never by appending to a tracked `.gitignore` in a linked worktree. The `.env` managed block, with any managed key defined outside the block stripped rather than ordered. First-`init` seeding of `.env` from the main checkout, unmanaged content only. The generated descriptor reader for Go, honouring the four-row resolution table and reading nothing but the descriptor and the spec.

**Exit criteria.**

- Two worktrees of one repo run side by side, both healthy at once, with disjoint resource tables.
- Both tear down cleanly, leaving no containers, no volumes and no registry entries.
- A failed `init` at materialisation leaves the tree exactly as it was found, and no entry.
- A failed health check leaves the worktree allocated and usable, and says what failed.
- `rm` works from outside the tree, by slug, with the directory already deleted.
- `rm` stops with exit 4 when `gh` is missing. A safety check that cannot run fails closed.
- The application's reader refuses to start in a linked worktree of an adopted repo with no descriptor, naming `wt init`.
- `.env` with a managed key defined above the block comes back with one definition, and the output says which key was stripped.

**Docs.** All five candidate documents reassessed. `TESTING.md` gains the acceptance layer.

### Phase 6 — Fleet

M6. Orphaned entries are routine rather than exceptional, because the tool that created a worktree removes it and will not call `wt rm` first.

**Build.** `list` across every repo, with stale and unverifiable kept as distinct markers and secrets redacted by default. `doctor`, reading everything and writing nothing, every finding naming its fix. `reconcile`, applying `init`'s repair path to entries, acting on the caller's own entries plus ephemeral entries whose owner has aged out, with `--dry-run` required. Reclamation of ephemeral clients on a configured interval measured against real last-seen times. `wt clients`.

**Exit criteria.**

- **Acceptance gate 1** in full: two worktrees side by side, both healthy, tables disjoint, both torn down, `doctor` clean.
- **Acceptance gate 2**: a container client and a host client allocate against the same app concurrently, and the container's slot is unavailable to the host.
- Deleting a worktree directory by hand and running `reconcile` tears the resources down and drops the entry.
- An entry whose path is not visible to the coordinator reports as unverifiable, never as stale, and `cleanup` skips it.
- Every `doctor` finding in M6 §4 is produced by a fixture and names a command.

**Docs.** Root `ARCHITECTURE.md` gains the orphan lifecycle.

### Phase 7 — Adoption: the onboarding skill and the generated artefacts

M9 then M7. The first phase where a second repo can be adopted.

**Build.** The primitives the skill reads: `ports scan`, `bands list`, `bands suggest`, `bands reserve`, `spec validate`, `spec explain`. The onboarding skill's eight phases, ending with two worktrees running side by side, because everything before that step is a claim. The generated artefacts: `worktree-create` and `worktree-remove` skills, the `SessionStart` tripwire, the opt-in `PreToolUse` guard hook calling `wt guard`, the reference doc, and the `CLAUDE.md` tripwire.

Every generated file carries a managed block recording the spec fields it came from, so `doctor` reports drift when a band moves or a resource is renamed, and hand edits outside the block survive regeneration.

**Exit criteria.**

- A repo none of the three source implementations covers is adopted end to end, and its phase 7 proof passes.
- Moving a band and re-running `doctor` reports the generated file and the field that moved.
- Regenerating an edited skill preserves the edits outside the managed block.
- `wt rm` exiting 3 makes the remove skill stop and ask rather than reach for `--force`.
- The briefing block refuses to render with the descriptor missing.

**Docs.** A nested `CLAUDE.md` in the template directory. A path-scoped rule making `internal/platform` the only package permitted to branch on `GOOS`.

### Phase 8 — `machine`, `cidr`, `cleanup`, and the remaining platforms

**Build.** The `machine` driver: Colima profiles on macOS, WSL2 distros on Windows, with the capacity guard refusing a new instance past the limit and naming what is running, background warm-up outside the request, `--keep-vm`, and the documented bypass supplied by the driver so the doc cannot drift. `cidr`, slicing a pool by slot, with `on_exhaustion` falling back loudly or failing. `cleanup`, the only verb that destroys a worktree unattended, gated on `gh` and applying the full `rm` checks even when the PR is merged. The scheduled sweep on the coordinator's own timer, more conservative than the interactive verb and logging every skip.

Platforms: the Linux systemd user unit with a paired socket unit, and the lingering caveat. Windows named pipe, and the service-versus-logon-task decision. `taskkill` reporting which of the two paths it took, because graceful termination is unreliable there. The Windows ACL path refusing to write credentials where the ACL cannot be set.

**Exit criteria.**

- A VM-per-worktree repo runs two worktrees concurrently on macOS, and the capacity guard refuses the fifth.
- `cleanup --dry-run` previews exactly what the real run does, and `cleanup` with `gh` unavailable cleans nothing and exits 4.
- Phases 1 to 6 pass on Linux and on Windows.
- On Windows, a state directory whose ACL cannot be set refuses to hold credentials rather than writing them world-readable.

### Phase 9 — Packaging and 1.0

**Build.** One distribution per platform containing both binaries, built from the one module. Installation registers the coordinator with the platform's supervisor and starts it. The coordinator upgrade path while clients are mid-operation, which the protocol negotiation does not cover on its own. Loopback TCP with a token, opt-in, for hosts where a socket cannot be shared into a container. A security pass over the socket surface, which is the whole attack surface of the most privileged component on the machine.

**Exit criteria.**

- A clean machine installs from a release artefact, adopts a repo, and passes both acceptance gates.
- Upgrading the coordinator while an entry is `reserving` leaves that entry recoverable.
- `wt list` from an agent container cannot read the host user's seed credentials.
- The second-user gap is documented rather than silently present.

## 6. Acceptance gates

Two live tests matter more than the unit coverage, and both come from `ARCHITECTURE.md` §13.2. They are gates, not smoke tests: a phase that regresses one is not finished.

**Gate 1.** Two worktrees run side by side at the same time, both healthy, resource tables disjoint. Both tear down. `doctor` is clean.

**Gate 2.** A container client and a host client allocate against the same app concurrently, and the container's slot is unavailable to the host.

Gate 1 is partly reachable at phase 5 and complete at phase 6. Gate 2 needs ephemeral clients and reclamation, so it lands at phase 6. Both run in CI from that point on.

## 7. Documentation maintained alongside

Five documents carry the repository's working knowledge, per the `repo-docs` convention. Each phase updates them in the same change that lands the code.

| File | Warranted | Holds |
|---|---|---|
| `CLAUDE.md` | yes | What the repo is, build and test commands, the two-binary split, the no-inference rule, links to the rest. Kept short. |
| `ARCHITECTURE.md` (root) | yes | The as-built codemap: internal packages, which of them may import which, the invariants in §3 of this plan, and the gotchas. Links to `docs/ARCHITECTURE.md` for the target design. |
| `TESTING.md` | yes | The four layers in §4, where each lives, how to run one test, and which layers need docker or `gh`. |
| `RELEASE.md` | yes | Versioning, the cross-compile matrix, the compatibility policy for the three independent versions, the installer, and rollback. |
| `HOSTING.md` | no | Nothing is deployed. The coordinator installs onto a developer's machine, which is installation and belongs in `RELEASE.md`. Revisit only if something hosted appears. |

Two documents will be named `ARCHITECTURE.md`. The root file is the as-built structure and changes every phase. `docs/ARCHITECTURE.md` is the architecture of record for the system being built, is referenced by name from all nine design documents, and does not move. The alternative — renaming the design document — costs a dozen cross-references and is available if the pair turns out to confuse people.

Three rules for these files, applied from phase 0:

- No phase narration. `CLAUDE.md` states what is true now. This plan and the git history record what happened.
- No measurements. Test counts, coverage percentages and pinned versions go stale silently. Port numbers, schema versions and the slot ceiling are decisions, and they stay.
- Every command and path traces to something in the repository. A gap is marked TODO rather than filled with what a repo like this usually does.

Nested `CLAUDE.md` files come later and only where a project has its own rules: the M7 template directory in phase 7 is the first genuine case.

## 8. Risks, and where each is answered

`ARCHITECTURE.md` §14.1 carries twelve open risks. Each needs an answer by a specific phase.

| | Risk | Answered in |
|---|---|---|
| R1 | Coordinator upgrade while clients are mid-operation | Phase 9 |
| R2 | Windows service under the user account, or a logon task | Phase 8 |
| R3 | Reclamation interval for an ephemeral client | Phase 6 |
| R4 | Is refusing to run in an un-initialised worktree too blunt | Phase 5, from the first real use |
| R5 | Band ledger shared across machines | Phase 3, decided by deferring |
| R6 | Who reserves a production stack's ports when no repo has a spec | Phase 3 |
| R7 | Generated reader as a published per-language library | Phase 5 |
| R8 | The agent surface without hooks and skills | Phase 7 |
| R9 | Does `start` need a `stop` | Phase 5 |
| R10 | Resources that must move together beyond the port-group case | Phase 4 |
| R11 | Where the adoption decision record lives | Phase 7 |
| R12 | Two users on one machine | Phase 9, documented rather than solved |

## 9. Decisions needed before their phase starts

- The Go module path, and whether the repository name is final. Phase 0.
- Whether the three fixture specs are committed to this repository or kept alongside each source repo. Phase 0.
- The reclamation interval's default. Phase 6, and R3 says the coordinator now has a real signal to set it from.
- Whether the guard hook caches its classification per session, and what happens when a worktree is removed mid-session. Phase 7.
- Whether the tripwire hook belongs in a repo's committed settings or the user's own. Phase 7 — committing it makes an opt-in decision for everyone who clones.
