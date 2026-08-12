# Worktree Manager — Design Overview

**Status:** all module documents drafted and swept for consistency. Not yet implemented.
**Input:** [`worktree-tooling-requirements.md`](../../worktree-tooling-requirements.md) — requirements gathered from three existing implementations (`bacio`, `deepseek-harness`, `mini-infra`).
**Date:** 2026-08-11

## 1. Goal

One tool that provides per-worktree environment isolation for every application on the machine, replacing three separate per-repo implementations. It must satisfy Part A of the requirements universally, cover Part B for whichever traits a given repo has, and let each repo make its own Part C policy calls.

## 2. Shape of the tool

A single host-global binary (`wt`) on `PATH`, plus one committed spec file per repo declaring that repo's resources and traits.

The binary owns allocation, the registry, the lifecycle and all cross-repo coordination. The spec turns Part B from code into configuration. This is possible because A4 already did the compression: every allocated resource is a function of one slot integer, so a repo's resource inventory is a list of derivation expressions rather than a program.

What stays in the repo is generated from the spec rather than hand-written — a descriptor reader for repos that resolve config natively (T18), the create and remove skills whose value comes from naming concrete ports and commands (B11.*), a session tripwire, an optional enforcement hook (C6, D1), and the reference doc (A17). 07-agent-surface lists them.

Onboarding a repo — auditing its resources, deciding its Part C policy, writing the spec and generating those three artefacts — is a Claude skill, not a `wt` subcommand. See §2.3.

### 2.1 Why not the alternatives

**A library each repo imports.** The three source repos are Go, Go and TypeScript, so the library gets written three times and drifts three ways. It also cannot answer open questions 2 and 3, because each repo keeps its own registry.

**A generator emitting repo-native code.** Better — no runtime dependency, and entry points can call it in-process. It still leaves three copies of one allocator, fixed only by regeneration, and still cannot coordinate across repos. It survives in this design, demoted to the repo-native edges listed above.

**An external binary.** Two requirements point at it directly. B9.2 says the tool must run before `pnpm install`, which anything running through `tsx` cannot do. B1.3 and open question 2 want a registry that spans repos, which only a shared tool can hold.

The cost is real and should be recorded: a repo now has a dependency that is not in its own lockfile, and B15.3's "name the install command" applies to `wt` itself.

### 2.2 Language: Go

One static binary, cross-compiled to macOS, Linux and Windows from a single toolchain, with no runtime for the repo to install. That last property is what makes B9.2 disappear: a tool that runs before `pnpm install` cannot depend on `pnpm install` having run.

What it gives the modules that need it:

- M8's platform surfaces are reachable — process signalling directly on unix, `golang.org/x/sys` for the Windows calls, and `taskkill` shelled out where Go cannot send a signal that does not exist;
- M2's lock is `os.OpenFile` with `O_CREATE|O_EXCL`, and `flock` is not needed, which the [virtiofs measurement](experiments/virtiofs-guarantees.md) established is the only option anyway;
- C3's three descriptor formats are covered by `encoding/json`, `encoding/xml` and one YAML dependency;
- M1's tests run against real repositories in `t.TempDir()`, which §8 of that document requires.

Build with `CGO_ENABLED=0` so the binary is genuinely static and B15.3's install instruction stays one line.

Two of the three source repos are already Go, so bacio's `internal/wtenv` is a starting point for M1 and M5 rather than a reference to reimplement. It also means the generated descriptor reader (M5 §4.2) is a real package for Go repos and generated source for everything else.

### 2.3 Onboarding is a skill, not a subcommand

Adopting a repo is complex, varied and judgment-heavy. It means finding every resource concurrent worktrees would fight over, choosing port bands that clear whatever the machine already runs, making the Part C policy calls, writing the spec, generating the repo-native artefacts, patching entry points that hardcode a port (B17.4), and proving it by running two worktrees side by side. Most of that is inference and conversation, and open question 6 already concluded that a scanner can only draft it — `$HOME` state, leases and blast radius need a human in the loop.

So the division of labour is:

| Concern | Owner |
|---|---|
| Judgment, inference, interviewing, code generation, one-time adoption | the onboarding skill |
| Allocation, registry, lifecycle, teardown, health — every repeated operation | `wt` |

`wt` performs no inference at all. It exposes the facts the skill needs as machine-readable primitives — validating a candidate spec, reporting what is listening on the host, describing what a spec would allocate — and refuses a spec it cannot honour. The skill decides; the binary knows.

That boundary keeps the binary deterministic and testable, and it means a repo that never uses Claude can still be adopted by hand-writing a spec, because the spec is the only artefact the binary consumes.

## 3. Core abstraction

Everything the tool does to a repo falls into one of three categories.

### Drivers

Allocatable resource types the tool owns. Each implements the same operations:

| Operation | Purpose |
|---|---|
| `derive(slot, spec)` | compute the value — the whole of Part B's allocation logic |
| `probe(value)` | is it actually free right now (B1.3) |
| `apply(value)` | materialise it, where materialising is more than writing a descriptor line |
| `teardown(handle)` | destroy it by a handle that survives the directory being gone (B2.2, D8) |
| `verify(value)` | report drift without changing anything (B16.4) |
| `blastRadius` | prose describing what is *not* isolated, for the shared block (B11.4) |

The set is smaller than T1–T18 implies:

| Driver | Requirements |
|---|---|
| `port` / `port-group` | T1 |
| `namespace` — a derived name | T2, and T3 by instantiating it twice |
| `cidr` | T5 |
| `state-path` | T6 |
| `machine` — VM or daemon | T4 |

T8 (co-resident production) is not a driver. It is an exclusion list fed to the allocator, which is what lets it hold even against a hand-edited manifest.

### Hooks

Repo-specific commands the tool sequences but does not understand: dependency install (T9), seed (T10), image pre-pull (B2.4), health-wait (B7.4), build. The tool owns ordering, rollback, dry-run and output discipline. The repo owns the command string.

### Emitters

How an allocation reaches the running application: a descriptor in the repo's chosen format (C3), an `.env` managed block (T17), or nothing because the app resolves natively (T18). C4 says all three combinations are valid.

Part C in general becomes spec fields rather than forks in the code — C1, C2, C3, C7, C8 and C9 are all declarations. C5 is the exception: locking strength stops being a repo choice and becomes the tool's, settled in 02-coordination §8.

## 4. Modules

Dependency order runs roughly top to bottom; M8 is a leaf every other module calls into.

| # | Module | Owns |
|---|---|---|
| M1 | Identity & Discovery | git worktree classification, slug rules, path conventions, descriptor location |
| M2 | Coordination Core | registry, locking, atomic writes, slot allocation, exclusions, the cross-repo port ledger |
| M3 | Resource Drivers | the driver set above, and the contract they implement |
| M4 | Lifecycle & Orchestration | the verb set, ordering, rollback, hook execution, process reaping, output discipline |
| M5 | Delivery & Config Surface | descriptor writing, `.env` managed blocks, the resolution precedence contract, native readers |
| M6 | Fleet Health | `list` / `doctor` / `cleanup` / `reconcile` across every repo on the machine |
| M7 | Agent Surface | generated skills, briefing and shared blocks, enforcement hook, reference docs |
| M8 | Platform | every OS-specific surface, isolated here because B15.2 asks for exactly that |
| M9 | Onboarding | the adoption skill, the spec-authoring flow, and the primitives `wt` exposes to it |

M8 exists because B15.2 names it as a requirement: process discovery (`lsof` vs `netstat -ano` + `tasklist`), the VM driver, shell resolution, and one-time prerequisites all get isolated to one module and enumerated. Splitting them across M3 and M4 would violate that directly.

M9 is the only module that is not part of the binary. It is built last and runs first — nothing can be onboarded until the spec schema M3 defines is settled.

M7 and M9 both produce skills and should not be confused. M7's run per worktree, many times, and are generated for a repo already adopted. M9's runs once per repo and does the adopting.

## 5. Cross-repo coordination

Slot namespaces stay per-repo, so `bacio` slot 3 and `mini-infra` slot 3 coexist. Port space is allocated globally.

A repo registers a port band once in a host-global band ledger. Within its band, the slot picks offsets. A4's legibility property survives — the last two digits of every port still equal the slot — while collisions between repos become structurally impossible rather than probabilistically avoided. The live probe (B1.3) goes back to being the bonus check it was always documented as.

This is the single largest gain over the generator approach, and it closes open questions 2 and 3.

## 6. Execution contexts

`wt` must run inside a container and coordinate with the host machine it runs on. B12.1–B12.4 anticipated part of this: an agent that clones the repo into a container and runs compose against the host's socket is the same collision in a different shape. Making the tool itself container-resident generalises that requirement rather than adding a new one, but it invalidates four assumptions a host-only tool could make.

### 6.1 The registry is located, not inferred

`$HOME` inside a container is not the user's home. B12.3's answer — bind-mount to whatever `os.UserHomeDir()` resolves to inside — lets the image dictate the mount point. Invert it: `WT_HOME` names the state directory and defaults to `$HOME/.wt`. A container mounts the host directory anywhere and points the variable at it.

This is not the ambient-env-var failure of D3 and B6.4. That rule is about a policy flag leaking into dispatched workers and silently re-enabling isolation nobody asked for. `WT_HOME` names a location, and a worker inheriting it is the desired behaviour.

### 6.2 Advisory locking does not cross the boundary reliably

On Linux with a bind mount, `flock` works across the container boundary because it is one inode on one kernel. On Docker Desktop for macOS the mount goes through virtiofs and advisory locks are not reliably propagated to the host. macOS is a primary platform under T15, so the lock cannot be built on `flock`.

What survives is exclusive creation of a lock file (`O_CREAT|O_EXCL`) holding the owner's view id, pid and creation time, with a stale timeout. That is close to harness's design under C5, which already specifies a 15s stale-lock timeout, a 5s acquisition deadline and a 50ms poll.

[Measured](experiments/virtiofs-guarantees.md) on Docker Desktop for macOS: exclusive creation and rename are both atomic across the mount, and POSIX record locks fail to propagate just as `flock` does. So the exclusive-create lock is the only mechanism available, and it works.

### 6.3 A path that is not visible is not a path that is gone

The registry records absolute worktree paths. `/Users/geoff/Repos/bacio/.claude/worktrees/foo` on the host is a different path, or no path, inside a container. A6 makes the registry global and A9 already says the recorded path is informational — but `doctor` and `cleanup` decide what to destroy by asking whether a directory still exists.

Run `wt doctor` in a container against a shared registry under that logic and every host worktree reports as stale. Run `wt cleanup` and it acts on the report.

So every registry entry records the view that created it — machine identity plus filesystem namespace — and `wt` computes its own view at startup. Entries from another view are read for allocation, which is the whole reason the registry is shared, and are never destroyed, never stale-detected, and never counted as absent by a capacity guard rail. D8's rule that teardown survives the directory being gone holds within a view. Across views, absent means invisible.

### 6.4 Host-namespace operations refuse rather than no-op

Three operations reach outside the filesystem and degrade quietly in a container:

- the live port probe (B1.3) binds the container's network namespace, which the requirements already document;
- process reaping (B7.1) sees the container's pid and network namespaces, finds nothing, and reports success;
- teardown by compose label (B2.2) works only if the host docker socket is mounted.

D7 forbids the silent degrade. Each has to detect that it cannot see what it is meant to see and say so, naming the remedy: mount the socket, run with `--pid=host`, or accept that the registry check is the only collision defence available in this context.

Detection probes the capability rather than sniffing for `/.dockerenv`. Whether the socket is present, whether the path resolves, whether the namespace matches — capability is what the decision turns on, and it is directly observable.

### 6.5 Ownership

A container writing the registry as uid 0 leaves files the host user cannot rewrite. The install documentation covers running with a matching `--user`, and `wt` reports an unwritable state directory as a clear error naming the fix rather than failing at the atomic rename.

### 6.6 Module consequences

| Module | What this adds |
|---|---|
| M1 | view identity computation; `-standalone` and container residency are the same concern |
| M2 | `WT_HOME` resolution, the lock mechanism, the `view` field on every entry, view-scoped destruction |
| M3 | probe and teardown declare their context requirements; drivers report unavailable rather than succeeding emptily |
| M4 | reaping refuses outside the host namespace |
| M6 | every destructive fleet verb is view-scoped; `list` is not |
| M8 | capability probes live here with the rest of the platform surface |

## 7. Non-goals

Section 2 of the requirements is inherited unchanged: no Docker-in-Docker, no isolating content-addressed caches, no serialising instead of isolating, no auto-creating an environment on first mutation, no named-profile indirection.

Two additions specific to this design:

- **`wt` does not learn any repo's domain.** Anything requiring knowledge of what the app does is a hook, not a feature.
- **`wt` does not create worktrees.** Claude Code, the dispatch harness, a container's `git clone` or a human already did. `init` attaches to what exists; there is no `create` verb. See 04-lifecycle §2.
- **`wt` does not translate paths between views.** It records which view an entry belongs to and declines to act outside its own, per §6.3. Mapping a container path back to a host path is guesswork that would make destruction unsafe.

## 8. Requirement coverage

Every requirement in Parts A, B, C and D is assigned to an owning module. Each module document opens with its own list, and those lists are authoritative — this table is the index.

| Source | Owning module |
|---|---|
| A1, A12 (the enforceable half), A13, A15 | M4 |
| A2, A8 (location), A9, A10, A16 | M1 |
| A3, A8 (writing and ignoring), A11 | M5 |
| A4, A5, A6, A7, A14 | M2 |
| A17 | M7 |
| T1, T2, T3, T4, T5, T6 (allocation) | M3 |
| T6 (B6.4, ambient environment) | M5 |
| T7, T9, T10 | M4 |
| T8 | M2 |
| T11: B11.7, B11.9 (supplied-slug half), B11.10, B11.11, B11.16 | M4 |
| T11 (remainder), T13, C7 | M7 — `wt` has no `create` verb, see 04-lifecycle §2 |
| T12: B12.1, B12.2 | M1 |
| T12: B12.3 | M2 |
| T12: B12.4 | M4 |
| T14, T17, T18 | M5 |
| T15 | M8 |
| T16 | M6 |
| C5 | M2 — locking stopped being a repo choice |
| D1 | M7 |
| D2, D8 | M3 |
| D3, D6 | M5 |
| D4 | M1 |
| D5 | M4 |
| D7 | M4 (output discipline) |
| B17.4, and the requirements' open question 6 | M9 |
| §6 execution contexts | M2, with the per-module additions in §6.6 |

Coverage is checked mechanically: every requirement identifier in the requirements document appears in at least one module document, and none appears only here.

## 9. Spec file — provisional

Sketch only, to make the design concrete enough to argue with. The schema is the piece most likely to be wrong, and it is settled in the M3 document, not here.

Superseded by [03-drivers §3](03-drivers.md#3-spec-schema), which is authoritative. Reproduced here in short form for orientation:

```yaml
version: 1
app: bacio
slots: { max: 32 }

resources:
  - { type: port, name: api }
  - { type: namespace, name: compose, kind: compose, template: "{app}-{slug}" }
  - type: state-path
    name: db
    template: "{home}/.bacio/worktrees/{slug}/db.sqlite"
    default: shared                   # C1
    flag: "--isolate-db"

reserved:
  ports: [5319, 5320]                 # B8.3 — committed defaults are a repo fact

hooks:
  install: null                       # B9.4 — Go needs none
  health: curl -fsS http://127.0.0.1:{api}/healthz

emit:
  descriptor:
    format: yaml                      # C3
    path: bacio-env.yaml              # undotted so `ls` shows it (A8) — see 01-identity §4.3
  env: false                          # C4
```

Note what is absent: port numbers. Bands are a fact about one machine and live in the host-global ledger, not in a committed file. See 03-drivers §3.2.

## 10. Open questions carried forward

From section "Open questions" of the requirements, with the position this design takes:

1. **What is shareable?** The core plus the drivers, which is more than the requirements estimated. The generator survives for the repo-native edges only.
2. **Cross-repo port collisions.** Solved by the band ledger in §5.
3. **Cross-repo `list` / `doctor`.** Falls out of one registry; owned by M6.
4. **Generic or generated skills?** Generated per repo from the spec. The spec already holds every concrete fact a skill needs.
5. **Repair, not just detection.** Yes, `reconcile` belongs centrally. It needs no repo knowledge beyond teardown-by-label, which B2.2 and D8 already force to be directory-independent. Owned by M6.
6. **Trait detection.** Not a command. Onboarding is a Claude skill (§2.3), because the inference a scanner can do — compose files, bound ports, package manager, committed defaults — is the easy half, and the half that matters (`$HOME` state, leases, blast radius) needs a conversation. Owned by M9.

Still unanswered, and to be resolved in the module documents:

- How a repo declares that two of its resources must move together beyond the adjacent-pair case (M3).
- ~~Whether the band ledger needs its own lock separate from the registry's~~ — resolved in 02-coordination §8: one lock for the state directory, because allocation reads the ledger and writes the registry as one operation.
- What `wt` does when its own version is older than the spec version a repo committed (M2 or M5).
- ~~Whether `O_CREAT|O_EXCL` is atomic over virtiofs, which §6.2 depends on~~ — [measured](experiments/virtiofs-guarantees.md): it is, as is `rename`. Colima, WSL2 and network filesystems remain unmeasured.
- How view identity is computed so it is stable across container restarts and image rebuilds, and distinct between two containers sharing one host mount.
- ~~Whether a container should be allowed to allocate at all when it cannot probe ports~~ — resolved in 03-drivers §4.1: yes, and the output states that the registry was the only check. B1.3 already holds that the registry is what prevents collisions and the probe is a bonus.

## 11. Document index

| Doc | Status |
|---|---|
| `00-overview.md` | this file |
| `01-identity.md` | draft |
| `02-coordination.md` | draft |
| `03-drivers.md` | draft — authoritative for the spec schema |
| `04-lifecycle.md` | draft |
| `05-delivery.md` | draft |
| `06-fleet.md` | draft |
| `07-agent-surface.md` | draft |
| `08-platform.md` | draft |
| `09-onboarding.md` | draft |
| `experiments/virtiofs-guarantees.md` | measured 2026-08-11 |
