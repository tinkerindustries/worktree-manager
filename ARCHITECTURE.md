# ARCHITECTURE.md

The as-built structure of this repository. `docs/ARCHITECTURE.md` is the
architecture of record for the system being built — this file is the code
map and changes as the code does; that one does not move. The two files are
deliberately both named `ARCHITECTURE.md` (plan.md §7).

## Packages

One module, `github.com/mrgeoffrich/worktree-manager`, two binaries, eight
internal packages:

```
cmd/wt        the client: verb dispatch into internal/cli, then os.Exit
cmd/wtd       the coordinator: socket listener, protocol loop, graceful
              shutdown; reads WT_HOME and WT_SOCKET, logs to stderr
internal/cli  verb dispatch, flag parsing, output, the exit-code error
              type, the one dial-and-request helper, the daemon verbs
internal/spec the wt.yaml schema: parser, validator, template evaluator,
              the walk-up finder, the quoted YAML emitter
internal/identity  M1: classification, root resolution, containment,
              slug validation, descriptor location, the guard engine
internal/platform  M8: path realisation, the mount's case-sensitivity
              probe, the socket path, peer credentials, the private-dir
              permission model, the supervisor (launchd) seam; the only
              package permitted to branch on GOOS
internal/descriptor  M5 read side: the per-worktree allocation record and
              its reader (yaml and json); phase 5 writes the same type
internal/protocol  the wire between the two binaries: message types,
              newline-delimited JSON framing, version negotiation
internal/store  the coordinator's state directory: root resolution,
              atomic writes, the schema_version envelope, clients.json
internal/coord  the coordinator's request core, socket server and the
              in-process harness; one writer serialises here
```

Import rules, fixed for the whole plan:

- `cmd/wt` → `internal/cli` → `internal/spec`, `internal/identity`,
  `internal/descriptor`, `internal/platform`, `internal/protocol`.
  Nothing else.
- `cmd/wt` may **never** import `internal/store`, `internal/coord`,
  `internal/driver` or `internal/fleet` — they are coordinator-only, and
  that separation is what keeps `WT_HOME` unreadable by a client.
  Enforced by `TestWtNeverImportsCoordinatorPackages` in `cmd/wt/`, which
  runs `go list -deps` over the real dependency graph, so a transitive
  import fails the suite too.
- `internal/coord` → `internal/store`, `internal/protocol`,
  `internal/platform`. Coordinator-only.
- `internal/store` → `internal/platform` (the permission model is a
  platform surface). Coordinator-only.
- `internal/protocol` → standard library only; both binaries link it.
- `internal/identity` → `internal/spec`, `internal/platform`,
  `internal/descriptor`. It contains no platform branch of its own.
- `internal/descriptor` → `internal/spec` (the `spec.Resolved` shape of its
  resources map).
- `internal/platform` imports only the standard library and is the only
  package that may branch on `GOOS`; no `runtime.GOOS ==` and no
  `_darwin.go` build tag exists anywhere else.
- The one third-party dependency is the YAML package in `go.mod`. No CLI
  framework, validation library, logging framework or test framework —
  flags, validation, logging (`log/slog`, `wtd` only) and tests are the
  standard library.

## The spec library (`internal/spec`)

- `Parse` decodes a `wt.yaml`, refusing unknown fields and any version
  other than 1 (naming the version found and the versions supported).
- `Validate` checks the whole spec. Every refusal is a `FieldError` naming
  the field and the reason: template cycles (named as a path), unknown
  resources and unknown variables, per-type field sets and constraints,
  the stride ceiling (`slots.max` ≤ 99), and the descriptor/reader
  formats. Resolved-name length caps are checked against a canonical
  worst-case context (longest slug, highest slot, fixed home/worktree
  paths), so a template that can overflow is refused at validation rather
  than at allocation.
- `Resolve(spec, Context)` is the pure function behind `spec explain`: one
  slot, one slug, one set of band bases, and it returns the resource
  table. It reads no environment and touches nothing — phase 3's
  coordinator calls the same function with the ledger's bases.
- Templates substitute `{app}`, `{slug}`, `{slot}`, `{home}`,
  `{worktree}` and the names of other resources, in dependency order, with
  cycle detection. Every `{...}` is a reference; a literal brace cannot be
  expressed. A hook's run template may also reference that hook's own
  declared params.
- `FindSpecPath` walks up from a directory to the worktree root and stops
  at the first `wt.yaml` — never above it. The generated reader (phase 5)
  depends on this rule.
- `EmitYAML` quotes every string scalar, unconditionally. A worktree may
  be slugged `no`, `on`, `off`, `yes` or `y`, and an unquoted emitter
  would turn those into booleans on the way back in (docs/ARCHITECTURE.md
  §8.2). Phase 5's descriptor emitter uses this same function.

## Identity and containment (`internal/identity`, `internal/platform`)

- `Classify(cwd, standalone)` runs real git: `rev-parse --show-toplevel`,
  `--git-dir`, `--git-common-dir` and, for linked worktrees, `worktree list
  --porcelain` (the first entry is the main checkout). Four outcomes: not a
  repository, primary checkout, linked worktree, standalone clone. The main
  checkout path is a field on the result, never a function — a
  `mainCheckoutPath()` beside `worktreeRoot()` is the shape of the D4 bug
  (01-identity.md §3). The deleted-worktree directory and the
  standalone-in-a-linked-worktree combinations are refused with named error
  types, never translated into "not a repository".
- The acting root is always the current worktree's own root, from
  `--show-toplevel` — never a walk back to the main checkout.
- `Contains(root, path)` resolves symlinks on both sides (via
  `platform.RealPath`, which resolves a not-yet-created path through its
  deepest existing ancestor) and compares on path segments, not string
  prefixes. The casing comparison follows the probed mount, not `GOOS`
  (08-platform.md §4.2): `platform.CaseSensitive` probes the mount by
  creating one probe file, and where the probe cannot determine the answer
  the comparison assumes case-insensitive, the conservative direction.
- `ValidateSlug` is the reason-giving form of `spec.ValidSlug` — one
  implementation of the rule, no second regexp. The slug defaults to the
  worktree directory basename, never the branch (`DefaultSlug`).
- `DescriptorPath(root, spec)` joins the worktree root with
  `emit.descriptor.filename`; the filename comes from the spec, never from a
  constant here.

## The descriptor (`internal/descriptor`)

- The per-worktree allocation record per 05-delivery.md §2.3, minus the
  `view` field revision 2 deleted: version, app, slug, slot, path, standalone
  flag, description, resolved resources (as `spec.Resolved` entries, port
  values normalised to `int` in both formats), isolation state, the
  structured `shared` block of name-and-impact pairs, and an `extras` map
  round-tripped untouched.
- `Read(path, format)` takes the format from `emit.descriptor.format` —
  yaml or json, never sniffed — refuses unknown fields, refuses a newer
  schema version naming the upgrade, and reports an unparseable descriptor
  without ever overwriting it (phase 5 owns the emitter; it marshals this
  same type).

## The guard (`internal/identity` guard engine, `wt guard` verb)

- The enforcement hook of ARCHITECTURE.md §9.6, local and opening no socket.
  It classifies cwd, resolves `file_path` and denies with a reason naming
  the correct worktree root verbatim, so the model reading the denial
  corrects itself and retries.
- Three denials, one per call: mutation of a live shared store named in the
  descriptor's `shared` block; a write whose resolved path lies outside the
  worktree; a read of a `CLAUDE.md` outside the worktree.
- It fails open on ambiguous shell constructs — pipes, `$(...)`, environment
  variables and globs — and on unknown tools: its goal is to make the
  obvious bypass loud, not to be a general bash sandbox. Denied exits 3
  (refused by a safety check); a PreToolUse payload on stdin is the primary
  input surface, with `--cwd/--tool/--input/--path` carrying the same fields
  for hand use.

## The protocol (`internal/protocol`)

- The wire is newline-delimited JSON: one JSON object per message, each
  terminated by a newline — no framing header, no length prefix, no
  protobuf, no gRPC. One MiB cap per message.
- The first exchange on a connection is a hello: the client's protocol
  version **range** (`min_version`/`max_version`) and its identity
  declaration. The coordinator replies with the agreed version (the
  highest common one) or refuses; a non-overlapping pair refuses whole and
  names the upgrade in whichever direction the ranges imply
  (`protocol.Agree`, `UpgradeError`).
- Requests carry a verb and its arguments; responses carry either a result
  or an error with the exit code the client should use — that is what lets
  codes 3, 4 and 5 originate in the coordinator and still reach the
  process exit status unchanged.

## The store (`internal/store`)

- Root resolution: `WT_HOME` when set, else `$HOME/.wt`; with both unset
  the coordinator stops and never invents a location. `WT_HOME` is read by
  `wtd` alone, never by a client.
- Directory `0700`, files `0600`, created by `platform.EnsurePrivateDir`
  (the permission model is a platform surface; Windows refuses to write
  credentials where the equivalent ACL cannot be set — 08-platform.md
  §4.6, compiled but unverified in this phase).
- The one write path is atomic: temp file in the same directory, fsync
  the file, rename, fsync the directory — same directory because a rename
  across filesystems is not atomic.
- Every store file carries a `schema_version` field; a file written by a
  newer schema is refused on read naming the upgrade, never written back.
- JSON, never YAML: slugs match `^[a-z0-9][a-z0-9-]*$`, which admits `no`,
  `on`, `off`, `yes` and `y`, and a YAML 1.1 parser turns all of those
  into booleans.
- This phase's one store file is `clients.json`: identity, kind, the
  coordinator's own last-seen measurement, and the ephemeral flag.

## The coordinator (`internal/coord`)

- `Handler` is the request core — protocol messages in, protocol
  responses out, a store path, no socket, no supervisor, no container —
  which is the in-process harness of ARCHITECTURE.md §13.3. `Harness`
  wraps it for the tests: a full request runs with no socket at all.
- Identity is enforced, never claimed: a host client's identity is the
  kernel's uid from peer credentials (`platform.PeerUID`: SO_PEERCRED on
  Linux, LOCAL_PEERCRED on macOS, both via the standard library's
  syscall), a named container's is its token, an ephemeral one's is a
  session id the coordinator issues. Every connection is observed in
  `clients.json`, so last-seen is measured rather than written.
- One writer serialises here. There is no lock file, no generation
  counter, no view identity and no view scoping — concurrent requests
  serialise in this one process, and the handler's mutex makes that true
  across its connection goroutines.
- `Server` is the socket loop with graceful shutdown: context
  cancellation (SIGINT/SIGTERM from `cmd/wtd`), in-flight requests
  allowed to finish, socket file removed on exit.

## The client (`internal/cli`, `cmd/wt`)

- One exported error type carries the exit code, the message and the
  remedy; `main` maps the code to the process exit status. An error that
  reaches the user with an empty remedy is reported as a defect — the
  "every error names the command that fixes it" rule (plan.md §3) is
  structural, not a habit.
- Exit codes are fixed: 0 success, 1 failure, 2 usage, 3 refused by a
  safety check, 4 required context unavailable, 5 coordinator unreachable.
  Code 5 is wired in exactly one place: `dialCoordinator`, the client's
  one dial-and-request helper, whose remedy is always the platform's
  start command (`platform.CoordinatorStartCommand`) — every verb that
  reaches the coordinator goes through it. `show` and `guard` never dial,
  and `spec validate` and `spec explain` are pure functions of the spec
  and their arguments, so all four work with the coordinator stopped.
  `daemon status` dials as its reachability probe but treats failure as a
  state, never as an error.
- Results go to stdout; diagnostics to stderr. `--json` prints exactly one
  JSON object on stdout and nothing else. No colour, no spinner, no
  prompt.
- Verbs are hand-dispatched with one `flag.FlagSet` per verb. This phase
  has six: `spec validate`, `spec explain`, `guard`, `show`,
  `daemon status` and `daemon install`.
- `daemon status` distinguishes four states — running, not registered,
  registered but stopped, running but unreachable — and names a different
  fix for each broken one. The state machine is a pure function of two
  platform observations (registration file present, supervisor says
  running) and the dial outcome, so the tests drive all four states on
  any platform; the launchd-backed observations exist on macOS.
- `daemon install` registers the coordinator with the platform's
  supervisor and starts it: a launchd LaunchAgent at
  `~/Library/LaunchAgents/com.mrgeoffrich.wtd.plist` (RunAtLoad and
  KeepAlive — not socket activation, which needs the C-only
  `launch_activate_socket` API this CGO_ENABLED=0 project cannot reach).
  `--prefix` directs the registration at a temporary directory and loads
  nothing, so no test ever touches the machine's launchd; Linux and
  Windows refuse a real registration with exit 4 (phase 8 owns both).
- The only environment variables read anywhere are `WT_SOCKET`,
  `WT_HOME` (wtd alone), `WT_STANDALONE` and `WT_CLIENT_EPHEMERAL` (the
  ephemeral declaration, `=1`). Adding a fifth is a scope question.

## Invariants (plan.md §3), as they bind this phase

- Both binaries perform no inference.
- The system refuses rather than partially honouring: a spec with a field
  this schema does not know is refused whole, and a client and coordinator
  whose protocol version ranges do not overlap refuse to proceed and name
  the upgrade — in either direction.
- `unavailable` is distinct from failure; an absent `wt.yaml` is reported
  as not adopted (exit 4), never as an obscure error.
- Hooks never run in the coordinator. The semantics of hooks and emission
  land in phases 4 and 5; only the field set is frozen.

## Gotchas

- Unix socket paths are length-limited (104 bytes on macOS), so socket
  paths under temp directories keep short basenames in tests.
- A closure that returns a `*Error` (cli's exit-code type) as an `error`
  hands callers a non-nil interface holding a nil pointer — the state
  machine in `daemon status` depends on the dial probe returning a plain
  `nil`, which is why `daemonDeps.reachable` normalises before returning.
- YAML values that begin with `{` are flow mappings: a template must be
  quoted (`template: "{app}-{slug}"`), or it parses as a map and the
  string field fails to decode.
- `spec validate`'s length-cap check uses fixed home/worktree paths, so
  its verdict does not depend on the machine it runs on; `Resolve` checks
  caps again against the actual context.
- Fixture specs embed `{slot}` in their templates so two explain runs for
  different slots of the same slug still resolve disjoint tables — the
  phase-0 exit criterion.
- `testdata/` is excluded from `go build ./...`; the fixture repositories
  are ordinary file trees with no `.git`, because later phases copy them
  into temp directories and initialise git there.
- The classification tests in `internal/identity/` build real repositories
  with `git init`, `git worktree add` and `git clone` in `t.TempDir()`, so
  that layer needs git on PATH — the point of the layer is that git's real
  output, not a mock of it, is what classification is tested against.
- git reports `--git-dir` and `--git-common-dir` relative to cwd, and
  resolves `--show-toplevel` symlinks to the real path; classification
  absolutises the former and compares the symlink-resolved forms, and the
  acting root is always the real path git reports.
- `platform.RealPath` resolves a not-yet-created path through its deepest
  existing ancestor — the guard must be able to approve a first write to a
  new file. The case-sensitivity probe creates one probe file per call; it
  is cheap, and whether the guard should cache it per session is an open
  question (plan.md §9.2).
