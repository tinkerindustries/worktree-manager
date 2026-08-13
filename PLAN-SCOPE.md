# Plan scope — worktree-manager, HTTP + SQLite rearchitecture

Frozen before the run starts. Not edited by the orchestrator or by any phase
agent. It is the authority on what may and may not be built, and it outranks
`plan.md` wherever the two disagree.

This supersedes the phases 0–9 scope, which is in git history at `318cbd7`.

## What this delivers

`wtd` is a resident per-user HTTP server listening on `127.0.0.1:7833` by
default, serving fifteen JSON endpoints under `/v1`, and `wt` is an HTTP
client that reaches it. All coordinator state — the registry, the band ledger,
the client table and the spec cache — lives in a single SQLite database at
`<WT_HOME|$HOME/.wt>/wt.db`, mode 0600 along with its WAL and SHM sidecars.
The API is described by a committed OpenAPI 3.1 document generated from the
route table, and `wt` reaches the server only through a client generated from
that same table.

On the absent and malformed paths: a missing database is created and
initialised; a database whose `meta.schema_version` is newer than the build is
refused, naming the upgrade; a database that cannot be opened is refused with
the same reasoning today's unparseable registry carries, because the registry
remains the only source of repository locations and rebuild-from-descriptors
cannot be safe. A missing `endpoint.json` leaves the client on its compiled
default and then exit 5; a malformed one is an error naming the field, never a
silent fallback. A port already in use is refused, naming the port and
`--addr`. Absent `--container-token`, `wtd` admits host clients only. Existing
`registry.json`, `bands.json`, `clients.json` and `specs.json` are never read
— `wtd` logs one startup warning naming `registry.json` if it is present, and
starts anyway.

Two worktrees of one repository still run side by side with disjoint
resources, `wt rm` still frees everything it allocated, and `wt show` and
`wt guard` still work with no coordinator running at all. Exit codes 0–5 keep
their present meanings, with 5 remaining a client-side transport failure.

## Non-goals

Absolute. A phase that appears to require one of these stops rather than
overriding it.

- **No migration of existing state.** Not an importer, not a one-shot, not a
  `--migrate` flag, not "it's only fifty lines". The four JSON files are never
  read. A startup warning is the entire concession.
- **No migration framework.** No versioned migration files, no golang-migrate,
  no up/down scripts. `meta.schema_version` plus refuse-if-newer is the whole
  mechanism.
- **No compatibility shim for the old wire.** The unix socket, the Windows
  named pipe, the NDJSON framing, the hello exchange and peer credentials are
  deleted, not deprecated. `wtd` does not serve both.
- **No client-side retries.** A retried `allocate` double-allocates. Every
  transport failure is exit 5, once.
- **No Prometheus or `/metrics` endpoint.**
- **No streaming progress.** No SSE, no chunked responses, no websockets, even
  though `materialise` can take minutes on a VM start. The client blocks on
  one request, as it does today.
- **No TLS on the listener.** Plain HTTP on loopback.
- **No bearer token rotation or expiry.** The host token lives as long as the
  endpoint file; ephemeral session ids are revoked by reclamation deleting the
  row, and by nothing else.
- **No `wt db` verb family.** No dump, vacuum, repair or backup subcommands.
- **No automatic database backups or snapshots.**
- **No allocation history or audit table.** Current state only.
- **No `wt daemon stop`, `restart` or `logs`.** The daemon verb family stays
  at `status` and `install`.
- **No access logging, request IDs or log rotation.** Logging stays as it is.
- **No system-wide multi-user daemon.** One `wtd` per user. A shared daemon
  would need real authorisation between users rather than admission, and that
  is a different piece of work.
- **No auto-start of `wtd` from `wt`.** A stopped coordinator is exit 5 naming
  the start command, exactly as today.
- **No new packaging channels.** No Homebrew formula, no winget manifest, no
  apt repository, no container image for `wtd` itself.
- **No package renames beyond `internal/protocol` → `internal/api`.**
- **No broad test-coverage expansion.** Add the tests each phase's exit
  criteria name. Coverage looking low in a package you are passing through is
  not a reason to write tests for it.
- **No refactoring of code a phase only passes through.** This applies hardest
  at R4, where three phases of other agents' code have accumulated and cleanup
  looks free. It is not in scope. If something is genuinely wrong, note it and
  leave it.
- **No third-party HTTP, routing or codegen libraries.** `net/http` and
  `http.ServeMux` only. The OpenAPI document and the client are produced by a
  small in-repo generator, not by oapi-codegen, swag, or openapi-generator.
- **No rewriting of `docs/design/*.md`.** Superseded documents get a header
  saying so and are otherwise left as the historical record.

## Fences

Paths and behaviours that must not change, each with its reason.

### Paths

- `internal/driver/**` — the six-operation contract, the five drivers and the
  docker seam are unrelated to transport and storage. The drift risk is a
  phase "fixing" a driver test that broke for a store reason.
- `internal/spec/**` — `wt.yaml` is the committed contract with every adopted
  repository. A schema change breaks repos already carrying a spec.
- `internal/identity/**`, `internal/treecheck/**` — classification, guard and
  the pre-destroy checks are client-local and never touch the coordinator.
- `internal/managed/**`, `internal/artefact/**`, `internal/descriptor/**`,
  `internal/envfile/**`, `internal/generate/**` — the managed-block convention
  and everything written into a repository. R4 regenerates fixture artefacts;
  it does not change how they are produced.
- `docs/design/03-drivers.md` — remains authoritative for the spec schema,
  which this work does not touch.
- `testdata/specs/**` — one invalid spec per validation refusal. A change here
  means the schema moved, which it must not.
- `testdata/fixtures/*/wt.yaml` — the three fixture specs. R4 may regenerate
  generated artefacts inside the fixtures; the specs themselves are fixed.
- `.claude/skills/onboarding/**` — R4 updates the primitives card to match the
  verb list. Nothing else in the skill changes, and no phase before R4 touches
  it.

### Behaviours

- **Exit codes 0–5 keep their meanings.** 5 stays client-side and stays a
  transport failure. 3 and 4 continue to originate in the coordinator and
  reach the process exit status unchanged.
- **Every verb keeps `--json`,** and `reconcile --dry-run` stays required.
- **The `Handler` mutex and `internal/coord/claim.go` stay.** The mutex guards
  read-modify-write sequences spanning store calls and driver decisions, which
  no single transaction covers; claims are what keep a VM start from queueing
  every other client. SQLite transactions go underneath both, not instead.
- **The store database is never client-readable.** `cmd/wt` must not import
  `internal/store`, `internal/coord`, `internal/driver` or `internal/fleet`.
  Only `endpoint.json` is client-readable, and a container never mounts the
  store.
- **`wt show` and `wt guard` work with no coordinator.**
- **Secrets stay owner-only.** Served to the owning client alone and only
  under `--wide`; redacted in `list`, `doctor`, `clients list` and every error
  path. A foreign named client's key renders as a short hash, because that key
  is its token.
- **`bands.reserve` stays host-client-only.**
- **`path_visible` semantics are unchanged.** A container path is recorded,
  never stat-checked, and never counted as stale.
- **The route table is frozen at the end of R1.** R2 generates the OpenAPI
  document and the client from it; R3 and R4 must not add, rename or remove a
  route.
- **`CGO_ENABLED=0` cross-compilation to macOS, Linux and Windows keeps
  working.** CI gates on it, and it is why the SQLite driver is pure Go.
- **The runtime dependency list stays at two** — the YAML package and
  `modernc.org/sqlite`. sqlc is a build-time `tool` directive whose output
  uses `database/sql`; it is not a runtime dependency and nothing else is
  added.

## In scope

- `internal/store/**` — rewritten over SQLite, including `schema.sql`,
  `query.sql` and the committed sqlc output.
- `internal/protocol/**` → `internal/api/**` — the rename, the route table,
  the retained `*Args`/`*Result` types, the generator and its committed
  output under `internal/api/client/**`.
- `internal/coord/server.go`, and the ~12 mutation call sites across
  `internal/coord/*.go`.
- `internal/cli/coord.go`, `internal/cli/daemon.go`.
- `internal/platform/` — deleting the socket, pipe and peer-credential files;
  reworking `daemon.go` around endpoint URLs.
- `cmd/wtd/main.go`, `cmd/wt/main.go`.
- `api/openapi.yaml` — new, generated, committed.
- `dist/install.sh`, `dist/install.ps1`, `dist/build.sh`.
- `.github/workflows/ci.yml` — the sqlc and OpenAPI drift checks.
- `go.mod`, `go.sum`.
- `docs/ARCHITECTURE.md`, root `ARCHITECTURE.md`, `CLAUDE.md`, `TESTING.md`,
  `RELEASE.md`, and superseded-headers on `docs/design/02-coordination.md` and
  `docs/design/08-platform.md`.
- Tests alongside each of the above.

## Definition of done

Run on the integration branch before the final pull request. Each of these
passes on `main` today, so a failure is this work's own.

```sh
go build ./...
go test -race ./...
go vet ./...
gofmt -l cmd internal        # must print nothing
staticcheck ./...            # must print nothing
go test -tags acceptance ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build ./...
```

And these behavioural checks, which no unit test covers:

- Starting `wtd` against an empty `WT_HOME` creates `wt.db` and
  `endpoint.json`, both 0600, and the WAL and SHM sidecars are 0600 too.
- `wt list` with `wtd` stopped exits 5 and names the start command.
  `wt show` and `wt guard` still succeed.
- `curl -s -o /dev/null -w '%{http_code}' -X POST localhost:7833/v1/ping`
  with no bearer is refused; the same request carrying the token from
  `endpoint.json` and `X-Wt-Client: 1` succeeds.
- A second `wtd` on the same port refuses to start, naming the port and
  `--addr`.
- Two worktrees of `testdata/fixtures/plain-app` start side by side with
  disjoint resources, and neither can reach the other's.
- `wt rm` on one of them frees every resource, and a re-run of the gate reuses
  the freed slot.
- Regenerating the OpenAPI document and the client on a clean tree produces no
  diff.
- `grep -rn "WT_SOCKET\|peer credential\|named pipe" internal/ cmd/ docs/` at
  R4 returns only historical references that say they are historical.
</content>
