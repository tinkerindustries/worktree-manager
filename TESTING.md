# TESTING.md

Four layers, from plan.md §4. Each layer lives with the code it tests, and
each runs under plain `go test ./...` unless it says otherwise.

## Layer 1 — pure unit

The rules that need no repository, no socket and no daemon: slug rules,
template evaluation, band arithmetic, `.env` block editing (phase 5).

Lives in `_test.go` files beside the code: `internal/spec/*_test.go` and
`internal/cli/*_test.go`. The client's verb dispatch is tested through
`cli.Run`, which takes args and the two streams and returns the exit code,
so the whole command surface is exercised in-process.

Run one test:

```sh
go test ./internal/spec/ -run TestExplainTablesDisjoint -v
```

## Layer 2 — real-repo fixtures

Classification, root resolution and containment against real repositories,
never mocked git output (plan.md §4): the bug M1 exists to prevent (D4) was a
misreading of what git reports, and a test built on a mock of that same
misreading would pass while the bug survived.

The fixtures are built by running real git commands in `t.TempDir()` — no
nested `.git` is committed to this repository. Two fixture kinds:

- **The committed trees** under `testdata/fixtures/`: `compose-app`,
  `plain-app` and `vm-app`, ordinary file trees each carrying its own
  `wt.yaml`. Later phases copy one into a temp directory and run `git init`
  there. The fixture specs must always validate; `testdata/specs/` holds the
  invalid specs, one per required validation refusal. Tests:
  `TestParseFixtureSpecs`, `TestInvalidSpecsRejected`,
  `TestFixtureWalkUpFromSubtree`, `TestFindSpecPath*` in `internal/spec/`.
- **The synthetic repos built at test time** in `internal/identity/`: a
  builder (`buildFixtures`) runs `git init`, commit, `git worktree add`,
  `git clone` and `os.RemoveAll` to produce the six fixtures of plan.md §4:

  1. a plain repository,
  2. a repository with two linked worktrees,
  3. a clone,
  4. a worktree whose directory has been removed,
  5. each of the above reached through a symlink (the plain repository, the
     main checkout, a linked worktree, the clone and the removed worktree's
     path all have symlink variants).

  `TestClassifyFixtures` classifies every one of them, including through
  symlinks, asserting the outcome and that the acting root is the real path
  git reports. `TestClassifyCarriesTheReach` pins that the main checkout
  path is a field on the classification result, never a function (the D4
  shape). The guard's exit criteria run against the same layer: an adopted
  two-worktree repo with a committed `wt.yaml`, a descriptor in the worktree
  and a shared store (`buildAdoptedRepo`), exercised by
  `TestGuardDeniesWriteToPrimaryCheckout`, `TestGuardDeniesReadCLAUDEOutside`
  and `TestGuardFailsOpen` in `internal/identity/`, and at the binary level
  by `TestRunGuard*` and `TestRunShow*` in `internal/cli/`.

Run one test:

```sh
go test ./internal/identity/ -run TestClassifyFixtures -v
go test ./internal/identity/ -run TestGuardDeniesWriteToPrimaryCheckout -v
```

This layer needs nothing installed but git.

## Layer 3 — in-process coordinator

The coordinator's inputs are protocol messages and a store path, so a test
runs a full request with no socket, no supervisor and no container
(ARCHITECTURE.md §13.3). The harness is a deliverable rather than a test
detail — phases 3 to 6 run allocation, authorisation, entry lifecycle and
migration against it. It lives in `internal/coord`:

- `Harness` (internal/coord/harness.go): opens a store at a temp root,
  builds the handler, and exposes `Connect(kind, token)` (the hello
  exchange, with a synthetic peer — the harness has no kernel) and
  `Request(ctx, session, verb, args)` (one full request round trip).
- The coordinator's phase-2 tests in `internal/coord/coord_test.go`:
  `TestHarnessFullRequest` (exit criterion 4), the version-refusal
  direction tests, identity assignment (host from peer credentials, named
  token, ephemeral session id), clients.json observation, and
  `TestServerGracefulShutdown`, which runs the real socket server over a
  temp socket to pin the lifecycle (cancellation, in-flight requests,
  socket-file cleanup).
- The store's own tests in `internal/store/store_test.go`: root
  resolution, 0700/0600, atomic writes, the schema_version refusal, the
  unwritable-root error naming the path.
- The protocol's tests in `internal/protocol/protocol_test.go`: framing
  (one JSON object per newline-terminated message) and `Agree` naming the
  upgrade in both directions.
- `daemon status`'s state machine is driven in
  `internal/cli/daemon_test.go`: all four states with the platform
  observations injected, plus end-to-end states with the real
  observations — a planted registration under `--prefix` and a fake hello
  server on a temp socket (a stand-in for the coordinator that never
  imports coordinator code into the client's tests).

Run one test:

```sh
go test ./internal/coord/ -run TestHarnessFullRequest -v
go test ./internal/cli/ -run TestDaemonStatusThreeStates -v
```

This layer needs nothing installed.

## Layer 4 — live acceptance

The two gates in plan.md §6, run against a real docker daemon and a
coordinator started in the foreground: `go test -tags acceptance ./...`.
Tests carry `//go:build acceptance` so plain `go test ./...` stays green on
a machine with neither docker nor `gh`. CI runs this layer from phase 6.

## Which layers need what

| Layer | Needs |
|---|---|
| Pure unit | nothing |
| Real-repo fixtures | git, for the real repositories built in `t.TempDir()` |
| In-process coordinator | nothing |
| Live acceptance | docker and a coordinator process (phase 5), `gh` for the gates that check PRs (phase 5/6) |

## Command surface

```sh
go test ./...                                   # layers 1 and 2, no docker, no gh
go test -tags acceptance ./...                  # layer 4 (from phase 6 in CI)
go test ./internal/spec/ -run <TestName> -v     # one test
```

Tests are table-driven, use no framework, and only `t.TempDir()` for
temporary repositories.
