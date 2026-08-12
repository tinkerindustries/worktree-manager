# M1 — Identity & Discovery

**Status:** draft, swept for consistency 2026-08-11.
**Owns:** A2, A8 (location only), A9, A10, A16, D4, B12.1, B12.2, and view identity from [overview §6.3](00-overview.md#63-a-path-that-is-not-visible-is-not-a-path-that-is-gone).
**Moved out:** naming that only a creator needs — B11.13, B11.14 and B13.3 — went to M7 when `wt` stopped creating worktrees (04-lifecycle §2.1).
**Depends on:** M8 for path realisation and for the mount's case-sensitivity (08-platform §4.2). Nothing else.

## 1. What this module answers

Three questions, asked before anything else happens:

- Where am I? — classification of the current directory.
- Which root do I act on? — the resolution that D4 got wrong.
- What is this thing called? — slug, branch name, directory name, descriptor path.

Plus one question the container requirement added: which view am I, so other views know what I may not touch.

M1 is pure. It reads git and the environment and returns values. It does not read the registry, does not allocate, does not write files. The gitignore append that A8 also describes belongs to M5, because M5 owns writing. M1 only says where the descriptor goes.

That purity is deliberate: every dangerous mistake in Part D that this module could have prevented was a misreading of git semantics, not a coding error, so this code needs to be testable against real repositories with no other machinery in the way.

## 2. Classification

Four outcomes:

| Outcome | Test |
|---|---|
| Not a repository | `git rev-parse` fails |
| Primary checkout | `--git-dir` equals `--git-common-dir`, no standalone declaration |
| Linked worktree | `--git-dir` differs from `--git-common-dir` |
| Standalone clone | `--git-dir` equals `--git-common-dir`, standalone declared |

A2 makes the primary checkout slot 0, never allocated and never managed. It keeps the committed defaults, so nothing changes for a user who never creates a worktree.

### 2.1 The standalone declaration

B12.1 requires disposable clones with no linked-worktree relationship to be supported at all: an agent that clones the target inside a container and runs compose against the host's socket is the same collision in a different shape. B12.2 is blunt about why the declaration exists: git genuinely cannot tell a disposable clone from the real main checkout, because both have `--git-dir` equal to `--git-common-dir`. The declaration is the only thing making that call.

Declaring it happens once, at `init`, and is recorded in the descriptor. Later verbs read it back rather than requiring it again, which is A11 applied to the one fact that cannot be derived. The descriptor is gitignored, so the declaration cannot travel into a real checkout through git.

The failure mode is the right way round. Forget the declaration and the clone classifies as a primary checkout, so `init` refuses, because A2 forbids managing slot 0. The error is loud and nothing is clobbered. The opposite arrangement — a marker file that makes a checkout disposable — fails toward allocating against someone's main checkout, so it is not available.

`WT_STANDALONE=1` is accepted for container images whose entire purpose is disposable clones. This looks like the D3 trap and is not, by the same test as `WT_HOME` in overview §6.1: ask whether a child process inheriting the value does the right thing. For a disposable-clone image, every process in it is in a disposable clone, so inheritance is correct. It must never appear in a shell profile on a development machine, where the same reasoning fails immediately.

## 3. Root resolution

D4's incident: `--show-toplevel` and a walk-back-to-the-main-checkout helper look interchangeable, until `init` run from a linked worktree overwrites the main worktree's manifest.

The rule is that the acting root is always the current worktree's own root, from `git rev-parse --show-toplevel`.

The main checkout's path is still needed — M5 seeds a new worktree's `.env` from it (B17.3), and M9 reads its committed defaults. It is exposed as a field on the classification result rather than as a function, because a function sitting beside `worktreeRoot()` is the shape of the original bug. A call site has to reach through the classification to get it, which makes the reach visible in review.

### 3.1 Containment testing

D1's enforcement needs to ask whether a path lies inside a worktree, and so does anything checking that a descriptor belongs to the tree it was found in. That test lives here as a shared primitive because M7's guard and M3's purge refusal both need it, and two implementations of the one test that must not be wrong would diverge.

Two rails:

- Resolve symlinks on both sides before comparing. On macOS this is not optional: `/tmp` resolves to `/private/tmp`, and paths under `/Users` can arrive via `/System/Volumes/Data`. An unresolved comparison reports a path outside the tree that is in fact inside it.
- Compare on path segments, not string prefixes. `/a/b` does not contain `/a/bc`.

## 4. Naming

### 4.1 Slug

The identity. `^[a-z0-9][a-z0-9-]*$` per A16, one implementation used by the registry, container namespaces and file paths alike.

It defaults to the worktree directory basename, never the branch. The glossary's reasoning stands on its own — branches get renamed and deleted, directories do not.

Length is capped at 32 characters. Two constraints bind:

- A compose project name derived from the slug becomes part of network and container names, which are resolvable as DNS labels and so limited to 63 octets. The project name is only one component of `<project>-<service>-<n>`, so the slug needs headroom.
- Any unix socket path under a slug-named directory has to fit `sun_path`, which is 104 bytes on macOS and 108 on Linux.

M1 validates a slug; it never invents one. Generating a memorable name (B11.14) belongs to whatever creates the worktree and therefore names the directory, which is M7's create skill.

Collision handling is not here either. The user-supplied half of B11.9 needs the registry and belongs to M4 (§2.3 of that document); the generated half went to M7 with the generator.

### 4.2 Derived names

One string comes from the slug here: the descriptor path, a fixed filename at the worktree root (§4.3).

Branch names (B13.3) and the `.claude/worktrees/<slug>` directory convention (B11.13) are derived by whoever creates the worktree, which is no longer `wt`. M7 owns both. M1 still needs to *recognise* the directory convention when classifying, but recognising a path and choosing one are different jobs and only the first is left here.

### 4.3 Descriptor location

A8 puts the descriptor at the worktree root and gives a reason: `ls` should show it, so it is not buried in a dot-directory.

The filename carries no leading dot, for the same reason. A dotted name hides the file from plain `ls` exactly as effectively as a dot-directory does, so `bacio-env.yaml` rather than `.bacio-env.yaml`. C10 records only that all three repos chose the root over a dot-directory; nothing in the requirements takes a position on the filename itself, so A8's stated reason is the only guidance and it points one way.

## 5. View identity

New with the container requirement. Overview §6.3 establishes the rule: registry entries record the view that created them, entries from another view are read for allocation but never destroyed or stale-detected.

M1 computes the view.

### 5.1 Mechanism

A view id is a random identifier generated on first use and persisted **outside** the shared state directory — in the container's or host's own filesystem, not in `WT_HOME`. That placement is what makes it work: two containers sharing one host mount have separate root filesystems and therefore separate ids, while the host keeps one stable id across all of it.

Alongside the id, the view records metadata for humans reading `list` and `doctor`: hostname, OS, whether a container was detected, the uid, and `WT_HOME` as this view resolves it. None of it participates in scoping decisions — only the id is compared — but "which machine was that?" is a question the output should answer.

Deriving the id from the environment instead was considered and rejected. `/etc/machine-id` inside a container is frequently the host's, empty, or baked into the image and therefore identical across every container from that image, which is precisely the case that must produce distinct ids.

### 5.2 Consequences to hand downstream

A fresh container on every run means a fresh view id on every run, so registry entries accumulate under views that no longer exist and that nothing is permitted to destroy. This is not a corner case — it is the ordinary lifecycle of a disposable agent container.

M1's obligation is to make the id and its last-seen metadata available. M6 owns the remedy, and needs one: foreign-view entries reported as unverifiable rather than stale, and an explicit transfer of ownership after the user confirms. Without it the registry fills with undeletable rows and slot exhaustion arrives on schedule.

## 6. Public surface

| Call | Returns |
|---|---|
| `classify(cwd)` | outcome, worktree root, main checkout path, git common dir |
| `worktreeRoot(cwd)` | the acting root, per §3 |
| `contains(root, path)` | boundary-safe, symlink-resolved containment |
| `validateSlug(s)` | ok, or the reason it failed |
| `descriptorPath(root, spec)` | where the descriptor lives for this tree |
| `view()` | id plus metadata |

## 7. Failure modes

Each of these produces a named error, not a stack trace, per B15.3's posture:

| Situation | Behaviour |
|---|---|
| Not a repository | every worktree verb stops and says so |
| Primary checkout, `init` attempted | refuse, cite A2, name the standalone declaration as the alternative if that is what the user meant |
| Standalone declared in a linked worktree | refuse — the combination is meaningless and probably a copied descriptor |
| Worktree directory deleted while cwd is inside it | git fails; report that rather than translating it |
| A worktree nested inside another worktree | refuse |
| `git` absent from `PATH` | name the install command |

## 8. Testing

Classification is tested against real repositories created in temporary directories, not against mocked git output. The bug this module exists to prevent (D4) was a misreading of what git reports, so a test built on a mock of that same misreading would pass while the bug survived.

The fixtures needed: a plain repository, a repository with two linked worktrees, a clone, a worktree whose directory has been removed, and one of each on a path that reaches through a symlink.

## 9. Open questions

- Should the view id be persisted at a fixed path, or is a path that varies by platform (M8) acceptable given a container may have no writable state directory outside the mount?
- Is a read-only root filesystem a supported container configuration? It has no place to persist a view id, which would force one per invocation and make every entry immediately orphaned.
- Does `derive` need to guarantee that two distinct slugs never produce the same compose project name after the cap in §4.1 truncates them? Truncation collisions are silent, and D7 forbids silent.
