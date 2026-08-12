# M4 — Lifecycle & Orchestration

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** A1, A12 (the half `wt` can enforce), A13, A15, T7, T9, T10, B2.4, B11.7, B11.9 (supplied-slug half), B11.10, B11.11, B11.16, B12.4, D5, D7.
**Explicitly not owned:** worktree creation. See §2.
**Depends on:** M1 for classification and naming, M2 for allocation, M3 for drivers, M8 for process discovery.

## 1. What this module answers

What happens, in what order, and what happens instead when a step fails.

M4 is the only module that runs other people's code — package managers, seed scripts, health checks — and the only one that signals processes. Both of those need rails, and most of this document is rails.

## 2. `wt` does not create worktrees

Something else already does. Claude Code's `isolation: "worktree"` creates one, the dispatch harness creates one, a container `git clone`s (T12), and people run `git worktree add` by hand. In every case the tree exists, is checked out, and may already have edits before `wt` hears about it.

So `wt` has no `create` verb. `init` attaches to a tree someone else made, and that is the whole of the entry path.

The reasoning is that a `create` verb's only unique contribution would be the pre-flight in B11.8 — clean tree, `git pull --ff-only` — and it would duplicate what the creating tool already does, in a second place, with a second set of conventions about branch naming and base branches. Keeping it would mean maintaining a worktree creator that most worktrees never go through.

### 2.1 What moves out of the tool

| Requirement | Now belongs to |
|---|---|
| B11.8 pre-flight — clean tree, `pull --ff-only`, stop on any failure | M7's create skill, which drives whatever makes the worktree |
| B11.9 slug collision on a *generated* name | M7, along with the generator |
| B11.14 memorable `<adjective>-<animal>` slug | M7 — `wt` never names a directory, so it never needs one |
| C7 base branch — always `main` versus read off the ticket | M7, and T13's issue-tracker integration with it |
| B13.3 branch naming convention | M7 |

Slug generation goes with them, and M1's public surface no longer offers it. The binary reads a slug from the directory basename and validates it; it never invents one.

### 2.2 What this costs

Three consequences run through the rest of this document.

**`init` never refuses over working-tree state.** A worktree Claude Code made and has been working in for an hour is dirty by design.

**A12's ordering is only half enforceable.** The sequence still holds — worktree exists, then `init`, then start — but `wt` does not own the first step and can only detect whether it happened, which §2.4 covers.

**Orphaned entries are the normal case.** A12 also requires `rm` before `git worktree remove`, because teardown needs the entry the removal would orphan, and a third-party tool will not call `wt rm` first. Together with the disposable-container problem from M1 §5.2, that is two routine sources of orphans rather than two edge cases. It makes M6's reconcile load-bearing rather than a tidy-up convenience.

### 2.3 Attaching

`init` derives everything from what it finds.

The slug comes from the worktree directory basename, which the requirements' glossary already specifies and which is exactly right here: another tool chose that name, and the branch is not usable as identity because branches get renamed and deleted.

The branch is whatever is checked out. `init` does not care what it was branched from.

Three outcomes when `init` runs in a tree it does not recognise:

| Found | Meaning | Action |
|---|---|---|
| no entry, no descriptor | not initialised | allocate and set up |
| entry and descriptor agree | initialised | reconcile per §3.1 |
| entry exists, descriptor missing | drift | re-emit the descriptor from the entry, and say so |
| descriptor exists, no entry | registry lost or another view's | rebuild the entry from the descriptor (A7) |

B18.4 says a missing descriptor inside an initialised worktree must fail loudly. That rule binds the *application's* reader (M5 §4.3), not `init` — `init` is the command B18.4's error tells the user to run, so it repairs rather than refuses.

The last row is A7 doing real work rather than being a theoretical property. The descriptor wins on disagreement, so a registry that was deleted, corrupted or is simply a different machine's can be repopulated from the trees themselves.

A slug that collides with an existing entry for a *different* path under the same app is a genuine conflict — two worktree directories with the same basename. `init` stops and asks for an explicit slug rather than inventing a suffix, because the two trees are then indistinguishable in every listing the user reads afterwards. This is B11.9's user-supplied-collision rule, which is the only half of that requirement the binary still owns.

### 2.4 Discovery, without auto-allocation

The gap this leaves is that nothing tells the user their new worktree has no environment. They find out when a port collides or compose attaches to another worktree's containers, which B11.5 warns looks like an unrelated bug.

Closing it with automatic allocation is not available. The requirements reject auto-creating an environment on first mutation, on the grounds that it fragments state when the user only wanted a quick branch, and that isolation must be an explicit act.

So the generated artefact is a tripwire rather than an action: a `SessionStart` hook, shipped by M7, that classifies the cwd and prints that this worktree has no environment and names `wt init`. Detection and a sentence, no allocation. A repo that wants allocation can configure the hook to do it, having made that choice explicitly, which is the condition the rejection was about.

## 3. Verbs

| Verb | Does |
|---|---|
| `init` | attach to the current tree: allocate, materialise, emit; idempotent |
| `start` | run the hooks that bring the stack up |
| `show` | read the descriptor back |
| `rm` | safety checks, reap, teardown, deallocate |
| `list`, `doctor`, `cleanup`, `reconcile` | M6 |

A1 asks for `init`/`start`, `list`, `show` and `rm`/`delete`, which this is exactly. The requirement never included creation.

The full command surface is larger than A1's verb set, because M6 and M9 need their own. Collected here so it can be read in one place:

| Command | Module |
|---|---|
| `init`, `start`, `show`, `rm` | M4 |
| `list`, `doctor`, `reconcile`, `cleanup`, `views`, `schedule install` | M6 |
| `spec validate`, `spec explain`, `bands list`, `bands suggest`, `bands reserve`, `ports scan` | M9's primitives, implemented over M2 and M3 |
| `guard` | M7's enforcement hook |

`show` matters more than its size suggests. A11 — "read it back with `<tool> show`", never "the UI is on 3100" — is the most repeated instruction across all three source repos, so this verb is what every doc, skill and agent is pointed at.

### 3.1 Idempotence

A1: `init` never errors on a re-run and never reallocates. An existing entry for this slug reconciles — the recorded slot is authoritative per A5 — and resources are re-derived, re-verified and rebuilt where they are missing.

That makes `init` the repair path as well as the setup path, which is what lets M6's `reconcile` be thin.

### 3.2 Human metadata

B11.16 requires a short description, ten words or fewer, so `list` says what each worktree is *for*. `init` requires it, prompts once when attached to a terminal and none was supplied, and fails asking for the flag when it is not. M7's create skill normally supplies it, but `init` is run directly often enough that it cannot depend on that.

## 4. `init`: sequence and rollback

| Step | Undo on failure |
|---|---|
| classify and resolve slug (M1) | nothing written yet |
| allocate (M2, commits `reserving`) | drop the entry |
| materialise (M3 `apply`) | driver `teardown`, in reverse order |
| emit descriptor and env (M5) | remove written files |
| hooks: install, prepull, build | nothing — see below |
| activate entry (M2) | — |
| hooks: start, seed, health | leave running; the worktree is usable |

A13 requires that a failed `init` leaves no half-set-up state. bacio implements it by having its script remove the git worktree it just created; here the worktree is not ours to remove, so rollback stops at the allocation and everything derived from it. The tree survives, un-initialised, exactly as `wt` found it — which is the correct outcome, since somebody else's tool made it and may still be using it.

Rollback stops being appropriate once the environment is allocated and the tree is usable. A failed health check has not left the worktree broken; it has left it not yet running, and destroying the allocation would discard a correct allocation and the diagnostic evidence.

Rollback also applies only to failures *during* `init`. B11.12 — never clean up on a failure path — is about what happens afterwards: a failed smoke test, no PR, a mid-investigation worktree. Those must stay alive. The two rules read as contradictory and are not, because they cover different windows.

### 4.1 Output

B11.7's discipline holds even though its example does not: data on stdout, progress on stderr. The requirement's `cd "$(scripts/new-worktree.sh ISSUE-256)"` was about a creation script printing a path nobody knew yet. `init` runs in a tree whose path the caller already has, so what it prints on stdout is the descriptor, and `wt show --json` is the form everything downstream reads.

## 5. Hooks

Repo-declared commands `wt` sequences without understanding.

```yaml
hooks:
  install: pnpm install --frozen-lockfile
  prepull: docker compose pull        # B2.4
  build: go build -o {worktree}/bacio-dev ./cmd/bacio
  seed: ./scripts/seed.sh --profile {seed_profile}
  health: curl -fsS http://127.0.0.1:{api}/healthz
```

Contract: cwd is the worktree root, the environment is what M5 emitted, stdout and stderr are streamed to stderr, non-zero exit stops the sequence, and `--dry-run` prints the resolved command without running it.

### 5.1 Install

B9.1: worktrees share git objects, not `node_modules`, so installation is part of `init`. The requirement says "part of creation", but the creating tool has no reason to know the repo needs an install, so it lands here.

B9.2 asked for install to run before any other package-manager command including the worktree CLI itself, because a CLI running through `tsx` cannot start until its own dependencies exist. That requirement is satisfied structurally here — `wt` is a binary on `PATH` and does not run through the repo's package manager. It is one of the two requirements that argued for an external binary in the first place (overview §2.1).

B9.3: install failures stop and surface the output. No `--force`, no `--shamefully-hoist`, no retry with different flags. A papered-over install produces a worktree that fails later somewhere unrelated.

B9.4 is a reminder that this hook is optional — Go module downloads are lazy and share the host cache, so the harness has no install step at all.

### 5.2 Seed

T10, and the most stateful of the hooks.

B10.1: seeding the running instance skips the onboarding wizard, and the resulting admin login and API key are stored in M2's `secrets` field for `list --wide`.

B10.2 describes a sticky parameter, and it generalises beyond seeding. A hook parameter may be declared sticky: chosen on first run, persisted in the descriptor, reused by later runs, and overridden by an explicit flag that contradicts it — with a warning, because a silent override of a persisted choice is a surprise waiting several runs to be discovered.

B10.3: `--skip-seed` and `--seed`, with already-seeded state readable from the descriptor so a re-run is cheap.

B10.4: missing seed credentials is a warning naming the remedy — copy `dev.env.example` to `~/.mini-infra/dev.env` — not a hard failure. Write the minimal descriptor and continue. The worktree is still useful without a seeded instance.

### 5.3 Health

B7.4: poll the health endpoint, log progress every 10 seconds, and on timeout dump the last N lines of the relevant logs. Returning before the stack is usable pushes the failure into whatever the user does next, where it looks unrelated.

### 5.4 Build, and which binary

D5: in a worktree, build a uniquely-named workspace binary for smoke-testing the in-progress change, but make every close-out and bookkeeping call use the known-good system binary on `PATH`.

The descriptor records both paths so M7's briefing can state which is which. This bites hardest when the application under development is itself the tooling — a worktree of `wt` must not tear itself down with its own in-progress build.

### 5.5 Rebuilding against a shared store

B7.3: after a rebuild and install, restart every long-running process sharing the store, because a stale binary against a migrated schema surfaces as `no such column`.

`wt` knows which entries share a state path, since a resource with `default: shared` is shared by definition. Restarting another worktree's processes is a cross-worktree action with no safe general form, so this is a warning rather than an action: after a build, when a shared state path exists and other entries reference it, say that peers running an older binary against the migrated store will fail, and name them.

## 6. The reaper

B7.1. Orphaned processes bound to a worktree's ports are reaped before any filesystem mutation, because a survivor keeps heartbeating a shared lease and can starve the instance the user is actually looking at.

Discovery is M8's: `lsof` on unix, `netstat -ano` plus `tasklist` on Windows, finding `LISTEN` holders of this worktree's allocated ports.

The rails, all from B7.1 and B7.2:

- **Only own binaries are signalled.** The spec names them. A process that merely grabbed the port is reported and never signalled — it is probably the user's own instance, which is B1.6 applied to processes rather than ports.
- **SIGTERM, wait about three seconds, then SIGKILL.**
- **Reserved and legacy default ports are never touched** (B8.3), which reuses M2's exclusion list rather than a second one.
- **Never `pkill -f <appname>`** (B7.2). It matches every instance on the machine including the user's. Background processes `wt` started are tracked by pid and stopped by pid.
- **Discovery failure is non-fatal.** Record a note, complete the teardown, tell the user to kill manually.
- `--keep-processes` opts out. `--dry-run` lists what would be signalled.

Two limitations are documented rather than solved. A process holding no listener cannot be found this way, and walking process working directories to find one is out of scope. And in a container the discovery sees the container's pid and network namespaces, so it finds nothing — per overview §6.4 that is reported as unavailable with the remedy named, not as a successful reap of zero processes.

The container case and the discovery-failure case converge on the same behaviour: note it, continue the teardown, tell the user. The difference is only in what the message says.

## 7. `rm`

Order is teardown first, `git worktree remove` second (A12), because teardown needs the registry entry the removal would orphan.

`rm` also has to work when that ordering was not followed, which per §2 is routine rather than exceptional. Claude Code removing its own worktree, or a disposable container disappearing, leaves an entry whose directory is gone. Teardown is already registry-and-label-only (D8, M3 §2.2), so this case works — but the caller is no longer standing in the tree, so `rm` needs to accept a slug rather than only inferring the target from cwd.

B12.4 makes the same point from the standalone-clone direction: teardown that is already registry-and-label-only needs no clone-specific path, and keeping it that way is what makes both cases fall out of one implementation.

### 7.1 Safety checks

B11.10, before anything is destroyed:

- uncommitted changes;
- unpushed commits, via `git log @{u}..HEAD` — and no upstream at all is itself a stop, not a pass;
- an open PR.

Any hit stops and asks. The PR check needs `gh`; when `gh` is missing or unauthenticated the check reports unavailable and `rm` stops. A safety check that cannot run fails closed.

`rm` never picks a target on its own. "The most recent worktree" is not an inference the tool makes.

### 7.2 Destruction rails

B11.11:

- never `--force` a `git worktree remove` automatically — git refusing is signal that a check missed something;
- never delete the worktree the caller is standing in, which M1's classification detects directly;
- never delete the remote branch, because the PR points at it.

### 7.3 Partial states

B16.5's cases each get their own path, and M6 owns the detection:

| State | Action |
|---|---|
| directory gone, entry present | registry-and-label-only teardown (D8) |
| directory present, no entry | `git worktree remove` only, nothing to deallocate |
| neither | say so and stop |

## 8. Output and exit codes

A15: `list` as a table plus `--json`, mutating verbs supporting `--dry-run`. Every verb offers `--json`, because M7's and M9's skills consume all of them.

D7 applies across the whole module: anywhere coverage is bounded — a skipped hook, a fallback to a shared pool, a reap that could not run, a slot cap — it is stated in the output with the remedy named. A silent degrade reads as success.

Exit codes are a small fixed set so a skill can branch without parsing prose:

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | failure |
| 2 | usage error |
| 3 | refused by a safety check — the tree is dirty, a PR is open, capacity is reached |
| 4 | required context unavailable — no docker socket, wrong namespace, `gh` missing |

Separating 3 and 4 is what lets an agent tell "you asked me to do something unsafe" from "mount the socket and try again", which are different conversations to have with the user.

## 9. Failure modes

| Situation | Behaviour |
|---|---|
| Not a linked worktree or standalone clone | stop, exit 3 — A2 forbids managing slot 0 |
| Slug collides with a different path | stop, exit 3, ask for an explicit slug |
| Materialisation fails | roll back in reverse, drop the entry, leave the tree untouched, exit 1 |
| Install fails | stop, surface the output verbatim, exit 1; the worktree and allocation survive for inspection |
| Health times out | keep the worktree, dump the last log lines, exit 1 |
| Seed credentials missing | warn, name the remedy, continue, exit 0 |
| Reaper cannot discover | note, continue teardown, tell the user to kill manually |
| Teardown leaves resources | do not free the slot, entry stays `tearing-down` (M2 §10), exit 1 |
| `gh` unavailable during `rm` | exit 4, name the check that could not run |
| Caller is standing in the target | refuse, exit 3 |

## 10. Open questions

- B11.15's recreate-from-branch recipe now sits entirely in M7, since `wt` no longer branches from anything. Worth checking that it still needs writing down once the create skill exists.
- Does `rm` still belong here, given `wt` does not create? It does — teardown needs the registry entry and the drivers, neither of which the creating tool has — but it makes the lifecycle deliberately asymmetric, and the skills in M7 have to explain why.
- Can `wt` detect *which* tool created a worktree, and does it need to? Claude Code's trees are identifiable by path convention (B11.13), which is weak evidence and would be wrong for a hand-made tree in the same location.
- Do hooks need a declared timeout, and what is a sensible default for one that legitimately takes minutes (B4.4's warm-up)?
- Does `start` need a `stop` counterpart that leaves the allocation intact? Nothing in the requirements asks for it, but `--keep-vm` (B4.3) implies the user sometimes wants the environment down but not gone.
- Should the reaper run on `init` as well as `rm`? B7.1 says before any filesystem mutation, and an idempotent `init` that rebuilds is a filesystem mutation.
