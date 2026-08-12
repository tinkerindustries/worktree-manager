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
never mocked git output: `testdata/fixtures/compose-app`,
`testdata/fixtures/plain-app` and `testdata/fixtures/vm-app`. Each is an
ordinary file tree — no `.git` — because later phases copy a fixture into a
temp directory and run `git init` there.

Each fixture carries its own `wt.yaml` at its root; the fixture specs must
always validate, and `testdata/specs/` holds the invalid specs, one per
required validation refusal. The fixture and walk-up tests are
`TestParseFixtureSpecs`, `TestInvalidSpecsRejected`,
`TestFixtureWalkUpFromSubtree` and `TestFindSpecPath*` in
`internal/spec/`. This layer needs nothing installed.

Run one test:

```sh
go test ./internal/spec/ -run TestFixtureHooksEmitCoverage -v
```

## Layer 3 — in-process coordinator

Allocation, authorisation, entry lifecycle and migration against a store
path and protocol messages, with no socket and no supervisor. This layer
starts in phase 2, when the coordinator skeleton lands.

## Layer 4 — live acceptance

The two gates in plan.md §6, run against a real docker daemon and a
coordinator started in the foreground: `go test -tags acceptance ./...`.
Tests carry `//go:build acceptance` so plain `go test ./...` stays green on
a machine with neither docker nor `gh`. CI runs this layer from phase 6.

## Which layers need what

| Layer | Needs |
|---|---|
| Pure unit | nothing |
| Real-repo fixtures | nothing |
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
