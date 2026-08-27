# The plain-app walkthrough — what the skill produced

`testdata/fixtures/plain-app` is the first repository adopted through this
skill (plan.md §4.1; the acceptance tests in `internal/cli/adoption_test.go`
drive the same phases). What each phase produced, so a real adoption has a
concrete shape to compare against:

## Phase 1 — audit

| Resource | Found | Decision |
|---|---|---|
| Port `api` | the one port the app binds (`.env.example`: `API_PORT=8080`) | isolate — one band base per machine |
| `~/.plain-app/db.sqlite` | the app's database, in `$HOME` | isolate, seeded |
| `~/.plain-app/cache/` | scratch state | isolate, empty |
| `~/.plain-app/shared/db.sqlite` | the shared store | share — reached unisolated by every worktree |
| `https://api.example.com/v1` | a third-party API tenant | share (the hand-authored `shared:` block) |

## Phase 2 — policy

Recorded in `docs/wt-decision-record.md`: C1 state-path isolates by
default with `shared_db` explicitly `default: shared`; C2 `db` seeded,
`cache` empty, `shared_db` shared — the three seed modes the fixture
proves; C3 json descriptor; C4 environment delivery; C7 default branch;
C8 `wt rm` owns cleanup; C9 slot ceiling 32; C6 the guard hook confirmed.

## Phase 3 — bands

`wt bands suggest` proposes the lowest free base; the developer confirms a
base and `wt bands reserve --base api=<base>` claims it. Machine-local: a
clone reserves on its own machine. The generated artefacts record the base
in their facts blocks.

## Phase 4 — spec

`wt.yaml` with one stride port and three state paths covering all three
seed modes; `wt spec validate` passes; `spec explain --slot 1` and
`--slot 2` give disjoint tables (the api ports differ).

## Phase 5 — generate

The seven files of the artefact inventory, each with the managed block:
the two skills, the tripwire and guard hooks, `.claude/settings.json`
(SessionStart always, PreToolUse because the developer confirmed), the
reference doc with its facts block, and the `CLAUDE.md` tripwire — plus
the committed `.gitignore` (the worktree directory, the descriptor, the
`.env`, the build output, the server log).

## Phase 6 — patch entry points

`bin/server.go` lands: the serving entry point reads `API_PORT`,
`DB_PATH`, `CACHE_PATH`, `SHARED_DB_PATH` from the environment — never a
hardcoded value — binds the worktree's allocated port, serves `/healthz`,
and probes it with `-healthcheck` (what the health hook runs, curl-free).

## Phase 7 — prove it

The acceptance test: two worktrees, `wt init` both (the build hook
compiles the server into each tree, the start hook launches it, the
health hook polls it), `wt start` both, both healthy at the same time,
the resource tables disjoint (api = base + slot in each), the seed modes
verified on disk — the db snapshot has the shared source's content, the
cache is empty, the shared_db is the same path in both worktrees with
`isolated: false` and a place in the descriptor's shared block — the `.env`
seeded from the main checkout's unmanaged content with the managed keys
from the worktree's own allocation — then `wt rm` both (the reaper stops
the servers) and `wt doctor` clean.

## Phase 8 — import

Nothing pre-existed in plain-app; the skip is stated, never silent.

## The spec-less case

`TestAcceptanceSkillEightPhasesOnSpeclessFixture` runs the same eight
phases against a copy of plain-app with the spec AND the adopted surface
removed: the skill writes the spec (drafted before `bands suggest`, since
suggest and reserve read the spec for the required size), commits it with
the artefacts, and the proof is identical.
