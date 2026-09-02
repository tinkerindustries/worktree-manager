# M3 — Resource Drivers

**Status:** draft, swept for consistency 2026-08-11. This document defines the spec schema, which is the contract between the onboarding skill (M9) and the binary.
**Owns:** T1 (B1.1, B1.2, B1.3, B1.6), T2 (B2.1, B2.2, B2.3), T3, T4 (B4.1, B4.2, B4.3, B4.5), T5, T6 (B6.1, B6.2, B6.3), B8.2, C1, C2, D2, D8.
**Depends on:** M2 for slot and band assignment, M8 for every platform-specific operation, M1 for slug rules.

## 1. What this module answers

Given a slot, what does this repo actually get, and how is each of those things checked, created, inspected and destroyed.

M2 hands over an integer. M3 turns it into ports, project names, subnets, paths and virtual machines, and knows how to take each of them away again.

## 2. The driver contract

Six operations. Two of them are optional, and which two is the most useful thing this document says about the shape of the problem.

| Operation | Required | Purpose |
|---|---|---|
| `derive(slot, spec, env)` | yes | compute the value |
| `probe(value)` | yes | is it free right now |
| `apply(value)` | no | create it, where creation is `wt`'s job |
| `teardown(handle)` | no | destroy it |
| `verify(value)` | yes | report drift, change nothing |
| `blastRadius(spec)` | yes | prose for the shared block |

### 2.1 Derived and provisioned resources

`apply` and `teardown` are optional independently, and the four combinations are all real:

| | no teardown | has teardown |
|---|---|---|
| **no apply** | port, cidr — a number written into a descriptor, materialised by the app when it binds or routes | namespace — the app's compose run creates the containers, `wt` destroys them |
| **has apply** | — | state-path, machine |

The top-right cell is the interesting one, and it explains B2.2. `wt` never creates a container, so it never holds a handle from creation. It has to find them again by asking the daemon for everything carrying `com.docker.compose.project=<name>`. That is why the requirement says to tear down by label and never by reading a compose file, and it generalises: a driver that does not create must discover.

### 2.2 Handles survive the directory

D8's rule, stated once for every driver: teardown takes its handle from the registry, never from anything on disk in the worktree. People run `git worktree remove` by hand first, and a teardown that needed a compose file, a descriptor or a lockfile from inside the tree would strand the resources permanently.

Every handle a driver needs is therefore derivable from `slot` and `spec` alone, which is what makes M2's denormalised `resources` field sufficient.

### 2.3 Context requirements

Per overview §6.4, each operation declares what it needs to be meaningful: host network namespace, host pid namespace, docker socket. When the context is absent the operation reports `unavailable` and says so. It never returns success having done nothing.

`unavailable` is a third result, distinct from success and failure, and the distinction matters at the call site: an unavailable probe does not block allocation, whereas an unavailable teardown does block freeing the slot.

## 3. Spec schema

### 3.1 Shape

```yaml
version: 1
app: bacio                      # machine-global name, keys the slot namespace (M2 §5.1)

slots:
  max: 32                       # C9

resources:
  - type: port
    name: api
  - type: namespace
    name: compose
    kind: compose
    template: "{app}-{slug}"
  - type: state-path
    name: db
    template: "{home}/.bacio/worktrees/{slug}/db.sqlite"
    default: shared

reserved:
  ports: [5319, 5320]           # B8.3 — the repo's committed defaults

shared:                         # B11.4, augmented automatically — see §6
  - name: "{home}/.bacio/db.sqlite"
    impact: writes are visible to every worktree and the main checkout

hooks: {}                       # M4
emit: {}                        # M5
```

One spec per repository, not one per runnable application. The worktree is the unit of isolation, so a monorepo's worktree allocates the resources of everything in it under one slot. Two applications that run independently still share a slot when they share a tree, which costs port-space and nothing else.

### 3.2 The spec does not contain port numbers

This is a correction to the sketch in the overview, which had a `band: 5400-5799` field.

The spec is committed to the repository. A band is a fact about one machine, held in the host-global ledger (M2 §6.2). If the spec pinned the band, two developers could not differ, and the ledger would stop being authoritative the moment a repo disagreed with it.

So the spec declares how many ports there are, what they are called and how they relate to each other. The ledger decides where they sit. `reserved` is the exception and belongs in the spec, because a committed default port genuinely is a property of the repository.

### 3.3 Templates

Values are templates over `{app}`, `{slug}`, `{slot}`, `{home}`, `{worktree}` for the worktree root, and the names of other resources.

Cross-resource references are what makes T3 expressible without special-casing it. B3.1 wants a second compose project named `${COMPOSE_PROJECT_NAME}-test`, and B3.3 insists the derivation match wherever else it is computed, because `rm` tears down by that exact name:

```yaml
  - type: namespace
    name: compose
    kind: compose
    template: "{app}-{slug}"
  - type: namespace
    name: compose_test
    kind: compose
    template: "{compose}-test"
```

Evaluation is a topological sort with cycle detection. Each resolved value is validated against its type's constraints before anything downstream uses it.

### 3.4 Isolated by default, or not

C1 records that the three repos isolate different dimensions by default and all three are right. B6.1 adds that state isolation has to be selectable independently of port isolation.

Both fall out of a per-resource default plus the flag that flips it:

```yaml
  - type: state-path
    name: db
    default: shared
    flag: "--isolate-db"
```

`default: isolated` with no flag is the ordinary case. `default: shared` marks a resource the tooling knows about, does not isolate, and lists in the shared block — which is how bacio's position is expressed: a dispatched worker has to reach the real ticket, so the shared store is the point, and isolation exists only for testing schema migrations.

## 4. The drivers

### 4.1 `port`

**Derive.** From the ledger's assignment for this service (M2 §6.1), in stride or group form.

**Probe.** Bind on loopback, release immediately. A slot whose ports are held by something outside the registry is skipped (B1.3).

Two limitations, both documented rather than fixed. Inside a container the bind lands in the container's network namespace, not the one compose publishes to, so the probe cannot see host ports — it reports `unavailable`. On the host, a port published by a container is bound by the daemon and will be seen, but a port a container binds internally will not.

Allocation proceeds when the probe is unavailable, and the output states that the registry was the only check performed. B1.3 already says the registry is what actually prevents collisions and the probe is a bonus; refusing to allocate without it would make `wt` unusable in exactly the containerised case that motivated sharing the registry. This closes the overview's open question about whether a container may allocate at all.

**Teardown.** None. A port is not a thing that exists.

B1.6 is a rail on the whole driver: a held port is never remediated. `wt` does not kill whatever holds it, because that is probably the user's own running instance. It skips the slot and says which slot it skipped and why. Reaping processes bound to a worktree's *own* ports is a different operation with different safety rules and belongs to M4.

**Verify.** Report whether each port is bound, and by what where the platform can tell.

### 4.2 `namespace`

A derived name. `kind: compose` gets docker-specific behaviour; `kind: plain` is a string the app uses however it likes.

**Derive.** Template. B2.1's `COMPOSE_PROJECT_NAME`. Once the project name is namespaced, named volumes and networks isolate for free, which is why the audit during onboarding is about finding what is *not* project-scoped rather than about the containers themselves.

**Probe.** For `kind: compose`, whether any object already carries that project label. A hit means a previous teardown was incomplete, not that the slot is taken.

**Teardown.** By label, per §2.1 and B2.2. Containers, then networks, then volumes, for the project and every dependent project the spec declared — B3.2 requires the test stack's objects to go with the dev stack's.

B8.2 is a rail on this path: no teardown may reach a co-resident production stack. A resolved project name matching a host-global reservation (M2 §7) is refused rather than torn down, because label-based teardown is the one operation that could otherwise reach a stack `wt` knows nothing else about.

If anything survives — an unreachable daemon being the usual cause — the slot is not freed. B2.3: leave the entry, report exactly what survived, tell the user to fix the cause and re-run. M2 §10 gives that its own `tearing-down` state so `list` can describe it.

**Verify, `kind: compose`.** This is where D2 gets caught. A compose file that pins `name:` makes the second worktree silently attach to the first one's containers, which the requirement calls worse than a port collision precisely because nothing fails.

So a compose namespace may declare the files it governs:

```yaml
  - type: namespace
    name: compose
    kind: compose
    template: "{app}-{slug}"
    files: [docker-compose.yml, docker-compose.test.yml]
```

`verify` reads them and reports a pinned `name:`. Where one is present the invocation must pass `-p` explicitly, which beats the file's `name:` — B3.1 states this for the test stack and it holds generally.

Knowing about compose is not a breach of the "no repo domain knowledge" non-goal. Compose is infrastructure `wt` already has to understand in order to tear down by label at all.

### 4.3 `cidr`

**Derive.** Slice the pool by slot (B5.1). Mini-infra carves a `/22` per worktree out of `172.30.0.0/16`, giving four disjoint `/24`s per worktree with no coordination, and the downstream allocator is size-agnostic.

```yaml
  - type: cidr
    name: egress
    pool: 172.30.0.0/16
    size: 22
    on_exhaustion: shared-pool
```

**Probe.** Whether any existing network on the reachable daemon overlaps. Unavailable without a socket.

**Teardown.** None. The network belongs to the namespace driver's project.

**Exhaustion.** B5.2 requires a fallback to the shared pool rather than an outright failure, and D7 requires that the fallback be loud. `on_exhaustion: shared-pool` emits a warning naming the remedy; `on_exhaustion: fail` is available for a repo that would rather stop.

### 4.4 `state-path`

The T6 driver, and the one with the most policy attached.

```yaml
  - type: state-path
    name: db
    template: "{home}/.bacio/worktrees/{slug}/db.sqlite"
    default: shared
    flag: "--isolate-db"
    seed:
      from: "{home}/.bacio/db.sqlite"
      modes: [seeded, empty, shared]
      default: seeded
    purge:
      flag: "--purge-db"
```

A store that is dead weight the moment the worktree goes declares the other form, which is the machine's `keep_flag` shape (§4.5) applied to a state store:

```yaml
  - type: state-path
    name: userdata
    template: "{home}/.bacio/worktrees/{slug}/userdata/"
    purge:
      on_teardown: always
      keep_flag: "--keep-userdata"
```

**Apply.** Create the directory, and seed per mode. C2's three modes are bacio's and generalise: seeded from live, empty, or shared (no isolation at all).

B6.2 requires the briefing to state plainly that a seeded store is a snapshot taken at creation that diverges as work continues, and is not a live mirror. M7 renders that; the driver supplies the fact that seeding occurred and when.

**Teardown.** The repository's policy, from `purge.on_teardown`. `flag` is the default and the behaviour a spec that declares nothing gets: the store survives unless the run selects it, by resource name (`--purge <name>`) or by the `purge.flag` the resource declares. `always` deletes the store as part of the teardown, and `purge.keep_flag` — spelled `--keep <name>` as well — is how one run says otherwise. `wt cleanup` and the coordinator's sweep are teardowns too, and both honour `always`; neither passes a flag, so `flag` mode is untouched by them.

Four rails hold `always`, checked at validation because a store deleted by default is not recoverable:

- `keep_flag` is required, so the one-run opt-out always exists.
- The template must reach `{slug}`, `{slot}` or `{worktree}`, directly or through the resources it references. Without one of them every worktree resolves the same path and the first `rm` deletes the store the others are using.
- `default: shared` refuses it. A shared store is every worktree's, and one worktree's teardown does not get to delete it.
- A run whose seed mode is `shared` purges nothing it was not asked to purge: apply created nothing, and the path is the shared store rather than a copy of it.

B6.3's refusal is structural rather than advisory: a purge whose resolved path is the shared source — or any ancestor of it — is refused, and the refusal names the path. Deleting the shared store would wipe every project's data, so this check runs on the resolved, symlink-realised path using M1 §3.1's containment test, not on the template. It applies to both forms, and to a default purge it is the last check standing.

One rule decides, `spec.Purges`, and both the driver and `wt rm` read it — so what the report names and what the teardown deleted cannot disagree. `wt rm --dry-run` names the stores it would delete and says which of them the spec deletes by default.

**Verify.** Path exists, is writable, and for the seeded mode, when it was seeded.

B6.4 belongs to M5 rather than here: an env var may supply the isolation flag's default, but it is per-invocation only and never a shell profile, because an ambient `*_ISOLATE_DB=1` is inherited by dispatched workers and recreates the exact bug it was added to fix.

### 4.5 `machine`

A per-worktree VM or daemon (B4.1) — a Colima profile on macOS, a WSL2 distro on Windows, each running its own dockerd. The driver selects by platform through M8 and accepts an override.

```yaml
  - type: machine
    name: vm
    driver: auto
    template: "{app}-{slug}"
    max_concurrent: 4
    keep_flag: "--keep-vm"
```

**Apply.** Create and start. Warm-up is measured in minutes, so B4.4 puts it in the background and `init` does not block on it. M2 §8.1 already keeps this outside the state lock; M4 owns the sequencing and the progress reporting.

**Capacity.** B4.2's guard rail refuses to start a *new* instance past the limit, names what is currently running and how to tear one down. Re-running an already-running profile stays allowed, because it is not a new instance. The limit exists for a concrete reason worth carrying into the message: multiple dockerds carve bridge subnets from one address pool, and past roughly four the pool exhausts and network creation fails as `all predefined address pools have been fully subnetted`, which surfaces as something entirely unrelated.

The count is across all views, since the constraint is on the machine.

**Teardown.** Destroy the instance, unless the keep flag is given. B4.3's `--keep-vm` drops the containers and the registry entry but leaves the expensive VM up.

**Documented bypass.** B4.5: the reference doc names `colima delete <profile> --data --force` and `wsl --unregister <distro>` for when the helper cannot run. M7 renders it; the driver supplies the exact command for its platform so the doc cannot drift from the implementation.

## 5. Ordering

Apply runs in dependency order derived from template references, with `machine` forced first among its dependents — a VM has to exist before anything expects its daemon. Teardown runs in reverse.

Teardown continues past a failure rather than stopping at the first one, collecting what survived. Stopping early would leave more behind than continuing does, and B2.3's rule already covers the reporting: the slot is not freed while anything remains.

## 6. The shared block

B11.4 wants a `<shared>` block in the descriptor enumerating what is *not* isolated and the blast radius of each, so a session learns it from inside the worktree without reading the repo docs.

Half of it is generated. Any resource with `default: shared` contributes itself automatically, with its resolved path and its impact text. Hand-listing those would drift the moment a default changed, and a stale shared block is worse than none because it is believed.

The other half is hand-authored in the spec's `shared:` section, for resources `wt` does not model at all — a third-party API, a shared cache, a hardware device. M9 captures those during onboarding, which is precisely the part overview §2.3 says a scanner cannot infer.

## 7. Failure modes

| Situation | Behaviour |
|---|---|
| Template references an unknown resource | reject the spec at validation, not at allocation |
| Template cycle | reject, naming the cycle |
| Resolved namespace exceeds the length limits in M1 §4.1 | reject; truncation collisions are silent and D7 forbids silent |
| Probe unavailable | allocate, and state that the registry was the only check |
| Teardown unavailable (no socket) | do not free the slot; name the mount that would fix it |
| Teardown partially failed | `tearing-down` state, list what survived, do not free the slot |
| Purge target is the shared source or its ancestor | refuse, naming the resolved path |
| `max_concurrent` reached | refuse the new instance, list the running ones, name the teardown command |
| CIDR pool exhausted | fall back with a warning, or fail, per `on_exhaustion` |

## 8. Open questions

- Does a resource need to declare that it must move together with another beyond the port-group case — a state path that has to sit on the same filesystem as a socket, say? Carried from the overview and still unanswered; nothing in the three source repos needs it, which is weak evidence that nothing will.
- Should `verify` be permitted to read files inside the worktree, given §2.2 forbids `teardown` from doing so? Reading a compose file for D2 detection is exactly that, and it is safe because verify changes nothing and is allowed to report "cannot check from here".
- How does the `machine` driver's capacity count stay correct when instances exist that no registry entry describes — created by hand, or by a view that is gone? Counting the daemon's instances rather than the registry's entries is more truthful and more expensive.
- Is `plain` namespace worth having, or does everything that is not compose end up expressed as an env var through M5 anyway?
- What happens to a `cidr` that has fallen back to the shared pool when a slot later frees up? Nothing reallocates it, so the warning persists for the life of the worktree.
