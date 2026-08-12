# compose-app

A small Go HTTP service, standing in for the `bacio`-shaped pilot of the
worktree-manager design. Phase 5 makes it runnable and proves the side-by-side
gate against it; phase 0 only needs it to exist far enough for `wt.yaml` to
describe it honestly.

## Layout

- `cmd/server` — the service: binds the resolved API port, serves
  `/healthz`, and carries `-healthcheck`, the compose healthcheck's probe.
- `Dockerfile` — builds the server and copies it into a minimal runtime
  image that carries nothing but the binary.
- `compose.yaml` — the dev stack: publishes `${API_PORT}`, keeps the
  database in a named volume. Deliberately does not pin `name:`, so a
  project name passed with `-p` wins.
- `compose.test.yaml` — the dependent test project: instantiates the dev
  stack's project and runs the test suite against it.
- `compose.prod.yaml` — a co-resident production stack standing in for the
  real one on fixed ports 5319/5320. It pins `name:` because a production
  stack must never be renamed by a stray `-p`. `wt.yaml` reserves those
  ports so no worktree can take them.
- `scripts/seed.sh` — the seed hook's payload.
- `wt.yaml` — the committed spec; the adoption signal.

## Resources (see wt.yaml)

Two ports — `api` in stride form, `proxy` in group form sitting at
`api − 1` — two compose namespaces (the dev project and its dependent test
project), and one isolated state path for the database.

## Running

The repo runs as a worktree pair through the tooling: `wt init` (from a
linked worktree) allocates the ports, writes the descriptor and the `.env`
managed block, and runs the hooks; `wt start` brings the stack up. On its
own, `go build ./...` inside this directory compiles the service, and
`docker compose up --build` runs the stack with the committed defaults.
