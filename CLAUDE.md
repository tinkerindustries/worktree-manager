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
go test -tags acceptance ./...   # the live gates, from phase 6 in CI; needs docker
```

The module is `github.com/mrgeoffrich/worktree-manager` and requires
Go 1.26. The one third-party dependency is the YAML package the spec needs;
everything else is the standard library. Cross-compilation is
`CGO_ENABLED=0 GOOS=<os> GOARCH=<arch> go build ./...` for macOS, Linux and
Windows, exercised by the workflow in `.github/workflows/ci.yml`.

## The two binaries

`cmd/wt` is the client: it runs once per operation, prints results to
stdout and diagnostics to stderr, and exits 0–5 (see `internal/cli`).
`cmd/wtd` is the coordinator, a resident per-user process owning the store
and every privileged operation. Both link `internal/spec`. The
coordinator-only packages (`internal/store`, `internal/coord`,
`internal/driver`, `internal/fleet`) are never imported by `cmd/wt`.

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
  type, the one dial-and-request helper (exit 5 lives there), and the
  verbs: `spec validate`, `spec explain`, `guard`, `show`,
  `daemon status`, `daemon install`, `bands list`, `bands reserve`,
  `init`, `start`, `rm`, `list`, `doctor`, `reconcile` and `clients`.
- `internal/identity` — M1: classification, root resolution, containment,
  slug validation, descriptor location, the guard engine.
- `internal/platform` — M8: symlink-resolved path realisation, the mount's
  case-sensitivity probe, the socket path, peer credentials, the private
  store-dir permission model, the atomic-write helper, and the launchd
  supervisor seam. The only package permitted to branch on `GOOS`.
- `internal/descriptor` — the per-worktree allocation record: type,
  reader, atomic writer, the shared-block and isolation-state builders,
  and the `info/exclude` ignore rule.
- `internal/envfile` — the `.env` managed block: replace-only-the-block
  re-runs, the duplicate strip, the first-write seed from the main
  checkout, the unbalanced-marker refusal.
- `internal/generate` — the generated Go descriptor reader, stdlib-only
  and gofmt-clean by construction.
- `internal/protocol` — the wire between the two binaries: message types,
  newline-delimited JSON framing, version negotiation.
- `internal/store` — the coordinator's state directory: `WT_HOME`/`$HOME/.wt`
  resolution, atomic writes, the `schema_version` envelope, `clients.json`,
  `registry.json`, `bands.json` (with per-base spans) and `specs.json`
  (the per-app spec cache reclamation falls back to).
- `internal/coord` — the coordinator's request core, socket server and the
  in-process harness; one writer serialises here. Since phase 6: the fleet
  verbs (`list` with the stale/unverifiable/reclaimable/foreign markers
  and owner-only secret redaction, `doctor` reading everything and
  writing nothing, `reconcile` reusing init's and rm's repair paths on
  entries, `clients list`), reclamation of aged-out ephemeral clients on a
  24-hour interval measured against real last-seen times, and the reaper
  allowlist read from the spec's `reaper.binaries`.
- `cmd/wt`, `cmd/wtd` — the two entry points.
- `testdata/fixtures/` — the three fixture repositories; each has its own
  `wt.yaml`, which is what the walk-up resolution rule is tested against.
  The classification fixtures (plain repo, two linked worktrees, clone,
  removed worktree, symlink variants) are built with real git in
  `t.TempDir()` inside `internal/identity` tests — never mocked git output.
- `testdata/specs/` — invalid specs, one per required validation refusal.
- `ARCHITECTURE.md` (root) — the as-built codemap; read it before touching
  package boundaries. `TESTING.md` — how the test layers work.

## Environment

The five variables are `WT_SOCKET`, `WT_HOME` (read by `wtd` alone),
`WT_STANDALONE`, `WT_CLIENT_EPHEMERAL` (=1) and `WT_CLIENT_TOKEN` (phase 6:
the named-container token; setting it together with the ephemeral
declaration is refused as ambiguous).

## Reading order

`PLAN-SCOPE.md` (frozen scope) → `plan.md` → `docs/ARCHITECTURE.md` →
`docs/design/03-drivers.md` (authoritative for the spec schema) → the
other module documents as needed. `docs/` is read-only for every phase.
