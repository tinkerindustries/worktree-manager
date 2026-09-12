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
  `WorktreePath` and the IPv4 block arithmetic, the state-path purge
  decision (`Purges`, `PurgeOnTeardown`) with the rails validation holds a
  store deleted on teardown to (a keep flag, a per-worktree template, and
  a default that is not shared), the `removal:` block —
  rm's per-check safety policy and its defaults, through `RemovalPolicy` —
  and the `worktrees:` block: where this repository's worktrees go and
  what they branch from. `path:` defaults to `.claude/worktrees/{slug}`
  (where Claude Code's own isolation puts a tree) and is overridable with
  a `{slug}`/`{app}`/`{home}` template that `WorktreeLocation` resolves
  against the main checkout; `base:` defaults to `origin/main`, a
  remote-tracking ref because a local branch is only as fresh as the last
  pull and the point of naming a base is that it does not depend on the
  state of the checkout that asked. Neither binary creates a worktree, so
  both fields are guidance the `WorktreeCreate` hook follows and
  `wt spec path` computes — never something either binary enforces.
- `internal/treecheck` — the checks that run before a worktree is
  destroyed: uncommitted changes, unpushed commits (an absent upstream is
  its own answer), what gh reports about the branch's pull request, and
  the never-forced `git worktree remove` (run from the repository's main
  checkout, never from the tree it deletes — Windows will not remove a
  directory a process is standing in). `wt rm`, `wt cleanup` and the
  coordinator's sweep each keep their own policy over this one mechanism.
  rm's policy is the repository's, from the spec's `removal:` block: each
  check either refuses (exit 3, or 4 when it could not run) or warns and
  lets rm continue, defaulting to refuse on uncommitted changes and warn
  on the other two, so a personal project with local-only branches and no
  gh is removable out of the box. `wt rm --strict` and `wt rm --force`
  override the block for one run, in the two directions, and neither
  reaches `git worktree remove`, which is still never forced. cleanup and
  the sweep ignore the block and require a merged PR. git and gh reach it
  through a runner function. Deleting a state store is separate from
  tearing one down, and which one a teardown does is the repository's
  policy, from the resource's `purge:` block: by default `rm` deletes a
  store only when the caller selects it, by resource name (`--purge db`)
  or by the flag the block declares (`--purge-db`), which `rm` registers
  the way it registers a machine's `keep_flag`. A resource declaring
  `on_teardown: always` is deleted with the worktree instead, and its
  required `keep_flag` — spelled `--keep <resource>` as well — is the
  one-run way out, the shape `--keep-vm` has for a machine. `spec.Purges`
  is the one decision both the driver and rm read, so the report cannot
  name something the teardown did not delete. A `--purge` or `--keep`
  value selecting no resource is a usage error naming what the repository
  can purge or keep, selecting and keeping one store in the same run is
  another, a declared flag colliding with one of rm's own is refused
  rather than left to panic the flag package, and both the dry run and the
  report name the stores deleted.
  After a teardown reports success, rm asks doctor whether anything of the
  entry outlived it and reports what it finds as drift — never as an exit
  code, because rm cannot undo what it has already destroyed, and never as
  drift when doctor did not answer, which is reported as unverified.
- `internal/driver` — the six-operation contract, the port, namespace,
  state-path, cidr and machine drivers, the docker CLI seam, and the
  sequencing: apply in dependency order with machine forced first among
  its dependents (a VM must exist before anything expects its daemon),
  teardown in reverse continuing past failures. The machine driver's
  rails — the capacity guard (refuse a new instance past
  `max_concurrent`, naming what is running and how to tear one down; the
  count is the daemon's own instances; re-running a running profile stays
  allowed), background warm-up (waited out for a short grace period so an
  instant failure — a missing sibling binary, a corrupt profile — is
  never mistaken for a boot in progress, with the child's output kept in
  `<store root>/logs/machine-<instance>.log`), `--keep-vm`, and the
  documented bypass — are proved against a fake `platform.MachineRunner`;
  the live Colima path is reported not_run on this machine. A namespace
  resource may declare `machine:`, naming the machine resource its
  compose project lives inside; every docker call the namespace driver
  makes on that resource's behalf runs against the bound machine's own
  endpoint (`platform.MachineRunner.DockerEndpoint`), through
  `Docker.WithHost`, never the coordinator's ambient `DOCKER_HOST` or
  docker context — colima start changes that context as a side effect, so
  with two worktrees' machines running the ambient daemon was only ever
  right for one of them, and a teardown addressing the other one's daemon
  could succeed against the wrong worktree's containers with no error to
  notice. A namespace bound to a machine whose instance is absent from
  the runner's own `List()` is torn down vacuously — a compose project
  inside a deleted VM is gone with it — which is what keeps
  `TeardownOrder`'s machine-last ordering from stranding an entry whose
  namespace teardown already failed against a daemon the machine step is
  about to delete anyway; an unreachable daemon alone is never read as
  "gone" the same way. Before removing a project's network the driver
  also force-removes (or, failing that, disconnects) any container still
  attached to it outside the compose-project label — a container the
  application attached through the Docker API at runtime, which the
  label-only listing never sees and which otherwise makes the network
  removal fail outright — naming every one of them in the teardown
  report's notes.
- `internal/cli` — verb dispatch, flag parsing, output, the exit-code error
  type, the one dial-and-request helper (exit 5 lives there), and the
  verbs: `spec validate`, `spec explain`, `spec path` (`--slug` refuses an
  illegal slug, `--name` normalises a caller-supplied one, `--json` adds
  the slug and the base revision), `guard`, `show` (`--brief` is the
  arrival form: identity, isolated values, and the shared block with each
  one's blast radius),
  `daemon status`, `daemon install`, `daemon uninstall`, `bands list`,
  `bands suggest`,
  `bands reserve`, `ports scan`, `init`, `start`, `rm`, `list`, `doctor`,
  `reconcile`, `clients`, `claude install`, `claude uninstall` and
  `cleanup` (gated on gh: missing or
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
  slug validation and the deterministic `NormaliseSlug` that turns a
  caller-supplied name into a legal slug (Claude Code names a worktree
  after the task that prompted it, so the `WorktreeCreate` hook is handed
  a name rather than asked for one), descriptor location, the guard
  engine, and the
  per-session classification cache (`WT_GUARD_CACHE`) the generated guard
  hook sets — keyed on cwd, validated by the stat identity of the root's
  `.git` entry (the root dir's own mtime is unusable: the case-sensitivity
  probe writes a file there), dropped and reclassified with a one-time
  note when the worktree is removed mid-session. `NestedInside` is init's
  refusal of a tree nested inside a *foreign* working tree; a tree sharing
  the git common directory is the same repository — which is what
  `.claude/worktrees/<slug>` is — and the walk continues past it.
- `internal/managed` — the managed block convention, declared once for
  every file the tool writes (`internal/envfile` imports it): the markers, `# wt-field:` records, replace-only-the-block
  regeneration, the unbalanced-marker refusal, append-to-a-markerless-file
  (the CLAUDE.md tripwire joining a repo's own text).
- `internal/artefact` — the phase-7 generated artefacts, rendered per
  repo: the SessionStart tripwire, the opt-in PreToolUse guard hook, the
  settings entries, the reference doc, the CLAUDE.md tripwire, and the
  briefing renderer (which refuses with the descriptor missing). Creating
  and removing a worktree are not among them — `internal/claudehook`'s
  two hook scripts are registered once per machine and answer for every
  repository, so a per-repo skill would be a second implementation of the
  same thing. Bodies are stable; the managed block records the spec
  fields each file came from. Not a verb — the onboarding skill and the
  tests drive it; `cmd/wt` never imports it.
- `internal/platform` — M8: symlink-resolved path realisation (on Windows
  via `GetFinalPathNameByHandleW`, which also canonicalises long paths and
  mapped drives), `SamePath` (the one do-these-name-the-same-directory
  predicate), the mount's case-sensitivity probe (answered once per
  directory per process), the hook shell (`sh -c` on every platform,
  resolved from Git for Windows on Windows and refused by name where no
  POSIX shell exists, because hook commands are shell commands),
  `ExternalPath` (the one conversion out of a realised path into the
  spelling an external tool accepts — the identity on unix, and the strip
  of the extended-length prefix on Windows, which git rejects as an
  argument), `IsAddrInUse` (a bind's address-taken answer, whose errno
  differs between unix and Winsock), `LookHelper` and `HelperCommand` (the one
  resolution of an external helper binary — git, lsof, docker, colima, gh,
  wsl — searching the process PATH first and the platform's known install
  directories only when that fails, because a supervisor-started `wtd` does
  not have the user's shell PATH; the fallback never shadows what PATH
  already resolves, and `WT_HELPER_DIRS` replaces the built-in list.
  `HelperCommand` builds the command as well, prepending the resolved
  binary's own directory to the child's PATH: colima runs limactl and
  docker runs `docker-credential-<store>` from their own PATH at runtime,
  and those siblings sit in the directory the helper was found in. Every
  call site that runs a helper goes through it, so what `wt doctor` reports
  reachable and what the drivers can actually run are the same binary.
  `HelperError` carries the child's stderr into the failure, because a
  helper that fails from the inside puts the whole diagnosis there), `FileIdentity` (the dev/inode/mtime
  the guard cache validates against, from stat on unix and
  GetFileInformationByHandle on Windows), the
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
  fake-runner tests fake; `colima list --json` is a JSON stream, one
  object per line, not an array). The only package permitted to branch on `GOOS`.
- `internal/descriptor` — the per-worktree allocation record: type,
  reader, atomic writer, the shared-block and isolation-state builders,
  and the `info/exclude` ignore rule — used twice by init, for the
  descriptor filename and for the directory the repository's worktrees
  live in, so a repo whose trees sit inside it keeps a clean
  `git status`.
- `internal/envfile` — the `.env` delivery channel: the duplicate strip,
  the first-write seed from the main checkout, and the dotenv-specific
  half of the block. The markers and the block primitives are
  `internal/managed`'s, so one convention covers every file the tool
  writes.
- `internal/claudehook` — the Claude Code integration, driven by `wt
  claude install` and `wt claude uninstall`: the two hook scripts for
  Claude Code's `WorktreeCreate` and `WorktreeRemove` events, the
  per-event merge into the user's `~/.claude/settings.json`, and the
  embedded copy of the onboarding skill. Claude Code hands worktree
  creation to a registered `WorktreeCreate` hook entirely and never falls
  back to `git worktree add` on its own, so the create script answers for
  every repository on the machine: one with a `wt.yaml` gets its worktree
  where the spec says, branched from what `worktrees.base` names, and an
  environment from `wt init`; every other one gets the plain worktree
  Claude Code would have made itself, branched from the session's HEAD,
  plus a sentence naming the worktree-onboarding skill. The name Claude
  Code generates is normalised rather than refused — refusing costs a
  person their worktree over a name nobody typed — and a branch of that
  name that already exists is checked out rather than refused: a branch
  only origin has is fetched first, so the local branch starts at origin's
  tip and the push that follows is a fast-forward. The two refusals that
  remain are a branch checked out in another worktree, which git will not
  check out twice, and a branch origin has whose commits cannot be
  fetched. `WT_HOOK_NO_ENV=1`
  makes the worktree and skips `wt init`, for a change that does not need
  the repository's stack up; the remove script reports `wt rm`'s exit 3
  and exit 4 as the different things they are. Nothing here is rendered from a
  spec — which is why this is linked by `cmd/wt` and `internal/artefact`
  is not — and nothing here touches the store, the registry or an
  allocated worktree. The merge preserves the user's other hooks and
  every other setting; an unparseable settings file and an event
  registered to somebody else's script both refuse. The embedded skill
  under `skill/` is a copy of `.claude/skills/worktree-onboarding`, and
  a test fails on drift between them. Whether an installed file is wt's
  own to replace is decided by what wt recorded writing, never by whether
  it differs from what this build would write — a file that differs is
  either an older release's or an edit, and the two want opposite
  treatment. A hook script is recognised by its managed markers; the
  skill files are documents copied verbatim (and one of them quotes those
  markers in an example), so their digests go in a sidecar,
  `.wt-installed.json`, written with them and removed with them.
- `internal/generate` — the generated Go descriptor reader, stdlib-only
  and gofmt-clean by construction.
  Every finding carries a `kind` — the check that produced it, as a stable
  slug — a one-line `summary` beside the full message, and the `repo` and
  `path` it is about, so a reader and the menu bar app can group, sort and
  act on a report without pattern-matching prose. A bound port on a
  running worktree is that worktree's own service and is reported as an
  observation, never as drift with a remedy that rebuilds nothing.
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
  reports the generated file, with the fields that moved as the finding's
  details — one file is one finding, because a block that predates a
  rename moves every field it records at once. Two files are not compared:
  one governed by a nested `wt.yaml` (a fixture repository committed
  inside another one is that spec's artefact, not this one's) and one
  whose markers do not close the file (the block is being quoted as an
  example, which the onboarding skill's own reference does). Both bounds
  are stated as notes. `doctor` also reports the helper binaries the coordinator cannot
  reach, asked through `platform.LookHelper` in the coordinator's own
  process — git and gh always, whatever `platform.ListenerHelpers` says a
  port scan runs on this machine (lsof on macOS, netstat and tasklist on
  Windows, nothing on a Linux whose scan reads /proc), and docker and the
  machine runner where a spec declares the resources that need them. It covers what
  `LookHelper`'s known install directories cannot: a custom location, for
  which the remedy names `WT_HELPER_DIRS`. Phase 8 adds the
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
  nearest `v*` tag when none is given; each archive nests its five files
  under one directory named for the archive, so unpacking one scatters
  nothing and the installers — which resolve everything relative to their
  own location — sit inside it; it writes a `SHA256SUMS` manifest outside
  the archives and a second one inside each archive covering the two
  binaries by bare name, and wires the archive version and commit into
  both binaries with `-ldflags -X` so `wt --version`/`wtd --version`
  report them; on windows `wtd` alone also gets `-H=windowsgui`, because it
  is the one binary a supervisor starts with no terminal already open — the
  Task Scheduler logon task — and a console-subsystem exe launched that way
  raises a visible window for as long as it runs; `wt` stays a console
  binary, and `wtd`'s own account of itself moves to
  `<store root>/logs/wtd.log` accordingly (`cmd/wtd/main.go`), since a
  GUI-subsystem process started with no inherited console has no stderr to
  reach),
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
- `.claude/skills/worktree-onboarding/` — the onboarding skill (a
  document), invoked as `/worktree-onboarding`: the eight phases, the
  primitives card, the plain-app walkthrough. It is the judgment half of
  the system; the binaries expose facts and refuse.
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

The seven variables are `WT_ENDPOINT` (the coordinator's base URL, e.g.
`http://127.0.0.1:7833`), `WT_HOME`, `WT_STANDALONE`,
`WT_CLIENT_EPHEMERAL` (=1), `WT_CLIENT_TOKEN` (the container token),
`WT_GUARD_CACHE`, the per-session classification cache directory the
generated guard hook sets, and `WT_HELPER_DIRS`.

`WT_HELPER_DIRS` replaces the built-in list of directories
`platform.LookHelper` searches after PATH when it resolves git, lsof,
docker, colima and gh. Whichever directory a helper resolves from is also
the one `HelperCommand` prepends to that helper's own PATH. It exists because `wtd` is started by a supervisor, and a
supervisor's PATH is not a shell's: launchd gives a LaunchAgent
`/usr/bin:/bin:/usr/sbin:/sbin`, which holds git and lsof but not the
`/usr/local/bin` Docker Desktop symlinks its CLI into nor the
`/opt/homebrew/bin` Homebrew puts colima and gh in. The built-in list
covers the usual install locations; this variable covers the ones it
cannot know about. An empty value is a real answer — search nothing beyond
PATH — which is how a test says a binary is absent.

Three more are read by the machine-wide hook scripts alone, never by a
binary: `WT_HOOK_DESCRIPTION` (the description recorded against the
allocation), `WT_HOOK_NO_ENV=1` (make the worktree, skip `wt init`) and
`WT_HOOK_RM_FLAGS` (flags passed through to `wt rm`).

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
# --- managed by wt; edits below are overwritten ---
# wt-field: app=worktree-manager
# wt-field: band coord=7840
# wt-field: descriptor=wt-env.json
# wt-field: resources=coord, store
# wt-field: shared=~/.wt/wt.db and 127.0.0.1:7834, ~/.local/bin/wt and ~/.local/bin/wtd, ~/.claude/settings.json, the launchd registration (dev.mrgeoffrich.worktreemanager)
# wt-field: worktrees=.claude/worktrees/{slug}
This repository uses per-worktree environments.
- Never hardcode a port or a path: read them with `wt show`.
- Something else creates the worktree; `wt init` attaches to it.
See docs/wt.md for the full reference.
# --- end ---
