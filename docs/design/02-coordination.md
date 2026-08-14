# M2 — Coordination Core

> **Superseded in part.** Everything this document says about the wire and
> about storage was replaced by the HTTP + SQLite rearchitecture: the
> newline-delimited-JSON-over-unix-socket protocol is now HTTP on a loopback
> port, and the four JSON store files are now one SQLite database. Read
> `docs/ARCHITECTURE.md` and the root `ARCHITECTURE.md` for what is true
> now. The allocation model this document describes — slots, entry states,
> the one-writer rule, reclamation — survives unchanged, and is why the
> document is kept rather than deleted.

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** A4, A5, A6, A7, A14, B1.4, B1.5, B8.1, B8.3, B10.1 (storage only), B12.3, C5, C9, and overview §5 and §6.1 to §6.3.
**Depends on:** M1 for slug rules and view identity. M8 for filesystem behaviour.

## 1. What this module answers

Which slot does this worktree get, what does it own, and how do two processes that cannot see each other avoid handing out the same thing twice.

Everything here is machinery. It holds no knowledge of what a port or a container is for — that is M3. It is the only module that writes to the shared state directory, so every concurrency and durability concern lives in this document.

## 2. The state directory

`WT_HOME`, defaulting to `$HOME/.wt`, per overview §6.1. A container mounts the host directory anywhere and points the variable at it.

```
$WT_HOME/
  registry.json     every worktree entry, every repo
  bands.json        port band ledger and host-global reservations
  views.json        known views, last seen
  lock              exclusive-create lock file
```

One registry file rather than a file per entry. Multi-entry operations stay atomic under a single rename, rebuild-from-descriptors stays simple, and the write rate does not justify finer contention — allocations happen a few times a day, and §8 keeps the lock held for milliseconds.

The directory is created `0700` and the files `0600`. B10.1 puts seed credentials in the registry, so this is not incidental.

## 3. Format

The registry is JSON, and this is not the same decision as C3.

C3 leaves the *descriptor* format to whatever the repo already parses, which is right — it is the repo's file. The registry is `wt`'s own file and only `wt` writes it, so the format should be chosen for unambiguity.

YAML is disqualified by a concrete hazard rather than by taste. Slugs match `^[a-z0-9][a-z0-9-]*$`, which admits `no`, `on`, `off`, `yes`, and `y`. YAML 1.1 parsers coerce all of those to booleans. A worktree slugged `no` would round-trip through the registry as `false`.

That hazard follows the slug wherever it goes, so it constrains M5 too: any descriptor emitted as YAML must quote every string scalar rather than relying on the value not looking like a boolean. Recorded here because M1 and M2 own the slug rule between them.

## 4. Registry entry

| Field | Purpose |
|---|---|
| `app` | namespace key, from the spec — see §5.1 |
| `slug` | identity (M1 §4.1) |
| `slot` | the integer everything derives from (A4) |
| `view` | which view created it (M1 §5, overview §6.3) |
| `path` | as the creating view saw it — informational only (A9) |
| `state` | `reserving` / `active` / `tearing-down` (§10) |
| `resources` | the derived values, so `list` never needs the repo's spec |
| `descriptor_path` | where the descriptor was written |
| `description` | required human metadata, ≤10 words (B11.16) |
| `secrets` | seed credentials (B10.1), redacted unless `--wide` |
| `created_at`, `last_seen` | staleness and view reclamation |
| `schema_version` | §11 |

`resources` being denormalised into the entry is deliberate. `wt list` across every repo on the machine cannot depend on each repo's spec being readable — the repo may be inside a container, on a disconnected volume, or checked out at a revision whose spec has changed. The registry has to be self-describing.

`secrets` gets a named field rather than living in `extras` so redaction is structural. A field the code knows is a secret cannot be printed by a `list` implementation that was written before that secret existed.

## 5. Slot allocation

A4 makes one integer drive everything. A5 makes allocation lowest-free and stable: an existing entry's slot is authoritative and is never reassigned under a running worktree.

### 5.1 Namespace

Slots are per app, so `bacio` slot 3 and `mini-infra` slot 3 coexist. The key is the spec's `app` field, not the repository path — a standalone clone (M1 §2.1) has a different path and must still share slot space with the checkout it was cloned from.

That makes `app` a machine-global name. The band ledger enforces uniqueness by making registration claim it (§6.2).

### 5.2 The algorithm

Lowest integer, from 1 upward, that is not held by an active or reserving entry for this app, not excluded (§7), and whose every derived resource probes free (M3). Slot 0 is the primary checkout and is never allocated (A2).

`init` is idempotent per A1: an existing entry for this slug reconciles and rebuilds, never reallocates.

### 5.3 Ceiling and exhaustion

The ceiling comes from the spec, default 32. C9 records that the three source repos chose 32, 100 and an open port-space walk, all correctly for their resource cost per slot.

Exhaustion is actionable per B1.4, and B16.6 fixes what it points at — the message names the range and the command that frees slots, which is M6's `cleanup`. It also states how many of the occupied slots belong to other views and therefore cannot be freed from here, because otherwise the remedy it names will appear to do nothing.

### 5.4 Back-derivation

B1.5 wants the slot recoverable from an entry's ports, so entries written before the field existed still resolve. `wt` writes the slot on every entry, so this is not a runtime concern. The requirement's underlying need — old data stays readable — is met by the schema version and migration in §11 instead.

Back-derivation does have a use, in M9: importing worktrees that an existing per-repo implementation allocated before adoption. It belongs there.

## 6. Port bands

Overview §5 keeps slot namespaces per repo and makes port space global. The mechanism is the band ledger.

### 6.1 Two allocation forms

A4's legibility property — harness's slot 7 having every port end in `07`, readable straight out of `docker ps` — only holds in one of the two forms, and the spec has to declare which it uses.

**Stride.** Each service gets a base that is a multiple of 100. `port = base + slot`. Slot 7 with bases 4200 and 8000 yields 4207 and 8007. Legible, and it caps slots at 99, which is where mini-infra's 100 came from. `spec validate` rejects a `slots.max` above 99 on a stride spec, since the ceiling is a property of the allocation form rather than a free choice.

**Group.** Where ports come in a fixed relationship that something external requires — B1.2's case, bacio's reverse proxy sitting at `api_port − 1` — the group is allocated as a unit with fixed internal offsets: `port = base + slot × size + offset`. Groups stay disjoint, which is what B1.2 asks for, but the digits no longer encode the slot.

Stride is the default. Group is for relationships the repo does not control.

### 6.2 The ledger

`bands.json` maps each app to the bases it holds, and holds machine-wide reservations that no app may allocate from.

Registration is explicit. `wt` refuses to allocate for an app with no registered bases and names the command, rather than grabbing a free range on the fly. Silent self-service would make the ledger's contents depend on the order in which repos happened to be used.

`wt` computes the required size from the spec — services × slot ceiling — so M9's skill only has to choose where the bases sit, not how large they are.

## 7. Exclusions

B8.1 requires production ports to be unallocatable even by a hand-edited manifest, and B8.3 extends that to a repo's committed defaults.

Exclusions come from two places:

- **Per-repo**, in the spec's `reserved` block: the repo's own committed defaults, which are a property of the repo.
- **Host-global**, in `bands.json`: production stacks, and anything else the machine runs. `deepseek-harness-prod` on 8180/8190/4522/8522 is a fact about the machine, not about the harness repo.

Making production reservations host-global is a change from the source implementations and an improvement on them. In the current arrangement only the repo that knows about the prod stack avoids it; a second repo's allocator has never heard of it. One ledger means every allocator respects it.

Enforcement happens at two points, because B8.1 says "even by a hand-edited manifest": at allocation, and again when validating a descriptor that `wt` did not write.

## 8. Concurrency

Overview §6.2 rules out `flock`, because advisory locks do not propagate across a virtiofs bind mount on macOS and macOS is a primary platform.

Two mechanisms, layered.

**The lock**, as the ordinary path: exclusive creation of `$WT_HOME/lock` (`O_CREAT|O_EXCL`) containing the owner's view id, pid and creation time. C5's numbers carry over — 5s acquisition deadline, 50ms poll, 15s stale timeout after which the lock may be broken.

**The generation counter**, as the guarantee: `registry.json` carries a monotonic generation. A writer reads the file, computes its change, and writes only if the on-disk generation still matches what it read. On mismatch it retries from the read.

The counter covers what the lock cannot. A holder that hangs past the stale timeout gets its lock legitimately broken by a second process and then writes anyway; only the compare-and-swap prevents that lost update. The lock keeps the common case from spinning; the counter is what makes the result correct.

[Measured](experiments/virtiofs-guarantees.md): exclusive creation is atomic over a Docker Desktop virtiofs bind mount — 5,500 contested creates across the boundary, no double wins, and a negative-lookup poisoning test that never produced a false win. That removes the second reason the counter was introduced, and leaves the first intact.

Neither advisory locking mechanism crosses the boundary. `flock` was ruled out in overview §6.2 and POSIX record locks fail the same way, so the exclusive-create lock is the only mechanism available rather than the preferred one.

One lock covers the whole state directory rather than one per file. An allocation has to read the band ledger and write the registry as one operation, so separate locks would only introduce an ordering problem. This closes the overview's open question about whether the ledger needs its own lock.

### 8.1 What the lock does not cover

Materialisation runs outside the lock. Creating a Colima profile or a WSL distro takes minutes (B4.4), and holding a global lock for that would serialise every repo on the machine behind one VM warm-up.

So allocation commits a `reserving` entry under the lock, releases, materialises, then flips the entry to `active` under the lock again. The slot is held throughout, which is the only thing the lock was protecting.

## 9. Durability

A14: temp file in the same directory, then rename. Same directory matters, because a rename across filesystems is not atomic and `$WT_HOME` may be a mount point.

The sequence is write temp, fsync the temp file, rename, fsync the directory. The directory fsync is what makes the rename itself durable on Linux.

[Measured](experiments/virtiofs-guarantees.md): rename is atomic over the bind mount in both directions — 206,000 reads of a 256 KiB file under continuous replacement, with no torn, short or absent observation. The generation counter still limits the damage on filesystems where this has not been measured: a torn or lost write is detected on the next read rather than silently accepted.

## 10. Entry lifecycle

| State | Meaning | Recovery |
|---|---|---|
| `reserving` | slot claimed, resources not yet materialised | a `reserving` entry older than its timeout is incomplete; M6 reports it and offers teardown |
| `active` | fully materialised | normal |
| `tearing-down` | teardown started | if teardown left resources behind, B2.3 says do not free the slot; the entry stays here with a note listing what survived |

A crashed `init` leaves `reserving`, which is exactly what A13's rollback is meant to clean up. A13 puts the rollback in `init`; this state is the backstop for the case where the process died before it could roll anything back.

`tearing-down` as a resting state is B2.3 made explicit. The requirement says to leave the registry entry and tell the user to fix the cause and re-run, rather than hand-editing the registry. Giving that situation its own state means `list` and `doctor` can describe it instead of showing an `active` entry whose resources are half gone.

## 11. Versions

Two independent versions, both enforced by refusing rather than by best-effort.

**Registry schema.** `wt` reads a registry newer than it understands well enough to `list` it, and refuses to write it, naming the upgrade. A7 requires in-place migration, following mini-infra's `worktrees.json` → `worktrees.yaml` precedent; migration runs under the lock and writes through the same atomic path as everything else.

**Spec version.** `wt` refuses a spec declaring features it cannot honour, rather than applying the part it understands. D7 is the reasoning: a partial application is a silent truncation, and it would produce a worktree that looks allocated while some resource it declared is shared.

## 12. View scoping

Overview §6.3 in mechanical terms.

Reads are global. Allocation considers every entry regardless of view, which is the entire reason the registry is shared — a slot taken by an agent's container is unavailable to a concurrent host worktree and the reverse, which is B12.3's requirement.

Writes are scoped. An entry may be modified or removed only by its own view. From another view it is readable, counts against allocation, and is never treated as stale.

A7's rebuild-from-descriptors inherits the same scoping. A container rebuilding from what it can see would drop every host entry, because it cannot read host descriptors. Rebuild only ever touches its own view's entries.

`views.json` records each view's metadata and last-seen time so M6 can offer to transfer ownership of entries whose view is gone — the disposable-container problem M1 §5.2 hands over.

## 13. Public surface

| Call | Purpose |
|---|---|
| `open()` | resolve `WT_HOME`, check permissions, migrate if needed |
| `withLock(fn)` | acquire, run, release, retry on generation mismatch |
| `allocate(app, slug, spec)` | lowest free slot plus derived resources, committed as `reserving` |
| `activate(id, resources)` | flip to `active` |
| `entries(filter)` | read, unscoped |
| `update(id, fn)` / `remove(id)` | view-scoped, refuse foreign |
| `bands.reserve(app, bases)` / `bands.reservations()` | ledger |
| `rebuild(view)` | A7, own view only |

## 14. Failure modes

| Situation | Behaviour |
|---|---|
| `WT_HOME` unwritable | clear error naming `--user` and ownership, per overview §6.5 — not a rename failure at the end of a long operation |
| `WT_HOME` unset and `$HOME` unset | stop; do not invent a location |
| Lock held past the acquisition deadline | report the holder's view, pid and age, and stop |
| Generation mismatch after retries | stop; something is writing continuously |
| Registry unparseable | do not truncate and recreate; report, and name `rebuild` |
| No band registered for the app | refuse allocation, name the registration command |
| Slot ceiling reached | name the range, the cleanup command, and how many slots belong to unreachable views |
| Write attempted on a foreign view's entry | refuse, name the view and its last-seen time |

## 15. Open questions

- ~~Is `O_CREAT|O_EXCL` atomic over virtiofs, and is `rename` atomic there?~~ — [measured](experiments/virtiofs-guarantees.md), both yes on Docker Desktop for macOS. Colima, WSL2 and network filesystems remain untested.
- Does the generation counter make the lock optional altogether? Dropping the lock would remove the stale-timeout logic entirely at the cost of more retries under contention. The measurement makes this more attractive, not less: the lock's only remaining job is reducing contention.
- Should `bands.json` be shared across machines — checked into a dotfiles repo, say — so a developer's port map is consistent everywhere? It would make bands stable but adds a second writer nobody can lock against.
- What owns the reservation for a production stack that no repo on the machine has a spec for? Today nothing writes it; someone has to.
- Should `last_seen` be updated by read operations? It makes view reclamation accurate and makes every `list` a write.
