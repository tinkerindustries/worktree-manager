# Scope baseline

**Status:** baseline for [`plan.md`](plan.md). Check work against it; change it deliberately or not at all.
**Date:** 2026-08-12

## What gets built

One Go module, two static binaries. `wtd`, a per-user resident coordinator holding all shared state and every privileged operation. `wt`, a client run per operation, on a host or in a container. Plus a set of files generated into an adopted repository, and one skill that does the adopting.

## The surface, in full

**Verbs.** `init`, `start`, `show`, `guard`, `rm`, `list`, `doctor`, `reconcile`, `cleanup`, `clients`, `spec validate`, `spec explain`, `bands list`, `bands suggest`, `bands reserve`, `ports scan`, `daemon status`, `daemon install`.

**Drivers.** `port`, `namespace`, `cidr`, `state-path`, `machine`.

**Generated per repo.** The `worktree-create` skill, the `worktree-remove` skill, the `SessionStart` tripwire, the `PreToolUse` guard hook, the reference doc with its `CLAUDE.md` tripwire, and the descriptor reader.

**Platforms.** macOS and Linux primary, Windows supported with the reaper's graceful step and the permission model both weaker. A container is Linux with reduced capabilities.

## Where repo-specific behaviour goes

Three seams, and nothing else. A driver allocates a resource type. A hook is a command string the client sequences without understanding. An emitter carries an allocation to the application — descriptor, `.env` block, or native resolution.

Policy is spec configuration. Neither binary branches on repo identity.

## Out of scope

Inherited: Docker-in-Docker, isolating content-addressed caches, serialising instead of isolating, auto-creating an environment on first mutation, named-profile indirection.

Added by the design: the system learns no repo's domain, does not create worktrees, and does not translate paths between filesystem namespaces.

## Decisions that are closed

Reopening one of these is a scope change, not a design discussion.

| | Decision |
|---|---|
| 1 | The coordinator is required. There is no local fallback for any verb but `show` and `guard`. |
| 2 | There is no `create` verb. `init` attaches to a tree something else made. |
| 3 | A client mutates only the entries it created. |
| 4 | One writer, so no lock file and no generation counter. |
| 5 | No view identity. `owner`, `ephemeral` and `path_visible` replaced it. |
| 6 | The committed spec is the adoption signal. An absent descriptor means nothing on its own. |
| 7 | Slot 0 is the primary checkout, never allocated and never managed. |
| 8 | Neither binary performs inference. All judgment lives in the onboarding skill. |
| 9 | Hooks never run in the coordinator. |

Deleted by revision 2 and not to return: `wt schedule install`, `WT_HOME` as a container mount point, the state directory bind-mounted into a container, the host docker socket mounted into a client.

## The drift test

Three questions. Any yes means the scope moved and this file needs changing first.

1. Does the work add a verb, a driver, a generated artefact, or a platform?
2. Does it put repo-specific knowledge anywhere other than a driver, a hook, or an emitter?
3. Does it reopen one of the nine closed decisions?

These are not drift, and need no amendment here: a new spec field, a new `doctor` finding, a driver's implementation for a platform it already claims, a failure message, a test, or an answer to one of the twelve risks in `plan.md` §8.
