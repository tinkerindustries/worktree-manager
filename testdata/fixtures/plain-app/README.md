# plain-app

A repository that does not use compose: it binds ports directly, keeps its
state in `$HOME` paths, and reads its environment from `.env`. It stands in
for the `deepseek-harness`-shaped pilot of the worktree-manager design.
Phase 7 adopts it through the onboarding skill; phase 0 only needs it to
exist far enough for `wt.yaml` to describe it honestly.

## Layout

- `bin/run.sh` — the app's entry point; reads `API_PORT`, `DB_PATH` and
  `CACHE_PATH` from the environment.
- `bin/setup.sh` — the install hook's payload.
- `.env.example` — the shape of the managed `.env` block (`wt.yaml`'s
  `emit.env`), the source a first `init` seeds from.
- `wt.yaml` — the committed spec; the adoption signal.

## Resources (see wt.yaml)

One stride port (`api`) and three state paths covering all three seed modes
from 03-drivers §4.4: `db` seeded from the shared source, `cache` empty, and
`shared_db` shared-by-default — the resource with `default: shared`, which
every worktree reaches unisolated and which contributes itself to the
descriptor's shared block.

## Running

Not runnable as a worktree pair yet; that is phase 7. The entry point works
standalone against whatever the environment names.
