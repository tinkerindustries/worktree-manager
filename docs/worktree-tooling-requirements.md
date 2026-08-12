# Per-Worktree Environment Tooling — Requirements

**Status:** requirements gathering only — no design, no implementation.
**Date:** 2026-08-11

## How to read this document

Requirements here are **trait-triggered, not universal**. Each application isolates a different set of resources because each application *uses* a different set of resources, and each makes different policy calls about what to isolate by default. A requirement that doesn't appear in one repo is almost never a gap — it's a requirement that repo doesn't have.

So the document is organised in five parts:

- **Part A — Core invariants.** True for any repo doing per-worktree isolation at all, regardless of stack. This is the small shared spine.
- **Part B — Trait-triggered requirements.** Grouped by the property of the app that creates the need. Read only the traits your app has.
- **Part C — Divergent policy choices.** Axes where the three repos chose differently and *all three are right*. These are decisions, not defaults to standardise.
- **Part D — Hard-won lessons.** Specific incidents and the rules they produced. Worth reading regardless of traits, because the failure modes generalise even when the resources don't.
- **Part E — Trait profiles.** What each of the three repos actually is, so the reasoning behind its choices is legible.

**Sources** — the three existing implementations in `~/Repos`:

| Repo | Stack | Implementation | Reference doc |
|---|---|---|---|
| `bacio` | Go CLI + Wails desktop + shared SQLite | `bacio worktree <verb>` (`internal/wtenv`, `internal/cli/worktree.go`), `scripts/new-worktree.sh` | `docs/worktree-environments.md`, `docs/agent-dispatch.md` |
| `deepseek-harness` | Go + NATS + docker compose + Vite | `harness worktree <verb>` (`internal/worktree/*`, `cmd/harness/worktree.go`), skills `worktree-create` / `worktree-remove` | `docs/WORKTREES.md` |
| `mini-infra` | pnpm monorepo + Colima/WSL2 VM + docker compose + Vault/NATS/HAProxy | `pnpm worktree-env <verb>` (`deployment/development/worktree-*.ts`), skills `setup-worktree` / `finish-worktree` | root `CLAUDE.md`, `docs/user/{colima,wsl2}-reference.md` |

---

## 1. Problem statement

Sibling git worktrees are *file*-isolated by git and nothing else. Every non-file resource the app touches stays shared, so the second worktree to start either fails loudly or — worse — silently attaches to the first one's state. The concrete collisions across the three repos:

- **Ports.** `bacio api` in worktree B can't bind because A holds `127.0.0.1:5320`. The second harness worktree can't bind 4222/8080/8090.
- **Container namespaces.** A compose file that pins `name:` makes the second worktree *quietly attach to the first one's containers* instead of erroring.
- **Shared state and leases.** Two bacio desktop processes raced on the `ui_leader` lease in `~/.bacio/db.sqlite`; the dispatch matcher, idle pinger and prune loop ran in two places at once.
- **Host daemons.** Multiple dockerds carve bridge subnets from one address pool: past ~4 concurrent mini-infra WSL distros, network creation fails with `all predefined address pools have been fully subnetted`, surfacing as a stuck dataplane sync.
- **Network address space.** Concurrent worktrees allocating egress /24s from one `172.30.0.0/16` collide.
- **Co-resident production.** `deepseek-harness-prod` runs on 8180/8190/4522/8522 on the dev machine and must never be touched.

Per-invocation escape hatches (`--db`, `--port`, `COMPOSE_PROJECT_NAME=`) technically solve this, but nobody threads them through every command, so in practice worktrees stomp on each other. **The tooling exists to make isolation declared once per worktree and honoured automatically by every entry point.**

Secondary driver across all three: **parallel agent sessions**. An agent can't be trusted to remember which port, DB or branch it owns, so the environment must be self-describing and its boundaries enforced rather than documented.

---

## 2. Out of scope

Rejected deliberately in the existing implementations — recorded so they don't get re-proposed:

- **Isolating the host docker socket** — would mean Docker-in-Docker per worktree.
- **Isolating content-addressed caches** (Go module cache, npm/pnpm store) — read-mostly; isolating multiplies setup time for no benefit.
- **Serialising instead of isolating** (a lockfile or mutex on the shared store) — the user *wants* side-by-side, not a queue.
- **Auto-creating an environment on first mutation** — fragments state when the user just wanted a quick branch. Isolation must be an explicit act.
- **A named-profile indirection** (`$APP_PROFILE` → `~/.app/profiles/<name>/`) — moves the source of truth away from the working tree and makes "which profile am I on?" a question the user answers instead of one the tool answers from cwd.

---

## 3. Glossary

| Term | Meaning |
|---|---|
| **Main / primary checkout** | The original clone. Treated as **slot 0**, left on the committed defaults, never allocated or managed. |
| **Linked worktree** | A `git worktree add` tree. The unit of isolation. |
| **Slug** | Short kebab-case identity, `^[a-z0-9][a-z0-9-]*$`. Defaults to the worktree directory basename — not the branch, which gets renamed and deleted. |
| **Slot** | Small integer every allocated resource derives from. One number, many resources. |
| **Descriptor / manifest** | Per-worktree file at the worktree root recording that worktree's allocation. Gitignored. |
| **Registry** | One host-global file recording every initialised worktree. The coordination point. |
| **Trait** | A property of the application that creates a requirement (e.g. "binds host ports", "runs docker compose"). |

---

# Part A — Core invariants

These hold for any repo doing per-worktree isolation, whatever it isolates.

**A1. One CLI, one verb set.** A single entry point in the repo's own stack (`bacio worktree`, `harness worktree`, `pnpm worktree-env`) covering: `init`/`start`, `list`, `show`, `rm`/`delete`. `init` is **idempotent** — a re-run reconciles the existing allocation and rebuilds; it never errors and never reallocates.

**A2. The main checkout is slot 0 and is never managed.** Detect it as `git rev-parse --git-dir` == `--git-common-dir`. It keeps the committed defaults, so nothing changes for anyone who never creates a worktree.

**A3. Opt-in, with byte-identical behaviour when absent.** No descriptor ⇒ legacy behaviour exactly. "Descriptor not present" is a fall-through, never an error.

**A4. One slot integer drives everything.** Every allocated resource derives from it, so the footprint is enumerable from a single number and readable at a glance. (Harness makes the last two digits of every port equal the slot: slot 7's ports all end in `07` — legible straight out of `docker ps` or `lsof`.)

**A5. Allocation is lowest-free and stable.** An existing entry's slot is authoritative and is never reassigned under a running worktree.

**A6. The registry is host-global, outside every worktree.** Repo-local is wrong: a linked worktree can't see a sibling's files, and coordinating *across* worktrees is the entire job. (`~/.bacio/worktrees.yaml`, `~/.deepseek-harness/worktrees.json`, `~/.mini-infra/worktrees.yaml`.)

**A7. The registry is a cache; the per-worktree descriptor wins on disagreement.** Rebuilding the registry from descriptors must always be safe. Support format migration in place (mini-infra migrated `worktrees.json` → `worktrees.yaml`).

**A8. The descriptor lives at the worktree root, and is auto-gitignored.** Not buried in a dot-directory — `ls` should show it. `init` appends the ignore line idempotently, and works when `.gitignore` is itself untracked. It is per-worktree, per-machine state; committing it would carry one user's allocations into another's tree.

**A9. Resolution does not depend on the recorded path still matching.** The absolute path in the descriptor is informational, so a worktree can be moved.

**A10. Resolve via the *linked worktree's own* root.** `git rev-parse --show-toplevel`, deliberately different from any "walk back to the main checkout" helper the repo has for repo identity. Getting this backwards had `init` from a linked worktree silently clobber the main worktree's manifest.

**A11. Everything downstream reads the descriptor.** Docs, skills, agents and scripts say "read it back with `<tool> show`", never "the UI is on 3100". This is the single most repeated instruction across all three repos.

**A12. Ordering is enforced by tooling, not memory.** Create git worktree → `init` → start. On teardown, `rm` **before** `git worktree remove`, because teardown needs the registry entry the removal would orphan.

**A13. Failed `init` rolls back.** No half-set-up state survives (bacio's `new-worktree.sh` removes the git worktree it just created if `worktree init` fails).

**A14. Writes are atomic.** Descriptor and registry both: temp file in the same directory, then rename, so a crash mid-write can't truncate.

**A15. Structured output on the mutating and listing verbs.** `list` as a table plus `--json`; mutating verbs support `--dry-run`. Where the repo has agent-CLI conventions, worktree verbs follow them rather than inventing their own.

**A16. Slug validation is shared.** One regex, enforced at the boundary, used by the registry, the compose project names and the descriptor filename alike.

**A17. One reference doc + one `CLAUDE.md` tripwire.** The doc covers why the tooling exists (the concrete collisions), the slot model and resource table, what `init` writes, what stays shared and why, the registry, the resolution chain and the teardown ordering. The tripwire is the one rule an agent needs in its head; bar for inclusion is "would a model that hasn't read the linked doc do the wrong thing?"

---

# Part B — Trait-triggered requirements

Read only the traits your app has.

## T1 — The app binds host ports

**B1.1** Allocate ports in per-service bands chosen to clear (a) the repo's committed defaults, (b) any production stack on the machine, (c) whatever else the machine had listening when the bands were picked.

**B1.2** Where ports come in fixed relationships, allocate the **group**, keeping groups disjoint. bacio reserves an adjacent pair (`api_port` and the reverse-proxy port at `api_port − 1`) so no worktree's API port lands on another's proxy port.

**B1.3** Probe live before committing a slot: bind on loopback, release immediately, skip the slot if something outside the registry holds it. Document the limitation — inside a container the probe binds a different netns than the host ports compose publishes to, so the *registry* check is what actually prevents collisions there; the probe is a bonus that happens not to fire.

**B1.4** Bound the slot space and make exhaustion actionable: "no free slot in 1..32 — run `doctor` to check for stale entries." Existing ceilings: harness 32, mini-infra 100 port slots.

**B1.5** Derive the slot back from an entry's ports (mini-infra's `slotOf`) so entries written before a field existed still resolve.

**B1.6** "Port in use" is **not the tool's to resolve.** Never kill whatever holds a port to make room — it's probably the user's own running instance. Re-check you're in the right worktree, or reallocate.

## T2 — The app runs docker compose

**B2.1** Per-worktree `COMPOSE_PROJECT_NAME`. Once the project name is namespaced, named volumes and networks isolate for free — the audit is about finding what *isn't* project-scoped.

**B2.2** Tear down by **compose project label** (`com.docker.compose.project`), never by reading a compose file, so `rm` still works after the directory is gone.

**B2.3** If teardown leaves resources behind (unreachable daemon, say), **do not free the slot.** Leave the registry entry, report what survived, and tell the user to fix the cause and re-run rather than hand-editing the registry.

**B2.4** Pre-pull or pre-build images that the runtime fetches lazily, so first-run failures surface at setup rather than mid-operation.

## T3 — The app has a separate test stack

**B3.1** Allocate a **second** compose project for tests, `${COMPOSE_PROJECT_NAME}-test`. A test compose file that pins `name:` must be overridden with an explicit `-p`, which always beats the file's `name:`. Reusing the dev project name for tests makes them the *same* project fighting over one service.

**B3.2** Teardown covers both projects' containers, networks and volumes.

**B3.3** The derivation must match wherever else it's computed (harness keeps `scripts/test.sh` and the descriptor in sync deliberately, and says so in a comment) — `rm` tears down by that exact name, not by recomputing it differently.

## T4 — The app manages the host docker daemon, or needs its own

**B4.1** A per-worktree VM/daemon (mini-infra: a Colima profile on macOS, a WSL2 distro on Windows, each running its own dockerd), selected by platform and overridable by env var.

**B4.2** **Capacity guard rail.** Refuse to start a *new* instance past a documented limit, naming what's currently running and how to tear one down. Mini-infra caps concurrent WSL distros at 4 because Docker's default address pool exhausts past that; re-running an already-running profile stays allowed.

**B4.3** `--keep-vm` on teardown — drop containers and the registry entry, leave the expensive VM up.

**B4.4** Warm-up is measured in minutes, so it runs in the **background** and the create flow doesn't block on it.

**B4.5** Document the bypass (`colima delete <profile> --data --force`, `wsl --unregister <distro>`) for when the helper can't run.

## T5 — The app allocates network subnets

**B5.1** Slice the address space by slot: mini-infra carves a `/22` per worktree out of `172.30.0.0/16` (slot 0 → `172.30.0.0/22`), giving four disjoint `/24`s per worktree with zero coordination, and the allocator downstream is size-agnostic so it just works.

**B5.2** Over-capacity slots fall back to the shared pool **with a warning naming the remedy**, rather than failing outright.

## T6 — The app keeps state under `$HOME` (DB, registry, credentials)

**B6.1** State isolation must be **independently selectable from port isolation** — see C1; their audiences genuinely differ.

**B6.2** Support seeding an isolated store from the live one, and say clearly in the briefing that it is **a snapshot taken at creation that diverges as the user keeps working — not a live mirror**.

**B6.3** Destructive teardown refuses obviously wrong targets: `--purge-db` refuses to delete a *shared* store path (it would wipe every project's data) and names the path in the refusal.

**B6.4** An env var may supply a flag default, but document it as **per-invocation only, never in a shell profile or a checked-in `.envrc`** — an ambient `*_ISOLATE_DB=1` is inherited by dispatched workers and re-introduces the exact bug it was meant to fix. Auto-detecting "am I an agent?" is not a workaround: an interactive dev session sets the same flag.

## T7 — The app has long-running processes, leases or leader election

**B7.1** **Reap orphaned processes bound to the worktree's ports** before any filesystem mutation — a survivor keeps heartbeating a shared lease and can starve the instance the user is actually looking at. bacio discovers `LISTEN` holders (`lsof` on unix, `netstat -ano` + `tasklist` on Windows), SIGTERMs those whose command names one of *its own* binaries, waits ~3s, escalates to SIGKILL. Rails:
- a non-matching process that merely grabbed the port is **never signalled**, only reported;
- the legacy default port is never touched;
- discovery failure is non-fatal — record a note, complete teardown, tell the user to kill manually;
- `--keep-processes` opts out; `--dry-run` lists what it *would* signal.
- Documented limitation: a process with no listener can't be found this way; a cwd-walk is out of scope.

**B7.2** Never blanket-`pkill -f <appname>` — it matches every instance on the machine including the user's. Track background processes by PID and stop only that PID.

**B7.3** After a rebuild+install, restart every long-running process sharing the store; a stale binary against a migrated schema surfaces as `no such column`.

**B7.4** Health-wait with progress on start (poll the health endpoint, log every 10s, dump the last N lines of logs on timeout) rather than returning before the stack is usable.

## T8 — A production stack co-resides on the dev machine

**B8.1** Its ports are **never allocatable**, even by a hand-edited manifest.

**B8.2** The tooling never touches it — no teardown path can reach it, and the reference doc states this explicitly.

**B8.3** Reserved defaults get the same treatment: bacio's `5320`/`5319` pair is reserved so an `init` never lands on it, and the reaper refuses to signal on it.

## T9 — The repo has a package manager whose deps aren't shared across worktrees

**B9.1** Install deps as part of creation — worktrees share git objects, **not** `node_modules`.

**B9.2** In a pnpm/npm repo this must run before *any* other package-manager command, **including the worktree CLI itself** when it runs through `tsx` (otherwise: `sh: tsx: command not found`).

**B9.3** Install failures **stop and surface the output** — no `--force` / `--shamefully-hoist` papering over.

**B9.4** Not every stack needs this: Go module downloads are lazy and share the host cache, so the harness has no separate install step.

## T10 — The app has a bootstrap / onboarding / seed step

**B10.1** Seed the running instance so the onboarding wizard is skipped, and store the resulting admin login and API key in the registry for `list --wide`.

**B10.2** A **seed profile** (`minimal` | `full`) controlling how much of the stack comes up, persisted on first run so later runs reuse it; an explicit flag contradicting the stored value overrides it **with a warning**.

**B10.3** Seeding is skippable (`--skip-seed`), re-runnable (`--seed`), and its already-seeded state is detectable from the descriptor so re-runs are cheap.

**B10.4** Missing seed credentials is a warning with the remedy named (copy `dev.env.example` to `~/.mini-infra/dev.env`), not a hard failure — write the minimal descriptor and continue.

## T11 — Agents work in the worktrees

**B11.1** Ship the lifecycle as **two installed skills**, create and remove, that get the ordering and safety checks right so neither human nor agent has to remember them. Descriptions enumerate the natural-language triggers ("spin up a worktree", "I want to work on this in parallel", "done with worktree", "free up that worktree's slot") **and the anti-triggers** ("already inside a worktree and wants to keep working there", "the run failed").

**B11.2** The skills are **scaffolding, not execution agents** — hand back a ready worktree and stop. Explicitly: no `ExitPlanMode`, no code changes, no PRs.

**B11.3** The create report must state: path, branch, dependency-install status, environment status (or "skipped — `--no-env`"), URLs **read back from the descriptor**, cwd, and **the shared-resource list repeated back**. Writes to shared resources escape the worktree and whoever works there needs to know *before* making one.

**B11.4** A **`<shared>` block in the descriptor** enumerating what is *not* isolated and the blast radius of each, so a session learns it from inside the worktree without reading the repo docs. Harness ships this as structured data (`name` + `impact` per resource).

**B11.5** `--no-env` for docs-only work, with a hard rule that skipping is **always stated explicitly** — an un-allocated worktree collides with everything and the failure looks like an unrelated bug (a port in use, or compose reusing another worktree's containers).

**B11.6** A **paste-into-the-agent briefing block**: what's isolated, what's shared, the exact path prefix every `Read`/`Edit`/`Write` must start with, how to reach the environment from outside the worktree (`--env <path>`), which commands are *not* for this session, and any snapshot-vs-live caveat.

**B11.7** Print the resolved path **and nothing else** on stdout, progress on stderr, so it pipes: `cd "$(scripts/new-worktree.sh ISSUE-256)"`.

**B11.8** Pre-flight before creating: repo root, default branch, clean tree, then `git pull --ff-only`. Any failure **stops** — never auto-stash, auto-checkout or silently merge; the user's WIP matters more than the skill finishing.

**B11.9** Never silently reuse an existing worktree or branch. A *generated* slug that collides regenerates (≤3 attempts, then ask); a *user-supplied* slug that collides stops and asks.

**B11.10** Defensive checks before destroying: uncommitted changes, unpushed commits (`git log @{u}..HEAD`, where the no-upstream fallback is itself a stop signal), no open PR. Any hit stops and asks. Never auto-pick "the most recent worktree".

**B11.11** Never `--force` a `git worktree remove` automatically — git refusing is **signal** that the checks missed something. Never delete the worktree you're standing in. Never delete the remote branch: the PR points at it.

**B11.12** Never clean up on a failure path. Failed smoke, no PR, mid-investigation → leaving it alive is correct.

**B11.13** Conventional worktree path — all three use `.claude/worktrees/<slug>`, matching what the dispatch harness's own `isolation: "worktree"` creates, so the manual flow is explicitly "the manual equivalent" of the automatic one.

**B11.14** Generate a memorable `<adjective>-<animal>` slug when none is supplied (both words ≤8 chars) and state it up front so the user can `cd` there.

**B11.15** A documented recreate-from-branch recipe for the review-feedback case, since the create flow branches from `main` and is the wrong tool for resuming an existing branch.

**B11.16** Human metadata: a required short description (≤10 words, plus an optional long form) captured on first run and persisted, so `list` says what each worktree is *for*. Prompt once interactively if not supplied.

## T12 — Agents clone the repo rather than using a worktree

**B12.1** Support **disposable clones with no linked-worktree relationship** — an agent that `git clone`s the target inside a container and runs compose against the *host's* socket is the same collision in a different shape, potentially landing on the dev (or prod) stack currently running it.

**B12.2** An explicit `-standalone` flag, because git genuinely cannot distinguish "the real main checkout" from "a disposable clone" — `--git-dir` and `--git-common-dir` are equal for both. The flag is the only thing making that call and must be documented as never-for-your-primary-checkout.

**B12.3** The registry must be **genuinely shared** with the host — bind-mount `$HOME/.<app>` into the container at whatever path `os.UserHomeDir()` resolves to there — so a slot the agent takes is unavailable to a concurrent host worktree and vice versa.

**B12.4** Teardown that is already registry-and-label-only needs no standalone-specific path; keep it that way.

## T13 — The repo has an issue tracker driving the work

**B13.1** Accept an issue key in place of a slug; read `base_branch` off the ticket and branch from `origin/<base_branch>` rather than always `main`.

**B13.2** Warn loudly when the chosen isolation mode would make that ticket unreachable from inside the new worktree (an empty isolated DB means the issue doesn't exist there).

**B13.3** Branch and directory naming is a per-repo convention — bare `<slug>`, `claude/<slug>` to namespace agent-created branches, `manual-<slug>` to distinguish hand-made from dispatch-made.

## T14 — The app has a GUI or other long-running user-facing surface

**B14.1** Surface the worktree identity **in the UI itself** — bacio puts the slug in the desktop window title and the leader-election holder label — so two open windows are distinguishable.

**B14.2** Every entry point resolves the same environment, including the ones that aren't a shell command: GUI binaries, MCP subprocesses, editor hooks. bacio's desktop binary needed a `--db` flag it had never had, plus `--env`, purely so it could be steered like the CLI.

**B14.3** Document the fast inner loop against a worktree's own instance (dev server + the worktree's API port, read from the descriptor).

## T15 — Development happens on more than one platform

**B15.1** One implementation in the repo's own stack — **no parallel `.sh`/`.ps1` wrappers** (mini-infra retired theirs for a single TypeScript CLI).

**B15.2** Isolate platform-specific surfaces to one module and enumerate them: process discovery (`lsof` vs `netstat -ano`+`tasklist`), VM driver, shell resolution (Windows `spawnSync` needs `shell:true` to find `.cmd` shims), one-time prerequisites (build the WSL base tarball first).

**B15.3** Missing platform tooling is a clear error naming the install command, not a stack trace.

## T16 — Worktrees accumulate faster than they're cleaned up

**B16.1** A `cleanup` verb that scans all git worktrees, asks `gh` whether each branch's PR is merged, and for merged ones destroys the runtime, removes the worktree and drops the registry entry.

**B16.2** `--dry-run` previews exactly what would be cleaned.

**B16.3** Optionally an installable OS scheduler job running it periodically (mini-infra: a launchd agent, hourly, installed/uninstalled via `install-cleanup-agent [--remove]`).

**B16.4** A `doctor` verb reporting drift without changing anything: registry entries whose directory is gone; worktree directories with no registry entry; entries whose descriptor is missing or unreadable. Each finding names the exact command that fixes it.

**B16.5** Handle partial states explicitly, each with its own path: directory gone + entry present → registry-only teardown; directory present + no entry → git-only removal; neither → say so and stop. A stale marker in `list` (bacio's `!`) plus the command to drop the row.

**B16.6** Capacity warnings and slot-exhaustion errors point at cleanup as the remedy.

## T17 — Config reaches the app through env vars

**B17.1** Generate a `.env`, and put the generated content in a **marked managed block** (`# --- managed by <tool>; edits below are overwritten ---` … `# --- end ---`). A re-run replaces only that block, leaving hand edits alone.

**B17.2** **Strip any definition of a managed key from outside the block** rather than letting it shadow the managed value — dotenv parsers disagree on which duplicate wins (one implementation keeps the *first*, docker compose keeps the *last*), so two definitions is a bug either way, not a matter of ordering them correctly.

**B17.3** A first `init` **seeds the new worktree's `.env` from the main checkout's**, so `GITHUB_TOKEN` and API credentials carry over. A missing source file seeds nothing and is not an error.

**B17.4** Audit for files inside the repo that hardcode a port and make them descriptor-aware as part of adoption. Harness needed exactly two (`web/vite.config.ts` and `scripts/test.sh`) and documented why each.

**B17.5** A free-form `extras` section the tool round-trips untouched, so adjacent tooling (Vite, dev URLs, future listeners) can add keys without a schema bump. Prefix keys by tool name.

## T18 — The app resolves config natively (not only through env)

**B18.1** A documented precedence honoured identically by **every** entry point:
1. explicit flags (`--db`, `--addr`/`--port`) — power-user override;
2. env override pointing at a descriptor (`APP_ENV=<path>`, with a per-invocation `--env <path>` beating the env var);
3. the worktree descriptor found from cwd;
4. the legacy default.

**B18.2** When the most specific inputs are fully supplied (e.g. both `--db` and `--addr`), short-circuit the descriptor read entirely, so a broken env override can't take down an explicit call.

**B18.3** A status command surfaces **which source won** (`env_source`, `env_path`) alongside the resolved values, so "which environment am I on?" is answered by the tool.

**B18.4** Inside an initialised worktree a **missing descriptor is drift and must fail loudly** — it is not an absent-by-design default.

---

# Part C — Divergent policy choices

Axes where the three repos chose differently, deliberately. Each option is correct for its context; the point is to choose consciously and record why.

**C1. Which dimensions are isolated by default.**
- *Port-only, state shared* (bacio) — because a dispatched worker must reach the real ticket. DB isolation's only genuine use is testing the app's own schema migrations across worktrees, so it's behind `--isolate-db`.
- *Ports + containers* (harness) — the natural unit when everything runs in compose.
- *Ports + containers + whole VM* (mini-infra) — required because the app manages the host docker daemon and would otherwise fight the user's own.
- **Driver:** whether the shared state is something the worktree's user *wants* to reach.

**C2. Isolated-state seeding mode.** bacio's script offers three: isolated + seeded from live (default), isolated + empty (`--empty-db`), shared (`--shared-db`). Which is right depends entirely on whether the work is *on* the store's schema or merely *through* it.

**C3. Descriptor format.** YAML (bacio — matches its existing `.bacio/config.yaml`, same parser, no new dependency), XML (harness, mini-infra — marshals straight from a struct, and `xmllint --xpath` is a one-liner for scripts). Pick whatever the repo already parses.

**C4. Config delivery.** Native resolution in the app's own entry points (bacio — required, because CLI, API, desktop, MCP channel and hooks all need it) vs `.env` generation only (harness, mini-infra — sufficient when everything reads env). Both is also valid.

**C5. Concurrency protection.** Full lockfile + atomic write (harness — stale-lock timeout at 15s, 5s acquisition deadline, 50ms poll) vs atomic write alone (mini-infra) vs neither. Driven by how likely two `init`s are to race, which in turn depends on whether agents create worktrees unattended.

**C6. Enforcement of agent boundaries.** A PreToolUse hook that denies (bacio) vs prompt-level rules in the skills (harness, mini-infra). See D1 — the hook exists because the prompt-level version demonstrably failed, but it's Claude-Code-specific machinery and a repo without unattended dispatch may not need it.

**C7. Where the branch comes from.** Always `main` (harness, mini-infra) vs read `base_branch` off the ticket (bacio). Depends on whether the repo has stacked/dependent work.

**C8. Cleanup posture.** Manual only (bacio, harness) vs merged-PR sweep on a schedule (mini-infra). Driven by how expensive an idle worktree is — a VM per worktree makes automatic GC pay for itself; a manifest file doesn't.

**C9. Slot ceiling.** 32 (harness), 100 ports / 64 CIDRs (mini-infra), port-space walk (bacio). A function of how many resources each slot consumes and how many worktrees actually run at once.

**C10. Descriptor location convention.** All three chose the worktree root over a dot-directory, but for a stated reason (`ls` shows it) that a repo could reasonably weigh differently.

---

# Part D — Hard-won lessons

Incidents worth carrying across repos even when the resources differ.

**D1. Prompt-level worktree confinement is a one-time cwd snapshot and does not hold.** `Read`/`Edit`/`Write` take absolute paths — cwd is irrelevant to them — and a worktree lives *inside* the repo it branches from, so a parent-repo path is always valid and existing. A worker can therefore do every edit, commit and push in the primary checkout while its startup check reported "I'm in a worktree". bacio's enforcement:
- classify cwd via `--git-dir` vs `--git-common-dir` into not-a-repo (allow) / primary checkout (**deny every edit**) / linked worktree (allow under its root);
- resolve `file_path` symlink-safely with a boundary-safe prefix test, and **deny** with a reason naming the correct root verbatim, so the model self-corrects and retries;
- deny `Read` of a `CLAUDE.md` outside the worktree — the parent copy is stale (the worktree was fast-forwarded onto `origin/<base>`) and reading it nudges the model into adopting the wrong prefix;
- deny raw `sqlite3` against the live shared store, so mutations can't bypass the audit log. Deliberately **fail-open on ambiguous shell constructs** (pipes, `$(...)`, env vars, globs) — the goal is to make the obvious bypass loud, not to be a general bash sandbox;
- emit **only one deny per call** so the tool surface stays uncluttered.

Complementary process-layer rules that raise the floor without enforcing: the worker's first task records the worktree-root prefix verbatim, and it re-runs `git branch --show-current` immediately before `commit` and before `push`.

**D2. A fixed compose project name is worse than a port collision.** A port clash fails loudly; a pinned `name:` makes the second worktree *silently attach to the first one's containers*.

**D3. Ambient env vars leak into dispatched workers.** An isolation flag set in a shell profile is inherited by every agent, re-creating the bug it was added to fix. Typing the flag on the one `init` per worktree is the reliable override.

**D4. The wrong git root silently clobbers.** `--show-toplevel` vs a walk-back-to-main helper look interchangeable until `init` from a linked worktree overwrites the main worktree's manifest.

**D5. Workspace binary vs system binary.** In a worktree, build a uniquely-named workspace binary for smoke-testing the in-progress change, but require every close-out/bookkeeping call to use the known-good system binary on `PATH`.

**D6. Duplicate env keys are a bug in both directions.** Since parsers disagree on first-vs-last-wins, never leave two definitions and try to order them correctly — strip one.

**D7. Silent truncation reads as success.** Anywhere the tooling bounds coverage (slot caps, fallback to a shared pool, skipped steps), it must **say so** — a warning naming the remedy, not a silent degrade.

**D8. Teardown must survive the directory being gone.** People run `git worktree remove` by hand first. Registry-and-label-only teardown makes that recoverable instead of an orphaned VM nobody can find.

---

# Part E — Trait profiles of the three repos

Descriptive, so the choices above are legible. Not a scorecard.

| | bacio | deepseek-harness | mini-infra |
|---|---|---|---|
| Binds host ports (T1) | ✅ API + proxy pair | ✅ 6 ports | ✅ 10 ports |
| Docker compose (T2) | — | ✅ | ✅ |
| Separate test stack (T3) | — | ✅ | — |
| Manages host daemon / needs VM (T4) | — | shares host socket | ✅ Colima/WSL2 |
| Allocates subnets (T5) | — | — | ✅ /22 per worktree |
| State under `$HOME` (T6) | ✅ shared SQLite | — | ✅ credentials |
| Long-running procs / leases (T7) | ✅ leader election | ✅ | ✅ |
| Co-resident production (T8) | — | ✅ `-prod` stack | — |
| Non-shared deps (T9) | Go — no | ✅ npm | ✅ pnpm |
| Bootstrap/seed step (T10) | — | — | ✅ full/minimal |
| Agents in worktrees (T11) | ✅ dispatch | ✅ | ✅ |
| Agents clone the repo (T12) | — | ✅ `-standalone` | — |
| Issue tracker drives work (T13) | ✅ | — | — |
| GUI surface (T14) | ✅ Wails desktop | web UI | web UI |
| Multi-platform (T15) | ✅ | — | ✅ macOS + Windows |
| Accumulation pressure (T16) | manual | `doctor` | ✅ launchd sweep |
| Env-var config (T17) | — | ✅ managed block | ✅ compose env |
| Native resolution (T18) | ✅ `wtenv.Resolve` | — | — |

---

## Open questions

1. **What's actually shareable?** Given how much is trait-driven, the reusable core looks like Part A plus the slot/registry/lock machinery — roughly: slot allocation, the host-global registry with locking and atomic writes, descriptor read/write, git worktree classification, and slug validation. Everything in Part B is per-app. Is that core worth extracting, or is the real reusable artefact a **checklist + generator** (audit the resources, design the slot mapping, emit repo-native code) rather than a library?
2. **Cross-repo port collisions.** Each repo has its own registry, so nothing stops bacio's allocator and mini-infra's from picking the same free port on one machine. A host-global registry shared *across repos* would fix it, at the cost of coupling. The live port probe (B1.3) partly mitigates it already.
3. **Cross-repo `list` / `doctor`.** "What's running on this machine, from any repo" isn't currently possible and would be useful — and is mostly the same question as (2).
4. **Should skills be generated per repo or generic?** The existing three are very concrete — they name the repo's own commands, ports and shared resources — and that concreteness is a large part of why they work. A generic pair reading the descriptor would be thinner but weaker.
5. **Repair, not just detection.** Every repo has some version of "someone `rm -rf`'d the worktree without tearing down". `doctor` reports it; nothing repairs it. Is a `reconcile` verb worth owning centrally?
6. **Trait detection.** Could an adoption tool infer the trait list by scanning a repo (compose files, `$HOME` paths, bound ports, package manager) and propose the requirement set, rather than the developer picking from Part B by hand?
