# M6 — Fleet Health

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** T16, B16.5's partial states, view reclamation from M1 §5.2, and open questions 3 and 5 from the requirements.
**Depends on:** M2 for the registry, M3 for driver teardown and verify, M4 for `init`'s idempotence, M1 for view identity.

## 1. What this module answers

What exists on this machine, what is wrong with it, and what fixes each thing.

It is the only module that operates across repositories, and the only reason open questions 2 and 3 have an answer — "what's running on this machine, from any repo" is a question one registry can answer and three cannot.

## 2. Orphans are the normal case

The overview gave this module a light role. Two later decisions made it load-bearing.

`wt` does not create worktrees (M4 §2), so the tool that does — Claude Code, the dispatch harness, a hand-typed `git worktree add` — will also remove them, and will not call `wt rm` first. A12's ordering is unenforceable on that side.

Disposable containers get a fresh view id per run (M1 §5.2), so their entries outlive the view that may write to them.

In the source repos, an orphaned entry meant somebody had made a mistake. Here it is the ordinary end of a worktree's life. That changes what `reconcile` is for: not tidying up after an error, but completing a lifecycle whose last step nothing else performs.

## 3. `list`

Cross-repo by default, table plus `--json` per A15.

| Column | Source |
|---|---|
| app, slug, slot | registry |
| description | B11.16, required at `init` |
| resources | denormalised into the entry (M2 §4), so no repo's spec is read |
| state | `reserving` / `active` / `tearing-down` (M2 §10) |
| view | blank when it is this view |
| flags | stale, foreign, over-age |

Two markers, and keeping them distinct is the point:

- **stale** — bacio's `!`. The entry's directory is gone and this view could have seen it. Actionable from here.
- **foreign** — the entry belongs to another view, so its path cannot be checked. Not a fault, and not something to fix from here.

Collapsing those two into one marker is the mistake overview §6.3 exists to prevent, and it is the mistake a `list` implementation makes by accident.

`--wide` reveals the seed credentials M2 stores (B10.1). They are redacted by default so that reading them is a deliberate act, and so that a screenshot of `wt list` is safe to paste.

## 4. `doctor`

B16.4: report drift, change nothing, and have every finding name the exact command that fixes it.

| Finding | Fix named |
|---|---|
| entry present, directory gone | `wt rm <slug>` or `wt reconcile` |
| directory present, no entry | `wt init` in that directory |
| entry present, descriptor missing or unreadable | `wt init` |
| `reserving` older than its timeout | `wt reconcile` |
| `tearing-down` with surviving resources | the driver's own remedy, then `wt rm` again (B2.3) |
| resource drift — port held by something else, compose project gone, VM missing | `wt init` to rebuild |
| compose file pins `name:` | D2, from M3 §4.2 — the repo needs `-p`, and this is the finding that catches the silent-attach failure |
| duplicate managed key outside the `.env` block | D6, `wt init` re-emits and strips |
| band ledger overlap between two apps | the ledger command, and which app to move |
| slot ceiling approaching | `wt cleanup` — B16.6 makes cleanup the remedy every capacity message points at |
| `max_concurrent` machines approaching | B4.2's message, naming what is running |
| entries from views unseen for N days | §6 |

`doctor` reads everything and writes nothing, so it reports across all views. Findings about foreign views are phrased as unverifiable rather than broken, because from here that is all that is known.

Requiring every finding to name its fix is worth keeping as a hard rule rather than a style preference. A report that lists problems without remedies gets read once.

## 5. `reconcile`

The requirements' open question 5 asked whether repair is worth owning centrally. It is, and it is cheap, because M4 §3.1 already made `init` idempotent — reconcile is `init`'s repair path applied to entries rather than to cwd.

Writes are view-scoped per M2 §12.

| State | Repair |
|---|---|
| entry present, directory gone | driver teardown by handle (D8), drop the entry |
| descriptor present, no entry | rebuild the entry from the descriptor (A7) |
| entry present, descriptor missing | re-emit the descriptor from the entry, and say that is what happened |
| `reserving` past its timeout | roll the allocation back |
| registry unparseable | rebuild from every descriptor this view can see |

The last row is A7's "rebuilding the registry from descriptors must always be safe" doing its intended job. It only ever covers this view's entries, since a container rebuilding from what it can see would silently drop every host entry.

Two rails. `reconcile` never removes a worktree directory — that is §7's job, and it needs evidence `reconcile` does not have. And `--dry-run` is required, not optional, because this verb's whole surface is destructive.

## 6. Views

The problem M1 §5.2 handed over: entries accumulate under views that no longer exist and that nothing is permitted to destroy.

### 6.1 Reclamation

`wt views` lists known views with their last-seen times. `wt reconcile --adopt-view <id>` transfers that view's entries to the current view, after a confirmation that names how many entries and how old the view is.

Adopting a view that is still alive would let two views both believe they own an entry, and last-seen is weak evidence — a view that has been idle for a week is indistinguishable from one that died. So adoption of a recently-seen view is refused by default and needs an explicit override, with the age stated in the warning.

### 6.2 Declaring a view ephemeral

Reclamation is garbage collection, and the disposable-container case does not need it, because the container's operator knows in advance that its paths are disposable.

`WT_VIEW_EPHEMERAL=1` marks a view's entries as adoptable without ceremony. The host can then tear them down by handle as soon as the view goes unseen, with no confirmation dance — which is safe precisely because D8 already requires teardown to work without the directory.

This passes M5 §6's inheritance test: every process in a disposable-clone container is in a disposable clone, so a child inheriting the value is correct. It is a declaration about the environment, not a policy about a task.

It also puts the decision in the right place. Overview §6.3's rule — an invisible path is not a gone path — is a safe default for a view that has not said otherwise. A view that declares itself disposable has said otherwise.

## 7. `cleanup`

B16.1: scan the git worktrees, ask `gh` whether each branch's PR is merged, and for merged ones destroy the runtime, remove the worktree and drop the registry entry.

B16.2: `--dry-run` previews exactly what would be cleaned.

### 7.1 Safety

`cleanup` is the only verb that destroys a worktree unattended, so M4 §7.1's checks apply in full even when the PR is merged. A merged PR says the branch landed; it says nothing about whether the tree is clean.

| Condition | Behaviour |
|---|---|
| uncommitted changes | skip, report |
| unpushed commits, or no upstream | skip, report |
| PR not merged, or no PR | skip |
| `gh` missing or unauthenticated | clean nothing, exit 4 |
| entry belongs to a foreign, non-ephemeral view | skip |
| primary checkout | never a candidate (A2) |

`gh` being unavailable stops the whole verb rather than falling back to a heuristic. Guessing at merge status is how a sweep deletes work.

### 7.2 Scheduling

B16.3 wants an installable OS scheduler job — mini-infra ships an hourly launchd agent installed and removed through a flag. `wt schedule install [--remove]` covers it, through M8 for the platform's scheduler.

One job per machine rather than one per repo, since the tool is machine-global. C8 makes the posture a policy choice — manual for bacio and harness, a scheduled sweep for mini-infra, driven by how expensive an idle worktree is — so each app declares its own in the spec, and the single job honours those declarations.

The scheduled run is deliberately more conservative than the interactive one: merged PRs with clean trees only, never adopting a view, logging every decision including the skips. An unattended destructive sweep earns its keep by being boring.

## 8. Cross-repo

Open question 2 asked about port collisions between repos, and question 3 about a machine-wide view. Both are answered by there being one registry and one band ledger (M2 §6).

`doctor` reports what that makes visible: two apps holding overlapping bands, an allocation sitting outside its app's band, and a port in the ledger that something unregistered is holding. None of those could be detected at all when each repo had its own registry.

## 9. Failure modes

| Situation | Behaviour |
|---|---|
| Registry unreadable | `doctor` still runs and reports that as its first finding |
| A repo's spec unreadable or gone | `list` unaffected — entries are self-describing (M2 §4) — and `doctor` reports it |
| Teardown during `reconcile` leaves resources | `tearing-down`, slot not freed, per B2.3 |
| `gh` unavailable | `cleanup` exits 4; `doctor` and `list` are unaffected |
| Adoption requested for a live view | refuse, name the last-seen age, require the override |
| Scheduled run finds nothing to do | log nothing beyond a heartbeat |

## 10. Open questions

- What is N, for "views unseen for N days"? Too short and a laptop that was closed over a weekend gets its entries adopted out from under it.
- Should `doctor` have an exit code that distinguishes findings from failures, so a scheduled job can alert on drift without alerting on its own success?
- Does `cleanup` need to handle a merged PR whose worktree another view owns and which is not ephemeral? Currently it is skipped forever and only `doctor` ever mentions it.
- Is there a safe way to detect that a foreign view is dead rather than idle? A pid is meaningless across a container boundary, and last-seen is all there is.
- Should `list` show worktrees that exist on disk but have no entry, or is that only a `doctor` finding? Showing them makes the list truthful and makes the un-initialised state visible, at the cost of listing every quick branch anyone ever made.
