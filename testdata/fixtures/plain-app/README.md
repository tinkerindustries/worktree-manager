# plain-app

A repository that does not use compose: it binds ports directly, keeps its
state in `$HOME` paths, and reads its environment from `.env`. It stands in
for the `deepseek-harness`-shaped pilot of the worktree-manager design.
Phase 7 adopts it through the onboarding skill and gives it runnable
content (plan.md §4.1); the fixture is the adopted state, artefacts
included.

## Layout

- `bin/run.sh` — the app's original entry point; reads `API_PORT`,
  `DB_PATH` and `CACHE_PATH` from the environment.
- `bin/server.go` — the serving entry point phase 7 adds: binds the
  worktree's allocated port and serves `/healthz`; `-healthcheck` probes
  the same endpoint (what the health hook runs).
- `bin/setup.sh` — the install hook's payload.
- `.env.example` — the shape of the managed `.env` block (`wt.yaml`'s
  `emit.env`), the source a first `init` seeds from.
- `wt.yaml` — the committed spec; the adoption signal.
- `.claude/` — the generated artefacts (skills, hooks, settings).
- `docs/wt.md` — the generated reference doc.
- `CLAUDE.md` — the generated tripwire.
- `docs/wt-decision-record.md` — the adoption's policy decisions.

## Resources (see wt.yaml)

One stride port (`api`) and three state paths covering all three seed modes
from 03-drivers §4.4: `db` seeded from the shared source, `cache` empty, and
`shared_db` shared-by-default — the resource with `default: shared`, which
every worktree reaches unisolated and which contributes itself to the
descriptor's shared block.

## Running

Two worktrees run side by side: something creates each worktree, `wt init`
attaches to it (build, start and health hooks bring the server up), and
`wt rm` tears it down. The band base the generated artefacts record is
`api=8200`; a machine reserves it with `wt bands reserve --base api=8200`
before the first `wt init`.
