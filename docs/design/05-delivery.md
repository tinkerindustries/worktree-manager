# M5 — Delivery & Config Surface

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** A3, A8 (writing and ignoring), A11, A14 (descriptor writes), T14, T17, T18, C3, C4, B6.4, D3, D6.
**Depends on:** M1 for the descriptor location and containment tests, M2 for the allocation, M3 for resolved resource values.

## 1. What this module answers

How an allocation reaches the running application, and how any entry point works out which environment it is in.

C4 records that repos differ here and both positions are right: bacio resolves natively in its own entry points because its CLI, API, desktop binary, MCP channel and hooks all need it, while harness and mini-infra generate `.env` and let everything read the environment. Both is also valid. So this module has three channels and a repo picks any combination.

| Channel | Requirement |
|---|---|
| Descriptor file | A8, C3 — always written, the source of truth |
| `.env` managed block | T17 |
| Native resolution in the app's own entry points | T18 |

## 2. The descriptor

Always written, whatever else a repo chooses. A7 makes it authoritative over the registry, so it is what rebuild and reconcile read back.

### 2.1 Location and format

The worktree root (M1 §4.3), in whatever format the repo already parses (C3) — YAML for bacio because it matches `.bacio/config.yaml` and needs no new dependency, XML for harness and mini-infra because it marshals straight from a struct and `xmllint --xpath` is a one-liner.

A repo choosing YAML inherits M2 §3's hazard: slugs may be `no`, `on`, `off`, `yes` or `y`, which YAML 1.1 coerces to booleans. The emitter quotes every string scalar rather than checking whether a particular value happens to look boolean.

Written atomically per A14 — temp file in the same directory, fsync, rename — for the same reason as the registry.

### 2.2 Ignoring it

A8 requires the descriptor to be gitignored, the line appended idempotently, and the append to work when `.gitignore` is itself untracked. It is per-worktree, per-machine state, and committing it would carry one user's allocations into another's tree.

Appending to `.gitignore` from `init` is the wrong mechanism, for a reason A8 could not have seen: in a linked worktree, `.gitignore` is a tracked file shared through the branch, so every `init` dirties the working tree of a worktree that another tool just created clean.

The descriptor filename is constant per repo, so the ignore rule is a property of the repo rather than of any worktree. Two places to put it, in order:

1. **Committed to `.gitignore`**, done once by M9 during onboarding. This is the right home.
2. **`$GIT_COMMON_DIR/info/exclude`**, written idempotently by `init` as the fallback when onboarding did not commit it. Git reads `info/exclude` from the common directory, so one write covers every linked worktree of that repo, and it touches no tracked file.

A8's "works when `.gitignore` is itself untracked" is then satisfied by not touching `.gitignore` at all.

### 2.3 Contents

```yaml
version: 1
app: bacio
slug: brisk-otter
slot: 7
view: 2f9c...                    # M1 §5
path: /Users/geoff/Repos/bacio/.claude/worktrees/brisk-otter
standalone: false                # M1 §2.1 — declared once, read back thereafter
description: "fix dispatch lease race"    # B11.16

resources:
  api: 5407
  proxy: 5406
  compose: bacio-brisk-otter
  db: /Users/geoff/.bacio/db.sqlite

state:
  db: { isolated: false }
  seeded: null

shared:                          # B11.4 — half generated (M3 §6), half from the spec
  - name: /Users/geoff/.bacio/db.sqlite
    impact: writes are visible to every worktree and the main checkout

extras: {}                       # B17.5
```

`path` is informational per A9, so a worktree can be moved. Nothing resolves through it.

The `shared` block is structured data rather than prose — harness ships `name` plus `impact` per resource — so M7 can render it into a briefing and an agent can read it directly from inside the worktree without opening the repo docs.

`extras` is round-tripped untouched (B17.5), letting adjacent tooling add keys without a schema bump. Keys are prefixed by the tool that owns them.

## 3. `.env` emission

### 3.1 The managed block

B17.1: generated content goes in a marked block.

```
# --- managed by wt; edits below are overwritten ---
COMPOSE_PROJECT_NAME=bacio-brisk-otter
API_PORT=5407
# --- end ---
```

A re-run replaces only that block, so hand edits above and below survive.

### 3.2 Duplicates are stripped, not ordered

B17.2 and D6 together. Any definition of a managed key found outside the block is removed rather than left to be shadowed.

The reason is that ordering cannot be made correct: dotenv parsers disagree about which duplicate wins, with one implementation keeping the first and docker compose keeping the last. Two definitions is a bug in both directions, so there is no arrangement that works — the second definition goes.

### 3.3 Seeding from the main checkout

B17.3: a first `init` seeds the new worktree's `.env` from the main checkout's, so `GITHUB_TOKEN` and API credentials carry over. A missing source file seeds nothing and is not an error.

This is the one legitimate consumer of the main checkout path that M1 §3 deliberately made awkward to reach. Every other use of it is the D4 bug.

Seeding copies the unmanaged content only. Managed keys come from this worktree's allocation, and copying the main checkout's would hand the new worktree slot 0's ports.

## 4. Native resolution

T18, for repos where entry points resolve config themselves rather than reading the environment.

### 4.1 Precedence

B18.1, honoured identically by every entry point:

1. explicit flags (`--db`, `--addr`, `--port`) — the power-user override;
2. an env override naming a descriptor, with a per-invocation `--env <path>` beating the env var;
3. the worktree descriptor found from cwd;
4. the legacy default.

The env variable at level 2 is app-scoped — `BACIO_ENV`, not `WT_ENV`. A single shared name would mean an ambient value set for one app pointing every other app at the same descriptor, which is the D3 failure in a new shape.

B18.2: when the most specific inputs are fully supplied — both `--db` and `--addr`, say — the descriptor read is short-circuited entirely, so a broken env override cannot take down an explicit call.

B18.3: a status command reports which source won, as `env_source` and `env_path` alongside the resolved values. "Which environment am I on?" is a question the tool answers rather than one the user reasons about.

### 4.2 The reader

Generated per repo by M9, in the repo's own language. Roughly a hundred lines: locate the descriptor, parse it, apply the precedence above, report the source.

Two constraints. It reads the descriptor file and nothing else — no registry, no `WT_HOME`, no dependency on `wt` being installed — so an application can be built and shipped without the tool present. And it is versioned against the descriptor schema, refusing a version it does not understand rather than reading the fields it recognises.

### 4.3 A3 and B18.4 contradict each other

A3: no descriptor means legacy behaviour exactly, and "descriptor not present" is a fall-through, never an error.

B18.4: inside an initialised worktree, a missing descriptor is drift and must fail loudly, because it is not an absent-by-design default.

They read as contradictory because the missing descriptor was treated as the signal. It is not. The signal is the committed spec.

A3's full text is "opt-in, with byte-identical behaviour when absent" — it is about a repository that has not adopted the tooling at all. A repository with a committed spec has adopted it. So the reader has three cases, not two, and it can tell them apart from the working tree alone:

| cwd | Spec | Descriptor | Resolution |
|---|---|---|---|
| not a repo, or no spec | — | — | legacy default (A3) |
| primary checkout | present | — | legacy default — slot 0 keeps the committed defaults (A2) |
| linked worktree | present | present | the descriptor |
| linked worktree | present | absent | **fail loudly** (B18.4) |

The last row covers both situations that looked identical, and it does not need to tell them apart. A worktree Claude Code made five minutes ago and one whose descriptor was deleted are equally dangerous to resolve with legacy defaults: either way the app attaches to the shared store or the committed port, and collides with whoever holds slot 0.

This keeps §4.2's constraint intact. The reader finds the spec by walking up from cwd to the worktree root, the same operation it already performs to find the descriptor. No registry, no `WT_HOME`, no dependency on `wt` being installed.

The cost is real and worth stating: in an adopted repository, the app's entry points refuse to run in a linked worktree until `wt init` has been run there. That is the explicit act the requirements ask for — isolation must be declared, not inferred — and it converts B11.5's warning about un-allocated worktrees from a documented hazard into a refusal. It also means `--no-env` (docs-only work) means exactly that: the tree is for editing, and running the app in it will refuse.

## 5. Other entry points

T14 is about the surfaces that are not a shell command.

B14.2: every entry point resolves the same environment — GUI binaries, MCP subprocesses, editor hooks. bacio's desktop binary needed a `--db` flag it had never had, plus `--env`, purely so it could be steered like the CLI. Adoption is not complete while one entry point still resolves differently, and an entry point that cannot take a flag has to take the env override instead.

B14.1: the worktree identity appears in the interface itself — bacio puts the slug in the desktop window title and in the leader-election holder label — so two open windows are distinguishable. The descriptor carries the slug precisely so this costs nothing to implement.

B14.3: the fast inner loop against a worktree's own instance is documented, with the ports read from the descriptor rather than written down.

## 6. Ambient environment variables

B6.4 and D3. An env var may supply a flag's default, and it is per-invocation only — never a shell profile, never a checked-in `.envrc`. An ambient `*_ISOLATE_DB=1` is inherited by every dispatched worker and re-creates the exact bug it was added to fix. Auto-detecting "am I an agent?" is not a workaround, because an interactive dev session sets the same flag.

The general test, which this design has now applied three times, belongs here where the environment surface is owned:

**Would a child process inheriting this value do the right thing?**

`WT_HOME` (overview §6.1) passes: a worker inheriting the state directory location is correct, and is the point. `WT_STANDALONE` in a disposable-clone image (M1 §2.1) passes, because every process in that image is in a disposable clone. An isolation flag fails: the worker's correct behaviour depends on the task, not on the environment, so no ambient value can be right for all of them.

Location and capability may be ambient. Policy may not.

## 7. Reading it back

A11 is the most repeated instruction across all three source repos, and it is a rule about documentation as much as about code: everything downstream says "read it back with `wt show`", never "the UI is on 3100".

M7 enforces it in the generated docs and skills. M5's obligation is to make `show` cheap, scriptable and complete — table by default, `--json` for consumers, and every value an agent might otherwise hardcode present in the output.

## 8. Failure modes

| Situation | Behaviour |
|---|---|
| Descriptor exists with a newer schema version | refuse to read, name the upgrade |
| Descriptor unparseable | do not overwrite it; report and name `wt init` as the rebuild |
| `.env` contains a managed key outside the block | strip it, and say which keys were stripped |
| `.env` managed markers unbalanced or nested | refuse to write, report the line numbers |
| Main checkout `.env` missing during seeding | seed nothing, continue, not an error |
| `--env` points at a path that does not exist | fail; an explicit override that cannot be honoured is never a fall-through |
| Env override points at a descriptor for a different app | fail, naming both apps |
| Descriptor missing in a linked worktree of an adopted repo | the app's reader fails loudly, naming `wt init` (§4.3); `wt init` itself repairs it |
| Descriptor missing, no spec in the repo | legacy default; the repo has not adopted the tooling (A3) |

## 9. Open questions

- §4.3 is settled: the committed spec is the opt-in signal, so the reader never needs to tell those two cases apart. What remains is whether refusing to run in an un-initialised worktree is too blunt for repos that expect a lot of quick throwaway branches.
- Does the `.env` need its own ignore handling, or has every repo that uses one already ignored it? Assuming yes and being wrong would commit credentials.
- Should the generated reader be published as a small library per language instead of copied into each repo? Copies drift; a dependency has to be released and versioned, and §4.2's whole point is that an app can ship without `wt`.
- Where does a repo declare which entry points exist, so M9 can check B14.2's "every entry point" rather than trusting an audit done once?
- Does `extras` need namespacing rules enforced, or is the prefix convention (B17.5) sufficient given the tool round-trips the section untouched?
