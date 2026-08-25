# CLAUDE.md

Worktree Manager allocates per-worktree resources — ports, compose project
names, subnets, state paths, VMs — so that two worktrees of one repository
run side by side without colliding. A repo opts in by committing a `wt.yaml`
at its root; that committed spec is the adoption signal and the contract
between the onboarding skill and the binaries.

The repository holds the implementation plan (`plan.md`), the frozen scope
(`PLAN-SCOPE.md`), the target architecture (`docs/ARCHITECTURE.md`, the
design of record) and the nine module designs under `docs/design/`.

`plan.md` and `PLAN-SCOPE.md` are frozen for the duration of the work they
describe and are never edited by a phase. `docs/design/*.md` describe the
system as built in phases 0–9; the transport and store halves were
superseded by the HTTP + SQLite rearchitecture, and every superseded
document says so in its first paragraph. `docs/design/03-drivers.md`
remains authoritative for the spec schema, which the rearchitecture did not
touch.

## Build and test

```sh
go build ./...
go test -race ./...          # CI gates on -race
go vet ./...
gofmt -l cmd internal        # must print nothing
staticcheck ./...            # CI gates on this too; must print nothing
go test -tags acceptance ./...   # the live gates, from phase 6 in CI; needs docker
go run ./cmd/wtgen               # regenerate the OpenAPI doc and the client; CI fails on drift
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate   # regenerate the store queries; CI fails on drift
```

The module is `github.com/mrgeoffrich/worktree-manager` and requires
Go 1.26. There are two runtime dependencies — the YAML package the spec
needs and `modernc.org/sqlite`, which is pure Go precisely so that
`CGO_ENABLED=0` cross-compilation keeps working. Everything else is the
standard library. sqlc is a build-time tool invoked by version and is
deliberately absent from `go.mod`, so it contributes nothing to the
module graph. Cross-compilation is
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
  It owns the accessors every other package used to copy:
  `ResourceByName`, `NamespaceKind`, `HookByName`, `HookNames`,
  `WorktreePath` and the IPv4 block arithmetic, and the `removal:` block —
  rm's per-check safety policy and its defaults, through `RemovalPolicy`.
- `internal/treecheck` — the checks that run before a worktree is
  destroyed: uncommitted changes, unpushed commits (an absent upstream is
  its own answer), what gh reports about the branch's pull request, and
  the never-forced `git worktree remove`. `wt rm`, `wt cleanup` and the
  coordinator's sweep each keep their own policy over this one mechanism.
  rm's policy is the repository's, from the spec's `removal:` block: each
  check either refuses (exit 3, or 4 when it could not run) or warns and
  lets rm continue, defaulting to refuse on uncommitted changes and warn
  on the other two, so a personal project with local-only branches and no
  gh is removable out of the box. `wt rm --strict` and `wt rm --force`
  override the block for one run, in the two directions, and neither
  reaches `git worktree remove`, which is still never forced. cleanup and
  the sweep ignore the block and require a merged PR. git and gh reach it
  through a runner function.
- `internal/driver` — the six-operation contract, the port, namespace,
  state-path, cidr and machine drivers, the docker CLI seam, and the
  sequencing: apply in dependency order with machine forced first among
  its dependents (a VM must exist before anything expects its daemon),
  teardown in reverse continuing past failures. The machine driver's
  rails — the capacity guard (refuse a new instance past
  `max_concurrent`, naming what is running and how to tear one down; the
  count is the daemon's own instances; re-running a running profile stays
  allowed), background warm-up, `--keep-vm`, and the documented bypass —
  are proved against a fake `platform.MachineRunner`; the live Colima
  path is reported not_run on this machine.
- `internal/cli` — verb dispatch, flag parsing, output, the exit-code error
  type, the one dial-and-request helper (exit 5 lives there), and the
  verbs: `spec validate`, `spec explain`, `guard`, `show`,
  `daemon status`, `daemon install`, `daemon uninstall`, `bands list`,
  `bands suggest`,
  `bands reserve`, `ports scan`, `init`, `start`, `rm`, `list`, `doctor`,
  `reconcile`, `clients` and `cleanup` (gated on gh: missing or
  unauthenticated gh cleans nothing and exits 4; the full rm safety
  checks apply even when the PR is merged; unverifiable entries are never
  touched). `daemon status` reports the systemd lingering caveat on Linux,
  and `daemon install` registers with launchd, systemd (with its paired
  socket unit, which now carries a TCP `ListenStream`) or the Task
  Scheduler per platform. `daemon uninstall` is the reverse — stop,
  deregister, remove the registration file. Deregistration is client-local
  and needs no route, but the entry count comes from the coordinator's
  `list`, because the store database is never client-readable. It never
  removes the store, and it refuses with exit 3 while the registry still
  holds entries, naming `wt list` and `wt rm`. With the coordinator
  unreachable the count is unknowable, so that is a refusal too; `--force`
  is the only way past either. `daemon install --addr <host:port>` and
  `--container-token <token>` are independent settings — a custom address
  needs no token and a token needs no custom address. With no `--addr` the
  install probes for a free port, pins it into the registration and says
  which, so a second user on one machine needs no manual step. A
  registration carrying the token is 0600 on unix and the token is never
  echoed.
- `internal/identity` — M1: classification, root resolution, containment,
  slug validation, descriptor location, the guard engine, and the
  per-session classification cache (`WT_GUARD_CACHE`) the generated guard
  hook sets — keyed on cwd, validated by the stat identity of the root's
  `.git` entry (the root dir's own mtime is unusable: the case-sensitivity
  probe writes a file there), dropped and reclassified with a one-time
  note when the worktree is removed mid-session.
- `internal/managed` — the managed block convention, declared once for
  every file the tool writes (`internal/envfile` imports it): the markers, `# wt-field:` records, replace-only-the-block
  regeneration, the unbalanced-marker refusal, append-to-a-markerless-file
  (the CLAUDE.md tripwire joining a repo's own text).
- `internal/artefact` — the phase-7 generated artefacts, rendered per
  repo: the `worktree-create`/`worktree-remove` skills, the SessionStart
  tripwire, the opt-in PreToolUse guard hook, the settings entries, the
  reference doc, the CLAUDE.md tripwire, and the briefing renderer (which
  refuses with the descriptor missing). Bodies are stable; the managed
  block records the spec fields each file came from. Not a verb — the
  onboarding skill and the tests drive it; `cmd/wt` never imports it.
- `internal/platform` — M8: symlink-resolved path realisation (on Windows
  via `GetFinalPathNameByHandleW`, which also canonicalises long paths and
  mapped drives), `SamePath` (the one do-these-name-the-same-directory
  predicate), the mount's case-sensitivity probe (answered once per
  directory per process), the hook shell (`sh -c` on every platform,
  resolved from Git for Windows on Windows and refused by name where no
  POSIX shell exists, because hook commands are shell commands), the
  listen-address and container-token rails (`ValidateListenAddr`,
  `ValidateContainerToken` and the `ValidateCoordinatorConfig` that both
  `wtd` and `wt daemon install` run, so the two cannot disagree about what
  a valid configuration is), the allowed-Host rail (`ValidateAllowedHost` —
  a host, never a URL and never an address with a port), the free-port
  probe that pins an address into
  a registration (`ChooseRegistrationAddr`, over the same `ProbeBind` the
  port driver uses),
  the private store-dir permission model (0700 on unix; the current-user
  ACL, with the refusal to hold credentials where it cannot be set, on
  Windows), the file-mode helpers the store and the endpoint file share
  (`WithPrivateUmask`, `VerifyPrivateFileModes`; unix-only), the
  atomic-write helper (the directory-fsync step is unix-only
  and stated), the supervisor seam (launchd on macOS, the systemd user
  unit with its paired .socket unit and LISTEN_FDS socket activation on
  Linux, the logon scheduled task on Windows), and the machine runner seam
  (Colima on macOS, WSL2 on Windows, nothing elsewhere — the
  `MachineRunner` interface the machine driver shells out through and its
  fake-runner tests fake). The only package permitted to branch on `GOOS`.
- `internal/descriptor` — the per-worktree allocation record: type,
  reader, atomic writer, the shared-block and isolation-state builders,
  and the `info/exclude` ignore rule.
- `internal/envfile` — the `.env` delivery channel: the duplicate strip,
  the first-write seed from the main checkout, and the dotenv-specific
  half of the block. The markers and the block primitives are
  `internal/managed`'s, so one convention covers every file the tool
  writes.
- `internal/generate` — the generated Go descriptor reader, stdlib-only
  and gofmt-clean by construction.
- `internal/api` — the HTTP surface both binaries share: the `*Args` and
  `*Result` types, the single `Routes` table mapping verb to method and
  path (frozen — client and server both derive from it, so a route added
  in one place is added in both), and the `endpoint.json` reader/writer
  with `DefaultAddr`. Every RPC is `POST /v1/<verb>`; the exceptions are
  `GET /version`, the unversioned range report that replaced hello
  negotiation, and `GET /v1/ping`.
- `internal/api/client` — the generated client. One method per route, the
  transport pinned to `Proxy: nil` so the bearer token can never reach an
  `HTTP_PROXY`, and no retries ever. `internal/cli` makes no HTTP call
  except through it.
- `internal/apigen`, `cmd/wtgen` — the generator. It reads `api.Routes` and
  the `*Args`/`*Result` types and emits `api/openapi.yaml` (OpenAPI 3.1)
  and the client. Stdlib-only, no third-party codegen, gofmt-clean by
  construction, output committed, and CI fails on drift. Adding a route
  without regenerating fails a test.
- `internal/store` — the coordinator's state, one SQLite database at
  `<WT_HOME|$HOME/.wt>/wt.db` (WAL, `busy_timeout=5000`,
  `foreign_keys=on`, `synchronous=FULL`; the database and its `-wal`/`-shm`
  sidecars are all 0600, because entry secrets live in them). Whole-
  collection reads (`ReadRegistry`, `ReadBands`, `ReadClients`,
  `ReadSpecs`) and row-level mutations (`UpsertEntry`, `DeleteEntry`,
  `UpdateEntryState`, `TouchEntry`, `UpsertBand`, `AddReservation`,
  `ReplaceReservationByNote`, `UpsertClient`, `DeleteClient`, `UpsertSpec`)
  plus `WithTx`. `UNIQUE (app, slot)` makes distinct-slot allocation a
  database constraint rather than something the mutex must guarantee.
  Queries are sqlc-generated from `schema.sql` and `query.sql`; sqlc is a
  build-time tool run as
  `go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate`, deliberately
  **not** a `go.mod` tool directive, so the runtime dependency list stays
  at two. `meta.schema_version` versions the database and a newer one is
  refused.
- `internal/coord` — the coordinator's request core, HTTP server and the
  in-process harness; one writer serialises here. The mutex covers the
  store, not the drivers: materialise, teardown, the reaper and the sweep
  release it and hold a per-`(app, slug)` claim instead (`claim.go`), so
  a VM start does not queue every other client behind it. Since phase 6: the fleet
  verbs (`list` with the stale/unverifiable/reclaimable/foreign markers
  and owner-only secret redaction, `doctor` reading everything and
  writing nothing, `reconcile` reusing init's and rm's repair paths on
  entries, `clients list`), reclamation of aged-out ephemeral clients on a
  24-hour interval measured against real last-seen times, and the reaper
  allowlist read from the spec's `reaper.binaries`. Phase 7 adds the two
  onboarding primitives (`ports scan` — every LISTEN socket, a fact;
  `bands suggest` — the lowest bases whose required ranges fit the
  ledger) and the generated-artefact drift check: `doctor` scans each
  repo's tracked files for the managed marker, compares the recorded
  `# wt-field:` records against the current spec and band ledger, and
  reports the generated file and the field that moved. Phase 8 adds the
  machine-capacity doctor finding (an app approaching `max_concurrent`,
  naming what is running and how to tear one down) and the scheduled
  cleanup sweep: the coordinator's own timer (hourly, on the one-minute
  sweeper), deliberately more conservative than `wt cleanup` — only the
  coordinator's own host user's entries, never adopting a view, every
  skip logged. `doctor`'s unparseable-registry row keeps its refusal and
  states plainly why rebuild-from-descriptors cannot be safe: the
  registry is the only source of repository locations. Phase 9: the R1
  restart recovery (`RecoverInterrupted` — every `reserving` entry a
  starting coordinator finds is torn down by handle and dropped, or moved
  to `tearing-down` with a note, before the first connection), the
  loopback TCP identity rule (a TCP connection must present the
  configured token — constant-time compare, one refusal for missing and
  wrong — and named clients are the only kind TCP accepts), the
  DNS-rebinding Host guard and the `--allow-host` values that widen it by
  name alone (`Server.AllowedHosts`; every Host not named is still
  refused, and the refusal names the flag that would admit it), and the
  security-pass redaction (a named client's key is its token, so foreign
  keys in `list`, `clients list` and every error are a short hash;
  `bands.reserve` is host-client-only; `release` refuses a
  `tearing-down` entry).
- `cmd/wt`, `cmd/wtd`, `cmd/wtgen` — the two entry points and the
  generator. `wtd` takes `--addr` (default `127.0.0.1:7833`),
  `--allow-remote` (required for a non-loopback bind), `--container-token`
  (16+ characters; absent, only host clients are admitted),
  `--allow-host` (repeatable; a `Host` header value accepted beyond
  loopback and the coordinator's own address, which is how a container
  reaching the host by name gets past the DNS-rebinding guard) and
  `--activate` (consume the systemd-passed listener). `cmd/wtgen` emits
  `api/openapi.yaml` and `internal/api/client`.
- `dist/` — the phase-9 distribution: `build.sh` (one archive per
  platform — darwin arm64/amd64, linux amd64/arm64, windows amd64 — with
  both binaries plus the installer, run by the release workflow on every
  `v*` tag; the version is semver or the build refuses, derived from the
  nearest `v*` tag when none is given; it writes a `SHA256SUMS` manifest outside
  the archives and a second one inside each archive covering the two
  binaries, and wires the archive version and commit into both binaries
  with `-ldflags -X` so `wt --version`/`wtd --version` report them),
  `install.sh`/`install.ps1` (verify the binaries against the archive's
  `SHA256SUMS` before copying — a missing manifest or a mismatched
  digest refuses, naming `--skip-verify`/`-SkipVerify` as the deliberate
  override — replace the binaries by copy-to-temp-then-rename, print what
  is being replaced and with what, support `--dry-run`/`-DryRun` (prints
  every action and changes nothing), `--client-only`/`-ClientOnly` (the
  container install: the client alone, no coordinator and no registration,
  refusing the flags that configure one) and `--uninstall`/`-Uninstall`
  (drive `wt daemon uninstall`, then remove the binaries; the store is
  never removed), then drive `wt daemon install`; an explicit `--prefix`
  is self-contained and loads nothing), `ziphelper.go` (the stdlib-only
  Windows zip builder).
- `.claude/skills/onboarding/` — the onboarding skill (a document): the
  eight phases, the primitives card, the plain-app walkthrough. It is the
  judgment half of the system; the binaries expose facts and refuse.
- `testdata/fixtures/` — the three fixture repositories; each has its own
  `wt.yaml`, which is what the walk-up resolution rule is tested against.
  `plain-app` is the adopted showcase — phase 7 gave it runnable content
  (`bin/server.go`, the build/start/health hooks) and the generated
  artefacts are committed in it, so a clone is already adopted.
  The classification fixtures (plain repo, two linked worktrees, clone,
  removed worktree, symlink variants) are built with real git in
  `t.TempDir()` inside `internal/identity` tests — never mocked git output.
- `testdata/specs/` — invalid specs, one per required validation refusal.
- `ARCHITECTURE.md` (root) — the as-built codemap; read it before touching
  package boundaries. `TESTING.md` — how the test layers work.
  `RELEASE.md` — how a version is cut and installed, on a host and in a
  container. `HOSTING.md` — why nothing here is deployed.

## Environment

The six variables are `WT_ENDPOINT` (the coordinator's base URL, e.g.
`http://127.0.0.1:7833`), `WT_HOME`, `WT_STANDALONE`,
`WT_CLIENT_EPHEMERAL` (=1), `WT_CLIENT_TOKEN` (the container token) and
`WT_GUARD_CACHE`, the per-session classification cache directory the
generated guard hook sets.

`WT_HOME` is read by **both** binaries, which is the one change the HTTP
rearchitecture made to this contract. The client reads only
`endpoint.json` from it — the base URL and the host token, 0600 — while
the database stays coordinator-private. A container is still never given
the store: it gets `WT_ENDPOINT` and `WT_CLIENT_TOKEN` explicitly.

A client resolves its endpoint as `WT_ENDPOINT`, then `endpoint.json`,
then the compiled default. A missing endpoint file leaves the client on
the default and then exit 5; a malformed one is an error naming the field,
never a silent fallback.

`WT_CLIENT_TOKEN` and `WT_CLIENT_EPHEMERAL=1` now compose rather than
conflicting: the token is the admission credential and the ephemeral flag
a lifecycle declaration. An ephemeral client presents the container token
to `POST /v1/session` once and uses the issued session id as its bearer
thereafter; that session id is a row in `clients`, so it survives a
coordinator restart mid-`init`, and reclamation deleting the row is what
revokes it.

`WT_CONTAINER_TOKEN` is read by `install.sh`/`install.ps1` alone, never by
a binary, as the operator's way to script `--container-token` without the
token in shell history.

## Reading order

`PLAN-SCOPE.md` (frozen scope) → `plan.md` → `docs/ARCHITECTURE.md` →
`docs/design/03-drivers.md` (authoritative for the spec schema) → the
other module documents as needed. `docs/` is read-only for every phase.
