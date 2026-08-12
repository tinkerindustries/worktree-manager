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
  `daemon status`, `daemon install`, `bands list`, `bands suggest`,
  `bands reserve`, `ports scan`, `init`, `start`, `rm`, `list`, `doctor`,
  `reconcile`, `clients` and `cleanup` (gated on gh: missing or
  unauthenticated gh cleans nothing and exits 4; the full rm safety
  checks apply even when the PR is merged; unverifiable entries are never
  touched). Since phase 8b `daemon status` reports the systemd lingering
  caveat on Linux, and `daemon install` registers with launchd, systemd
  (with its paired socket unit) or the Task Scheduler per platform.
- `internal/identity` — M1: classification, root resolution, containment,
  slug validation, descriptor location, the guard engine, and the
  per-session classification cache (`WT_GUARD_CACHE`) the generated guard
  hook sets — keyed on cwd, validated by the stat identity of the root's
  `.git` entry (the root dir's own mtime is unusable: the case-sensitivity
  probe bumps it every call), dropped and reclassified with a one-time
  note when the worktree is removed mid-session.
- `internal/managed` — the generated-artefact block convention: the `.env`
  block's own markers, `# wt-field:` records, replace-only-the-block
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
  mapped drives), the mount's case-sensitivity probe, the socket path, the
  named-pipe transport on Windows (owner-only ACL, pipe_windows.go), peer
  credentials (the pipe's ACL is the whole of host identity on Windows),
  the private store-dir permission model (0700 on unix; the current-user
  ACL, with the refusal to hold credentials where it cannot be set, on
  Windows), the atomic-write helper (the directory-fsync step is unix-only
  and stated), the supervisor seam (launchd on macOS, the systemd user
  unit with its paired .socket unit and LISTEN_FDS socket activation on
  Linux, the logon scheduled task on Windows), and the machine runner seam
  (Colima on macOS, WSL2 on Windows, nothing elsewhere — the
  `MachineRunner` interface the machine driver shells out through and its
  fake-runner tests fake). The only package permitted to branch on `GOOS`.
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
  registry is the only source of repository locations.
- `cmd/wt`, `cmd/wtd` — the two entry points.
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

## Environment

The six variables are `WT_SOCKET`, `WT_HOME` (read by `wtd` alone),
`WT_STANDALONE`, `WT_CLIENT_EPHEMERAL` (=1), `WT_CLIENT_TOKEN` (phase 6:
the named-container token; setting it together with the ephemeral
declaration is refused as ambiguous) and — phase 7's answer to the scope
question — `WT_GUARD_CACHE`, the per-session classification cache
directory the generated guard hook sets. A seventh is a scope question.

## Reading order

`PLAN-SCOPE.md` (frozen scope) → `plan.md` → `docs/ARCHITECTURE.md` →
`docs/design/03-drivers.md` (authoritative for the spec schema) → the
other module documents as needed. `docs/` is read-only for every phase.
