# CLAUDE.md

Worktree Manager allocates per-worktree resources — ports, compose project
names, subnets, state paths, VMs — so that two worktrees of one repository
run side by side without colliding. A repo opts in by committing a `wt.yaml`
at its root; that committed spec is the adoption signal and the contract
between the onboarding skill and the binaries.

The repository holds the implementation plan (`plan.md`), the frozen scope
(`PLAN-SCOPE.md`), the target architecture (`docs/ARCHITECTURE.md`, the
design of record) and the nine module designs under `docs/design/`. Those
documents are inputs and are never edited.

## Build and test

```sh
go build ./...
go test ./...
go vet ./...
gofmt -l cmd internal        # must print nothing
```

The module is `github.com/mrgeoffrich/worktree-manager` and requires
Go 1.26. The one third-party dependency is the YAML package the spec needs;
everything else is the standard library. Cross-compilation is
`CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build ./...` for macOS, Linux and
Windows, exercised by the workflow in `.github/workflows/ci.yml`.

## The two binaries

`cmd/wt` is the client: it runs once per operation, prints results to
stdout and diagnostics to stderr, and exits 0–5 (see `internal/cli`).
`cmd/wtd` is the coordinator, a resident per-user process; in this phase it
is a skeleton that prints its version line. Both link `internal/spec`.
The coordinator-only packages (`internal/store`, `internal/coord`,
`internal/driver`, `internal/fleet`) arrive in later phases and `cmd/wt`
never imports them.

## The no-inference rule

Neither binary infers anything about a repository. Resource allocation,
descriptor emission and hook sequencing are the binaries' work; judgment —
choosing bands, making policy calls, naming slugs — belongs to the
onboarding skill (`docs/design/09-onboarding.md`). Policy is spec
configuration; neither binary branches on repo identity.

## Where things live

- `internal/spec` — the `wt.yaml` schema: parser, validator, template
  evaluator, the walk-up `wt.yaml` finder, and the quoted YAML emitter.
- `internal/cli` — verb dispatch, flag parsing, output, the exit-code error
  type.
- `internal/identity` — M1: classification, root resolution, containment,
  slug validation, descriptor location, the guard engine.
- `internal/platform` — M8b: symlink-resolved path realisation and the
  mount's case-sensitivity probe. The only package permitted to branch on
  `GOOS`.
- `internal/descriptor` — the per-worktree allocation record and its
  reader (yaml and json); phase 5 writes the same type.
- `cmd/wt`, `cmd/wtd` — the two entry points.
- `testdata/fixtures/` — the three fixture repositories; each has its own
  `wt.yaml`, which is what the walk-up resolution rule is tested against.
  The classification fixtures (plain repo, two linked worktrees, clone,
  removed worktree, symlink variants) are built with real git in
  `t.TempDir()` inside `internal/identity` tests — never mocked git output.
- `testdata/specs/` — invalid specs, one per required validation refusal.
- `ARCHITECTURE.md` (root) — the as-built codemap; read it before touching
  package boundaries. `TESTING.md` — how the test layers work.

## Reading order

`PLAN-SCOPE.md` (frozen scope) → `plan.md` → `docs/ARCHITECTURE.md` →
`docs/design/03-drivers.md` (authoritative for the spec schema) → the
other module documents as needed. `docs/` is read-only for every phase.
