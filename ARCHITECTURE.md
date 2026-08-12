# ARCHITECTURE.md

The as-built structure of this repository. `docs/ARCHITECTURE.md` is the
architecture of record for the system being built — this file is the code
map and changes as the code does; that one does not move. The two files are
deliberately both named `ARCHITECTURE.md` (plan.md §7).

## Packages

One module, `github.com/mrgeoffrich/worktree-manager`, two binaries, eleven
internal packages:

```
cmd/wt        the client: verb dispatch into internal/cli, then os.Exit
cmd/wtd       the coordinator: socket listener, protocol loop, graceful
              shutdown; reads WT_HOME and WT_SOCKET, logs to stderr
internal/cli  verb dispatch, flag parsing, output, the exit-code error
              type, the one dial-and-request helper, the daemon verbs,
              and the lifecycle verbs — init (the seven-step sequence
              with rollback up to activation, the four attach outcomes),
              start (the bring-up hooks, no coordinator), rm (the three
              tree-reading safety checks, then reap + teardown + git
              worktree remove), and the hook sequencer (sticky
              parameters, health polling, process-group timeouts)
internal/spec the wt.yaml schema: parser, validator, template evaluator,
              the walk-up finder, the quoted YAML emitter
internal/identity  M1: classification, root resolution, containment,
              slug validation, descriptor location, the guard engine,
              and the nested-worktree refusal
internal/platform  M8: path realisation, the mount's case-sensitivity
              probe, the port-probe socket options (SO_REUSEADDR set on
              unix, unset on Windows — the one GOOS branch callers never
              see), the socket path, peer credentials, the private-dir
              permission model, the atomic-write helper, the supervisor
              (launchd) seam, listener discovery (lsof on macOS,
              /proc/net on Linux, netstat+tasklist on Windows) and
              signalling (TERM/KILL by pid, process groups); the only
              package permitted to branch on GOOS
internal/descriptor  M5: the per-worktree allocation record, its reader
              (yaml and json), its atomic writer, the shared-block and
              isolation-state builders, and the info/exclude ignore rule
internal/envfile  M5: the .env delivery channel — the wt-managed block,
              the duplicate strip, the first-write seed from the main
              checkout, and the unbalanced-marker refusal
internal/managed  M7: the generated-artefact block convention — the .env
              block's own markers, the `# wt-field:` records doctor
              compares, replace-only-the-block regeneration, the
              unbalanced-marker refusal, append-to-a-markerless-file
internal/artefact  M7: the phase-7 generated artefacts, rendered per
              repo from the templates under artefact/templates/ (the two
              skills, the SessionStart tripwire, the opt-in PreToolUse
              guard hook, the settings entries, the reference doc, the
              CLAUDE.md tripwire) and the briefing renderer; not a verb —
              the onboarding skill and the tests drive it, cmd/wt never
              imports it
internal/generate  M5: the generated Go descriptor reader — the source
              an adopted repo compiles into its own entry points, with an
              embedded YAML-subset parser (stdlib only)
internal/protocol  the wire between the two binaries: message types,
              newline-delimited JSON framing, version negotiation, and
              the verb payloads — the phase-3 allocate/activate/release
              and bands verbs, phase 5's materialise and rm, and phase
              6's list, doctor, reconcile and clients.list, which carry
              the parsed spec where a teardown needs it
internal/store  the coordinator's state directory: root resolution,
              atomic writes, the schema_version envelope, clients.json,
              registry.json, bands.json (with the per-base spans doctor
              reads) and specs.json (the per-app spec cache reclamation
              falls back to)
internal/coord  the coordinator's request core, socket server and the
              in-process harness; one writer serialises here. Phase 5
              adds the materialise verb (driver apply with the
              reverse-order rollback), the rm verb (reap, then teardown,
              then the entry drop) and the reaper, whose allowlist comes
              from the spec's reaper.binaries field (phase 6). Phase 6
              adds the fleet verbs — list (the stale/unverifiable/
              reclaimable/foreign markers, secrets served to the owning
              client alone), doctor (reads everything, writes nothing,
              every finding naming its fix), reconcile (the caller's own
              entries plus aged-out ephemeral ones, repaired by the
              existing paths) and clients.list — plus reclamation, the
              coordinator's timer's teardown of aged-out ephemeral
              clients' entries by handle
internal/driver  M3: the six-operation driver contract, the port,
              namespace, state-path, cidr and machine drivers, the docker
              CLI seam, the platform machine-runner seam, and the
              sequencing (apply in dependency order with machine forced
              first among its dependents, teardown in reverse continuing
              past failures)
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
  `internal/platform`, `internal/driver`, `internal/artefact` (the drift
  check compares against `artefact.FieldsFor`, the renderer's own field
  set — the recorded and compared sides cannot disagree).
  Coordinator-only.
- `internal/store` → `internal/platform` (the permission model is a
  platform surface). Coordinator-only.
- `internal/protocol` → standard library plus `internal/spec` (the phase-3
  verb payloads carry the parsed spec, per ARCHITECTURE.md §8.1); both
  binaries link it.
- `internal/driver` → `internal/spec`, `internal/platform`,
  `internal/identity`. Coordinator-only: `cmd/wt` may never import it.
- `internal/identity` → `internal/spec`, `internal/platform`,
  `internal/descriptor`. It contains no platform branch of its own.
- `internal/descriptor` → `internal/spec` (the `spec.Resolved` shape of its
  resources map, `spec.EmitYAML` for emission, `spec.Substitute` for the
  shared block) and `internal/platform` (the atomic write). It never
  imports the coordinator's store — the client-side emitters cannot.
- `internal/envfile` → `internal/spec` (the emit.env templates resolve
  through `spec.Substitute`) and `internal/platform` (the atomic write).
- `internal/generate` → `internal/spec` (the spec the reader is generated
  from) plus `go/format` from the standard library. The generated source
  itself imports only the standard library — it is copied into an adopted
  repository, so it can never add a dependency to that repo's go.mod.
- `internal/artefact` → `internal/spec`, `internal/managed`, plus the
  embedded templates under its own `templates/` directory (which carries
  a nested CLAUDE.md stating the directory's rules). Not linked by any
  binary.
- `internal/managed` → the standard library. The block convention is
  line-based text; nothing else to it.
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
  §8.2). The descriptor emitter uses this same function.
- `Substitute(t, ctx, resolved)` resolves a non-resource template — a
  hand-authored shared name, a seed source, an emit.env key — against the
  builtin variables plus the already-resolved resource table.

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
  without ever overwriting it.
- `Write(path, format, d)` is the emitter on the same type: yaml through
  `spec.EmitYAML` (every string scalar quoted, so a slugged `no` never
  round-trips as false) or json through `encoding/json`, written
  atomically via `platform.AtomicWrite` at 0600. The descriptor is always
  written, whatever else a repo chooses (05-delivery.md §1), and it lands
  at the worktree root, undotted, at `emit.descriptor.filename`.
- `BuildShared(s, ctx, resolved, impact)` builds the shared block's two
  halves (03-drivers.md §6): every resource with `default: shared`
  contributes itself with its resolved path and its impact text — the
  text arrives as a callback because only the coordinator side imports
  the drivers — and the spec's hand-authored `shared:` entries resolve
  their template names through `spec.Substitute`. A shared resource with
  no resolved value, or an empty impact text, is an error: a stale shared
  block is worse than none because it is believed.
- `BuildState(s)` carries the spec's isolation decisions into the
  descriptor: one entry per state-path resource, `isolated: false` for
  `default: shared`, `isolated: true` otherwise.
- `EnsureIgnored(worktreeRoot, gitCommonDir, filename)` implements
  05-delivery.md §2.2: if adoption already committed the ignore line to
  the worktree root's `.gitignore`, nothing is written; otherwise the line
  goes to `$GIT_COMMON_DIR/info/exclude`, idempotently and atomically. A
  tracked `.gitignore` is never touched — in a linked worktree it is
  shared through the branch, and every write would dirty a working tree
  another tool just created clean. The git common dir and the worktree
  root come from the classification (`identity.Classification`), the only
  way the caller should reach either.

## Delivery — the three channels (`internal/envfile`, `internal/generate`)

05-delivery.md §1's channels, as functions another package calls — init
(phase 5's other half) sequences them; nothing here is a verb.

- **The descriptor** is channel one, above.
- **The `.env` managed block** (`internal/envfile`): `Update(path, env,
  ctx, resolved, seedFrom)` rewrites a repo's .env through the marked
  block — a re-run replaces only the marked lines, so hand edits above
  and below survive. Any definition of a managed key found outside the
  block is stripped rather than left to be shadowed, and the report names
  the keys (duplicates cannot be ordered: dotenv parsers disagree about
  which duplicate wins). Unbalanced or nested markers refuse the write,
  naming the line numbers. A first write seeds from the main checkout's
  .env — the `seedFrom` path the caller reaches through
  `identity.Classification.MainCheckoutPath`, the one legitimate consumer
  of that path — copying unmanaged content only: managed keys come from
  this worktree's allocation, never from slot 0. A missing source file
  seeds nothing and is not an error; the report says the seed was
  skipped. A fresh .env is written 0600, an existing one keeps its mode.
- **The generated reader** (`internal/generate`): `Reader(spec)` renders
  the Go source an adopted repo compiles into its own entry points
  (05-delivery.md §4), baked from `emit.reader` (package, app-scoped env
  var), `emit.descriptor` (filename, format) and the resource list. It
  reads the descriptor file and nothing else — no registry, no socket, no
  `WT_HOME`, no wt binary installed — and applies the four-row
  resolution table: explicit flags (fully supplied short-circuits the
  descriptor read entirely, B18.2); an env override naming a descriptor,
  `--env` beating the app-scoped variable and a foreign-app descriptor
  refused naming both apps; the descriptor found by walking up from cwd
  to the worktree root, with git (the repo's own tool) telling the
  primary checkout — legacy default, slot 0 keeps the committed defaults
  — from a linked worktree; and a missing descriptor in a linked worktree
  of an adopted repo refusing loudly, naming `wt init`. The status output
  reports `env_source` and `env_path`. The generated source depends on
  the standard library alone and is gofmt-clean by construction: a
  yaml-format descriptor is parsed by a small parser for the exact YAML
  subset the emitter writes (block mappings, block sequences, quoted and
  plain scalars, empty flow collections), and anything outside the subset
  — or a newer schema version — is refused whole.

## The atomic write (`internal/platform`)

- The repository's one atomic write path: temp file in the same
  directory, fsync the file, rename, fsync the directory. It moved here
  from the store in phase 5 because the client-side emitters owe the same
  guarantee and never import the coordinator's store;
  `store.AtomicWrite` is a one-line delegate, so the sequence exists once
  and cannot drift.

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
- Phase 7's answer to the guard-caching question (plan.md §9.2): the
  generated hook sets `WT_GUARD_CACHE`, and guard caches its
  classification per session, keyed on the resolved cwd. The cache is
  validated by the stat identity of the root's `.git` entry — the root
  directory's own mtime is unusable because the case-sensitivity probe
  creates one probe file per call in it, self-invalidating any entry that
  keyed on it. A worktree removed mid-session drops the cache; the next
  call reclassifies, failing open with a one-time note. The descriptor is
  never cached: the shared-store denial reads it fresh every call.

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
  across filesystems is not atomic. The sequence lives in
  `internal/platform` since phase 5 (the client-side emitters owe it and
  never import the store); `store.AtomicWrite` delegates to it, and the
  registry, the ledger and clients.json all go through it.
- Every store file carries a `schema_version` field; a file written by a
  newer schema is refused on read naming the upgrade, never written back.
  The registry adds a second rail: `ReadRegistryList` decodes a newer file
  well enough to list it (carrying the file's own version), and
  `WriteRegistry` refuses to write a file whose version claims a newer
  schema — "lists but does not write" is structural at the write path.
- JSON, never YAML: slugs match `^[a-z0-9][a-z0-9-]*$`, which admits `no`,
  `on`, `off`, `yes` and `y`, and a YAML 1.1 parser turns all of those
  into booleans.

### The registry (`registry.json`)

One file for every entry across every repo, so a multi-entry operation
stays atomic under a single rename. The entry's fields are
`docs/ARCHITECTURE.md` §8.3 — there is no `view` field; `owner`,
`owner_kind`, `ephemeral` and `path_visible` replaced view identity.

- `owner`/`owner_kind` name the client identity that created the entry;
  the authorisation check reads them (the refusal message names both).
- `resources` is denormalised deliberately: a cross-repo `list` cannot
  depend on each repo's spec being readable, so the registry is
  self-describing.
- `secrets` is a named field rather than a free-form section so that
  redaction is structural: a field the code knows to be a secret cannot be
  served by a response encoder written before that secret existed.
- `path_visible` records whether the coordinator can stat the recorded
  path. A path that exists only inside a container is recorded, never
  checked, and never counted as stale.
- An unparseable registry is reported, never truncated and recreated, and
  the refusal names the rebuild.

### The band ledger (`bands.json`)

- Maps each app to the port bases it holds (keyed by port resource name,
  the shape `spec.Context.Bases` feeds into resolution), and holds the
  machine-wide reservations no app may allocate from.
- Registration is explicit: the coordinator refuses to allocate for an app
  with port resources and no registered band, naming the registration
  command. The coordinator computes the band's required size from the
  spec — slot ceiling × ports per slot — so the onboarding skill only
  chooses where the bases sit, not how large they are.
- Exclusions come from two places, both enforced at allocation: the
  spec's `reserved` block (a property of the repository) and the ledger's
  host-global reservations (a property of the machine). A host reservation
  carries a required note naming what holds the range — nothing infers a
  production stack, a person declares it once per machine. Since phase 4 a
  reservation may also carry compose project names (`wt bands reserve
  --host --name <project>...`): a resolved namespace matching one is
  refused rather than torn down, naming the reservation, because
  label-based teardown is the one operation that could otherwise reach a
  co-resident stack the tool knows nothing else about (03-drivers.md §4.2,
  B8.2).
- The store files are `clients.json`, `registry.json`, `bands.json` and —
  since phase 6 — `specs.json`, each carrying the `schema_version`
  envelope. The band ledger records each base's span (the size the
  coordinator computed at registration), which is what doctor's overlap
  check reads without any app's spec; `specs.json` is the per-app spec
  cache every allocate writes, the fallback reclamation uses when an
  entry's path is not visible from the host.

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
  across its connection goroutines. The concurrency test is
  `TestConcurrentAllocationsGetDistinctSlots`: two goroutines allocate
  against one app and get different slots.
- `Server` is the socket loop with graceful shutdown: context
  cancellation (SIGINT/SIGTERM from `cmd/wtd`), in-flight requests
  allowed to finish, socket file removed on exit. It also runs the
  reserving-ageing sweeper: `AgeReserving` drops a `reserving` entry past
  its 10-minute timeout on a one-minute tick — the resident process needs
  no scheduler for this, and the timeout also covers a client that died
  mid-sequence.

### Allocation

- Lowest free slot from 1 upward, per app, skipping slots held by any
  entry of the app, slots whose derived ports fall in either exclusion
  set, and slots whose resources probe held. Slot 0 is the primary
  checkout: never allocated, never managed.
- An existing entry's slot is authoritative (rule 5 below): re-running
  `init` reconciles and rebuilds, it never reallocates — `allocate` for an
  existing (app, slug) returns the stored allocation, refused when the
  entry belongs to another client.
- The probe is the phase-4 seam: `Handler.Probe` reports `ProbeFree`,
  `ProbeHeld` or `ProbeUnavailable` for a candidate slot's derived
  resources, and `InstallDrivers` builds it from the driver registry:
  only the drivers that gate allocation are consulted (port, and cidr
  from phase 8), a held port skips the slot — never remediated, the
  allocator says which slot it skipped — and unavailable does not block
  allocation, leaving a `probe_note` on the allocate result stating that
  the registry was the only check performed (03-drivers.md §4.1). A
  namespace hit is not consulted: it means a previous teardown was
  incomplete, not that the slot is taken.
- On exhaustion the message names the range, `wt cleanup`, and how many of
  the occupied slots the caller cannot free (the ones owned by other
  clients) — otherwise the remedy it names would appear to do nothing.

### Authorisation

- The ownership check runs on every mutating call (`activate`, `release`,
  and `allocate` against an existing entry): an entry owned by another
  client is refused with exit code 3, naming the owner (kind and key) and
  its last-seen time from clients.json. The coordinator acts with host
  privilege on a caller's behalf, so ownership is the boundary that
  replaces filesystem permissions. Reads are unrestricted apart from
  `secrets`, which are served to the owning client alone.

### Entry lifecycle

- `reserving` — slot claimed, resources not yet materialised; aged out on
  the coordinator's own timer.
- `active` — fully materialised.
- `tearing-down` — a resting state: teardown left resources behind, so the
  slot stays held and the entry keeps a `teardown_note` listing exactly
  what survived. A re-run that frees everything drops the entry; `release`
  drops it directly — the rollback path a client drives when init fails
  before activation.

### Fleet and reclamation (phase 6)

- `list` computes the markers from the registry plus one stat per visible
  path: `stale` when the directory is gone, `unverifiable` when the path
  was never visible to the coordinator (a container path), `reclaimable`
  when the ephemeral owner's measured last-seen is older than the
  reclamation interval, `foreign` when the entry belongs to another
  client. The registry is read with the lenient `ReadRegistryList`, so a
  newer schema still lists.
- `doctor` runs every check through the handler and writes nothing (the
  test pins byte-identical store files). The uninitialised-worktree check
  discovers repositories through the registry's visible entry paths and
  runs `git worktree list`; a repo with no entries is not scanned, and the
  bound is stated. The band-overlap check reads the per-base spans the
  ledger records at registration, so it needs no app's spec; two port
  resources of one app sharing a base (the group form) are not an
  overlap.
- `reconcile` is thin by construction: the client runs init's repair path
  (`runInit` with the entry's own slug, description and directory) for an
  eligible entry whose directory is present, and the coordinator verb
  re-runs rm's reap-teardown-drop sequence (or the reserving rollback)
  for the rest. Eligibility — the caller's own entries plus ephemeral
  entries whose owner has aged out — is re-checked coordinator-side, and
  every other entry is reported as skipped with the reason.
- The generated-artefact drift check (phase 7) runs inside doctor: every
  repo reachable through the registry has its tracked files scanned for
  the managed marker, and the recorded `# wt-field:` records are compared
  against the current spec (walk-up from the main checkout) and the band
  ledger — `artefact.FieldsFor` is the field set on both sides. A moved
  band, a renamed resource, a changed descriptor filename or shared list
  is a warning naming the generated file and the field that moved, with
  the remedy naming the skill's generate phase (hand edits outside the
  block survive regeneration). The scan is the convention, not
  inference, and is bounded (10,000 files, 1 MiB each) with the bound
  stated when it trips.
- Reclamation runs on the coordinator's sweeper: every
  `ReclaimIntervalDefault` (24 hours — the R3 answer, chosen and reasoned
  in fleet.go) the aged-out ephemeral clients' entries are torn down by
  handle and dropped. The spec comes from the walk-up lookup when the
  entry's path is visible, else from `specs.json`, the per-app spec cache
  every allocate writes; an entry whose spec is unavailable is skipped
  with the bound stated, never torn down blind.
- The reaper's allowlist is the spec's `reaper.binaries`, read through
  the `ReapBinaries` seam and defaulting to empty — a spec that names
  nothing signals nothing, which is the safe direction and must remain
  the default. `doctor` reports a spec with port resources and an empty
  allowlist, so the gap is visible rather than silent.

## The drivers (`internal/driver`)

The six-operation contract of `docs/design/03-drivers.md` §2, in code:
`derive`, `probe`, `verify` and `blastRadius` are required; `apply` and
`teardown` are optional, independently of each other. The registry maps a
resource type to its driver, and the conformance suite runs one table of
checks over every driver, so phase 8's `cidr` and `machine` join as rows
rather than as new test files (plan.md §4).

| Driver | Apply | Teardown | What it does |
|---|---|---|---|
| `port` | — | — | derives from the band base via `spec.Resolve` (stride and group, the two forms of `docs/ARCHITECTURE.md` §8.4); probes by binding on loopback with `platform.ProbeBind`, which sets `SO_REUSEADDR` on unix and leaves it unset on Windows; verifies bound/free (naming the holder is phase-5 reaper work); never remediates a held port — the allocator skips the slot and says which one |
| `namespace` | — | yes | derives a project name; probes by label (a hit means a previous teardown was incomplete, never that the slot is taken — so it does not gate allocation); tears down by label, containers then networks then volumes, for the project and every dependent project the spec declares, dependents first, continuing past failures; verifies a pinned `name:` in the governed compose files — the finding that catches the silent attach |
| `state-path` | yes | yes (purge only) | applies by creating the directory and seeding per mode (seeded/empty/shared) with a marker recording when seeding occurred; tears down only when the purge flag is given, with the structural refusal — a purge whose resolved path is the shared source or an ancestor of it is refused, naming the path, checked with `identity.Contains` on the symlink-realised path; verifies exists/writable/seeded-at |

Two rules bind every operation, both structural:

- **A driver that does not create must discover.** The coordinator never
  creates a container, so the namespace driver holds no handle from
  creation and finds the project's objects again by asking the daemon for
  everything carrying `com.docker.compose.project=<name>`. The rule
  generalises to the whole contract (docs/ARCHITECTURE.md §7.1).
- **A teardown handle comes from the registry, never from the working
  tree.** People run `git worktree remove` by hand first, so every handle
  a driver needs is derivable from slot and spec alone, which is what
  makes the registry's denormalised `resources` field sufficient
  (03-drivers.md §2.2).

The docker seam shells out to the `docker` binary — no client library —
honouring `DOCKER_HOST` and friends, and reports unavailable when the
binary is absent or the daemon is unreachable. `unavailable` is a third
result, distinct from success and failure, and the call sites treat it
differently: an unavailable probe does not block allocation (the allocate
result carries `probe_note` stating that the registry was the only check),
whereas an unavailable teardown does block freeing the slot (exit 4).

Sequencing (03-drivers.md §5): apply runs in dependency order derived from
the template references (`Registry.ApplyOrder`; the phase-8 "machine first
among its dependents" rule slots into the same keep predicate), and a
failure part-way through tears the applied resources down in reverse —
the rollback phase 5's `init` leans on. Teardown runs in reverse order,
continuing past a failure and collecting what survived.

The coordinator's teardown core (`Handler.Teardown`) drives the entry
lifecycle: nothing survives → the entry drops and the slot frees; anything
survives → `tearing-down` with a `teardown_note` listing exactly what
survived, and the slot stays held until a re-run frees everything. The
spec is required (the dependent projects and the purge refusal are computed
from it); the host-global reservations come from the ledger at call time,
so a namespace resolving to a reserved name is refused rather than torn
down, naming the reservation (exit 3). Phase 5's `rm` sequences this core
into the release verb.

## Authority rules, as invariants this code holds

The six rules of `docs/ARCHITECTURE.md` §8.6, stated as invariants:

1. **The descriptor beats the registry** — the registry is a cache;
   rebuilding it from descriptors must always be safe. (The descriptor
   emitter writes them; the rebuild lands in phase 6.)
2. **The ledger beats the spec on where ports sit** — allocation feeds the
   ledger's bases into `spec.Resolve`; a committed band would stop two
   developers from differing.
3. **The spec beats everything on shape** — resources, hooks and emission
   are the repo's declaration; the coordinator validates the spec it is
   sent and refuses it whole on any field error.
4. **The recorded path is informational** — resolution never reads the
   entry's path, so a worktree can be moved; `path_visible` only records
   whether the coordinator can stat it.
5. **An entry's slot is authoritative once written** — re-running `init`
   reconciles and rebuilds, it never reallocates.
6. **A client reads any entry and mutates only its own** — the ownership
   check on every mutating call, exit code 3 naming the owner and its
   last-seen time.

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
  has fourteen: `spec validate`, `spec explain`, `guard`, `show`,
  `daemon status`, `daemon install`, `bands list`, `bands suggest`,
  `bands reserve`, `ports scan`, `list`, `doctor`, `reconcile` and
  `clients`.
- `ports scan` reports every LISTEN TCP socket in the coordinator's
  network namespace — port, pid, command, sorted — as facts; it never
  classifies what it finds and never reserves anything. Discovery is
  `platform.AllListeners` (/proc on Linux, one `lsof -F pcn` run where
  /proc is absent, netstat+tasklist on Windows), and a scan that cannot
  see every listener says so, naming the missing tool; a coordinator
  inside a container states that the scan sees the container's
  namespaces only.
- `bands suggest` proposes where the spec's port bases could sit: the
  coordinator computes the required size (slot ceiling × ports per slot)
  and finds the lowest base per resource whose range collides with no
  existing band and no host-global reservation. Group-form resources
  share one base (their derived port sets are disjoint); independent
  resources keep disjoint ranges. The skill chooses only where the bases
  go, never how large they are.
- `bands list` prints the band ledger — the bases each app holds and the
  host-global reservations — and `bands reserve` registers either: an
  app's band from its committed spec (`--base <name>=<port>...`, the spec
  found by the walk-up rule, so the coordinator can compute the required
  size), or a host-global reservation (`--host --port <p>... --note
  <text>`, where the note is required — an unlabelled reservation is one
  nobody can later judge). Both reach the coordinator, so exit 5 is wired
  through `dialCoordinator` like every other coordinator verb.
- `list` is the cross-repo registry, table or `--json`: app, slug, slot,
  state, and the flags — `stale` (the coordinator can stat the recorded
  path and the directory is gone), `unverifiable` (the path exists only
  inside a container the coordinator cannot stat, and is never called
  stale), `reclaimable` (the ephemeral owner has aged out past the
  reclamation interval) and `foreign`. Seed credentials are redacted
  unless `--wide` is given to the owning client — `wt list` from an agent
  container cannot read the host user's credentials (phase-9 exit
  criterion, structural here).
- `doctor` reads everything and writes nothing. Every finding names the
  exact command that fixes it (a hard rule): stale entries fix with
  `wt rm`/`wt reconcile`, a missing descriptor with `wt init`, an
  uninitialised worktree with `wt init` in that directory, an overdue
  reserving entry with `wt reconcile`, a tearing-down entry with `wt rm`
  again, resource drift with `wt init` to rebuild, a band overlap with
  `wt bands reserve` to move one app, an approaching slot ceiling with
  `wt cleanup`/`wt rm`, and a spec whose reaper can signal nothing with
  `reaper.binaries`. The unverifiable marker is an observation, not a
  finding. Doctor exits 0 when it ran; findings are data.
- `reconcile` applies the repair paths to entries rather than to cwd:
  init's four attach outcomes for an entry whose directory is present,
  the reap-teardown-drop sequence for one whose directory is gone, the
  rollback for a reserving entry past its timeout. It acts on the
  caller's own entries plus ephemeral entries whose owner has aged out —
  everything else is reported as skipped, never silently passed over —
  and the coordinator re-checks the eligibility of every ref it receives.
  `--dry-run` previews exactly what the real run does.
- `clients` lists the known clients: identity, kind, the coordinator's
  measured last-seen, how many entries each owns, and which ephemeral
  clients have aged out.
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
- The five environment variables read anywhere are `WT_SOCKET`,
  `WT_HOME` (wtd alone), `WT_STANDALONE`, `WT_CLIENT_EPHEMERAL` (the
  ephemeral declaration, `=1`) and `WT_CLIENT_TOKEN` (the named-container
  token, phase 6). Setting the token together with the ephemeral
  declaration is refused as ambiguous — one names a persistent client,
  the other marks its entries reclaimable.

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
- A port resource's value round-trips through JSON as `float64`, and every
  consumer — the probe, the verify, the reaper's discovery input —
  switches on `int`; `spec.Resolved` normalises port values to int on
  JSON decode, the registry half of the rule the descriptor reader
  applies. A test asserting `float64` after a decode is pinning the old
  bug.
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
  into temp directories and initialise git there. `plain-app` carries the
  full adopted surface committed (skills, hooks, settings, reference doc,
  tripwire, decision record), so copying it yields an already-adopted
  repo; the specless case removes that surface in the test.
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
- The descriptor's emitted YAML quotes map keys as well as values
  (`"api":`), and an absent section emits as `state: {}` — the generated
  reader's YAML subset parser accepts both shapes, and anything else is
  refused with the line number. The subset is pinned by
  `TestGeneratedReaderParsesTheEmittedDescriptor`, which emits a real
  descriptor and parses it with the built generated reader.
- The generated reader is stdlib-only by design and gofmt-clean by
  construction (`go/format` runs inside the generator), because it lands
  in an adopted repo where it must not be the one file that fails that
  repo's fmt check or drags in a dependency.
- The fixture's canonical band is `api=8200` (the base the committed
  artefacts record). Sandboxes are not guaranteed to honour it — this
  container's own harness holds 8201–8208 — so the adoption acceptance
  tests reserve a base from a free range (10000) and assert the
  base-plus-slot derivation rather than a particular slot number: the
  held-port skipping is the allocator's designed behaviour, and the tests
  prove the derivation, not the luck.
- A generated artefact reads its runtime facts from its own managed
  block, never from a constant in its body: regeneration replaces only
  the block, so a filename baked into the tripwire's body would go stale
  while the block refreshed. The tripwire parses the descriptor filename
  and the port resources out of its own `# wt-field:` lines.
- The `.env` writer and the descriptor writer are 0600 for a fresh file
  (both may carry credentials), preserving an existing file's mode; the
  ignore fallback is 0644 — git's own info/exclude is not secret.
- The generated reader tells the primary checkout from a linked worktree
  with git, the repo's own tool — that is the only outside read the
  four-row table needs, and it is what distinguishes "slot 0 keeps the
  committed defaults" from "refuse, naming wt init".
