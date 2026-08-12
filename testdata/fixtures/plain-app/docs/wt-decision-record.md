# plain-app — adoption decision record

The policy decisions made when this repository was adopted through the
onboarding skill (docs/design/09-onboarding.md phase 2). The spec says
what was chosen; this record says why. Six months later the spec is the
contract and this file is the memory.

## Phase 1 — audit

| Resource | Found | Decision |
|---|---|---|
| Port `api` | the one port the app binds | isolate — one band base per machine |
| `~/.plain-app/db.sqlite` | the app's database, in `$HOME` | isolate, seeded from the shared source |
| `~/.plain-app/cache/` | scratch state | isolate, empty |
| `~/.plain-app/shared/db.sqlite` | the shared store | share — every worktree reaches it unisolated |
| `https://api.example.com/v1` | a third-party API tenant | share — nothing in the repo can isolate it |

## Phase 2 — policy

| Decision | Answer | Why |
|---|---|---|
| C1 which dimensions isolate by default | state-path isolates; the shared store is explicitly `default: shared` | the work in a worktree is on the store's schema, not merely through it — except for `shared_db`, which the developer wants every worktree to reach |
| C2 seeding mode | `db` seeded, `cache` empty, `shared_db` shared | the work is on the store's schema (seeded snapshot); the cache is scratch (empty); the shared store is the shared resource itself |
| C3 descriptor format | json | the repo's entry points read `.env`; the descriptor is machine-read |
| C4 config delivery | environment | every entry point reads the environment (`.env` managed block) |
| C7 base branch | default branch | no stacked or dependent work in this repo |
| C8 cleanup posture | cheap enough for `wt rm` to own it | an idle worktree costs one port and a few paths |
| C9 slot ceiling | 32 | one service, one port per slot; 32 is the design default |

## Phase 3 — bands

The band is machine-local: each machine reserves it with
`wt bands reserve --base api=8200` (the base the generated artefacts
record). A colleague who clones this repo reserves the band on their own
machine; it is not part of adoption.
