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

The driver contract runs the same conformance suite over every driver —
`internal/driver/conformance_test.go`, one table of checks per driver
(`TestDriverConformance`), so phase 8's `cidr` and `machine` join as rows
of the table, not as new test files (plan.md §4). The drivers' behaviour
tests live beside them: `internal/driver/port_test.go`,
`namespace_test.go` (against a fake docker seam), `statepath_test.go`,
and `order_test.go` (the sequencing: apply in dependency order, teardown
in reverse, the reverse-order rollback).

Run one test:

```sh
go test ./internal/spec/ -run TestExplainTablesDisjoint -v
go test ./internal/driver/ -run TestDriverConformance -v
```

### The delivery layer (phase 5)

The three channels of `internal/envfile` and `internal/descriptor`'s write
side, one named test per exit criterion:

- `internal/descriptor/write_test.go`:
  - `TestWriteYAMLQuotesTheSlug` — criterion 1: a descriptor emitted as
    YAML round-trips a worktree slugged `no` (and `on`, `off`, `yes`,
    `y`) without turning it into `false`.
  - `TestWriteLandsAtWorktreeRootUndotted` and `TestWriteIsAtomic` —
    criterion 2: the descriptor lands at the worktree root, undotted, in
    the format `emit.descriptor` declares, via temp-file-fsync-rename with
    no temp file left behind.
  - `TestBuildSharedCarriesBothHalves` and
    `TestBuildSharedRefusesSilentOmission` — criterion 3: the shared block
    carries both halves, and a shared resource with no resolved value or
    no impact text is an error, never a silent omission.
  - `TestWriteRoundTrip`, `TestWriteEmitsThroughTheReader`,
    `TestWriteUnknownFormat`, `TestBuildStateCarriesTheIsolationDecisions`.
- `internal/envfile/envfile_test.go`:
  - `TestManagedKeyAboveBlockIsStripped` and `TestReRunReplacesOnlyTheBlock`
    — criterion 4: a managed key defined above the block comes back with
    one definition, the report names the stripped keys, and a re-run
    replaces only the block so hand edits above and below survive.
  - `TestFirstWriteSeedsFromMainCheckout` and
    `TestFirstWriteSeedsNothingWhenSourceMissing` — criterion 5: a first
    write seeds from the main checkout copying unmanaged content only
    (slot 0's managed values never carry over), and a missing source
    seeds nothing and is not an error.
  - `TestUnbalancedMarkersRefused` — criterion 6: unbalanced or nested
    managed markers refuse the write naming the line numbers, and the
    file is left untouched.
  - `TestCommentedManagedKeyIsNotStripped`, `TestNoSeedWhenDisabled`,
    `TestNewFileModeIsPrivate`.
- `internal/generate/reader_test.go` — the generated reader, proved by
  building it:
  - `TestGeneratedReaderCompilesAndRuns` — criterion 8: the generated
    source is written into a temp Go module, built with `go build` (a
    generator whose output was never built is a generator that does not
    work), and the binary exercises all four rows of the resolution table
    against real git repositories, including the loud failure naming
    `wt init`.
  - `TestGeneratedReaderRefusesNewerVersion` — criterion 9: a descriptor
    whose schema version is newer than the reader understands is refused
    naming the version, on both the cwd and the env-override paths.
  - `TestGeneratedReaderEnvOverride` (—env beats the variable, a missing
    override is never a fall-through, a foreign-app descriptor is refused
    naming both apps), `TestGeneratedReaderShortCircuitsExplicitFlags`
    (B18.2: a broken override cannot take down an explicit call),
    `TestGeneratedReaderParsesTheEmittedDescriptor` (the yaml subset
    parser round-trips exactly what the emitter writes),
    `TestGeneratedReaderParsesJSONFormat`, `TestGeneratedReaderRefuses-
    UnknownFields`, `TestReaderGenerationRefusals`,
    `TestGeneratedReaderFormatIsBaked`.
- `internal/descriptor/ignore_test.go` — the gitignore rule, criterion 7,
  in layer 2 below: it needs real git repositories.

Run one test:

```sh
go test ./internal/envfile/ -run TestManagedKeyAboveBlockIsStripped -v
go test ./internal/generate/ -run TestGeneratedReaderCompilesAndRuns -v
```

This layer needs a Go toolchain (the generated reader is built) and, for
the reader tests, git (the resolution rows are real repositories).

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
- The gitignore rule (criterion 7) is tested against real repositories in
  `internal/descriptor/ignore_test.go`, because the rule exists for a
  mechanism only a real repo exercises — a tracked `.gitignore` shared
  through the branch in a linked worktree:
  `TestEnsureIgnoredWritesInfoExclude` (the line goes to
  `$GIT_COMMON_DIR/info/exclude` and git honours it, tree stays clean),
  `TestEnsureIgnoredIsIdempotent` (a second write changes nothing),
  `TestEnsureIgnoredLeavesTrackedGitignoreUntouched` (a committed
  `.gitignore` is never modified),
  `TestEnsureIgnoredSkipsWhenAdoptionCommittedTheLine` (detected, never
  duplicated), and `TestEnsureIgnoredOneWriteCoversLinkedWorktrees` (one
  write from one worktree covers every linked worktree of the repo).

Run one test:

```sh
go test ./internal/identity/ -run TestClassifyFixtures -v
go test ./internal/identity/ -run TestGuardDeniesWriteToPrimaryCheckout -v
go test ./internal/descriptor/ -run TestEnsureIgnoredWritesInfoExclude -v
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
  `ConnectPeer(peer, kind, token)` runs the hello with an explicit peer,
  so a test can act as a second host client with a different uid — the
  ownership check needs two distinct identities.
- The coordinator's phase-2 tests in `internal/coord/coord_test.go`:
  `TestHarnessFullRequest` (exit criterion 4), the version-refusal
  direction tests, identity assignment (host from peer credentials, named
  token, ephemeral session id), clients.json observation, and
  `TestServerGracefulShutdown`, which runs the real socket server over a
  temp socket to pin the lifecycle (cancellation, in-flight requests,
  socket-file cleanup).
- The coordinator's phase-3 tests in `internal/coord/p3_test.go`, one
  named per exit criterion:
  - `TestConcurrentAllocationsGetDistinctSlots` — criterion 1: two
    goroutines allocate against one app and get different slots.
  - `TestForeignEntryMutationRefused` — criterion 2: a mutating call
    against an entry owned by another client is refused, naming the owner
    and its last-seen time.
  - `TestAllocationRequiresRegisteredBand` — criterion 3: allocation for
    an app with no registered band refuses and names the registration
    command.
  - `TestSlotExhaustionNamesRangeCleanupAndForeignCount` — criterion 4:
    the ceiling message names the range, `wt cleanup`, and how many
    occupied slots the caller cannot free.
  - `TestReservedPortsNeverAllocated` — criterion 5: a port in the
    ledger's reservations is never allocated, and neither is one in the
    spec's `reserved` block.
  - `TestNewerRegistryListsButDoesNotWrite` — criterion 6: a registry
    written by a newer schema version lists but does not write.
  The same file pins the lifecycle (`TestEntryLifecycle`), the ageing of
  `reserving` entries (`TestReservingEntryAgedOutOnTimer`), the phase-4
  probe seam (`TestAllocatorProbeSeam`: held skips a slot, unavailable
  does not block), rule 5's idempotence (`TestAllocateIsIdempotentFor-
  ExistingSlug`), the ledger verbs (`TestBandReserveAndList`,
  `TestBandReserveValidations`), path visibility and secrets.
- The coordinator's phase-4 tests in `internal/coord/p4_test.go`, one
  named per exit criterion: `TestCoordTeardownLeavesTearingDownAndHoldsTheSlot`
  (criterion 3: survivors move the entry to `tearing-down` and the slot is
  not freed), `TestCoordTeardownUnavailableDoesNotFreeTheSlot`,
  `TestCoordTeardownReservedNamespaceRefused` (criterion 4: a namespace
  resolving to a reserved name is refused, naming the reservation),
  `TestCoordTeardownCleanDropsTheEntry` and
  `TestCoordTeardownRequiresSpecAndOwnership`.
- The store's own tests in `internal/store/store_test.go` (root
  resolution, 0700/0600, atomic writes, the schema_version refusal) and
  `internal/store/registry_test.go` (registry and bands round trips, the
  lenient list read of a newer registry, the write-path refusal, the
  never-truncate rule for an unparseable registry).
- The protocol's tests in `internal/protocol/protocol_test.go`: framing
  (one JSON object per newline-terminated message) and `Agree` naming the
  upgrade in both directions.
- `daemon status`'s state machine is driven in
  `internal/cli/daemon_test.go`: all four states with the platform
  observations injected, plus end-to-end states with the real
  observations — a planted registration under `--prefix` and a fake hello
  server on a temp socket (a stand-in for the coordinator that never
  imports coordinator code into the client's tests).
- The bands verbs' client side is driven in `internal/cli/bands_test.go`
  against the same stand-in pattern, extended to serve requests from a
  per-verb handler map: `TestRunBandsListJSON`, `TestRunBandsReserveHost`,
  `TestRunBandsReserveAppMode` (the request carries the committed spec),
  the usage rails, the not-adopted exit 4, a coordinator refusal passing
  through with its own exit code, and both verbs exiting 5 with the
  coordinator stopped.
- The live client-to-coordinator path — `bands reserve` and `bands list`
  against a real foreground `wtd` on a temp socket with a temp `WT_HOME`
  — is exercised by hand in verification, not by a test (the phase-3
  verification run records it).

Run one test:

```sh
go test ./internal/coord/ -run TestHarnessFullRequest -v
go test ./internal/coord/ -run TestConcurrentAllocationsGetDistinctSlots -v
go test ./internal/cli/ -run TestDaemonStatusThreeStates -v
```

This layer needs nothing installed.

## Layer 4 — live acceptance

The exit criteria that need a real docker daemon, run with
`go test -tags acceptance ./...`. Tests carry `//go:build acceptance` so
plain `go test ./...` stays green on a machine with neither docker nor
`gh`. CI runs this layer from phase 6; a hosted macOS runner has no docker
daemon, so it also runs locally on macOS.

What the phase-4 tag covers, with the exit criterion each test proves and
its untagged twin:

| Test | Proves | Untagged twin |
|---|---|---|
| `TestAcceptanceTeardownByLabelWithWorktreeDeleted` (`internal/driver`) | criterion 1: teardown succeeds with the worktree directory deleted first | `TestNamespaceTeardownHandleFromRegistry` |
| `TestAcceptanceVerifyPinnedNameAgainstLiveStack` (`internal/driver`) | criterion 2: verify reports a compose file that pins `name:`, against a real silent attach | `TestNamespaceVerifyPinnedName` |
| `TestAcceptanceTeardownUnavailableMovesEntryToTearingDown` (`internal/coord`) | criterion 3: a teardown that leaves resources behind moves the entry to `tearing-down` and does not free the slot (here: the daemon genuinely unreachable via `DOCKER_HOST`) | `TestCoordTeardownLeavesTearingDownAndHoldsTheSlot`, `TestCoordTeardownUnavailableDoesNotFreeTheSlot` |
| `TestAcceptanceCoordinatorProbeSeesPublishedPort` (`internal/coord`) | criterion 5: the probe run from the coordinator sees a port published by a container | `TestPortProbeFreeAndHeld` |
| `TestAcceptanceTeardownContinuesPastFailure` (`internal/driver`) | criterion 6: teardown continues past a failure and reports everything that survived | `TestNamespaceTeardownContinuesPastFailure`, `TestTeardownAllReverseOrderContinuingPastFailure` |

The tagged probe tests publish a port into the coordinator's own network
namespace (`--network container:<id>`): the test process runs inside a
container whose loopback is not the daemon's publishing loopback, so a
host-published port is invisible from here — the documented limitation of
03-drivers.md §4.1 — while a port a container publishes into the
coordinator's namespace is exactly what revision 2 says the probe sees
(ARCHITECTURE.md §8.4).

Every object the tagged tests create carries a project label unique to the
run (`wtp4-<random>`) and is removed in `t.Cleanup`; a failed test still
leaves nothing behind.

Run the layer:

```sh
go test -tags acceptance ./...
```

This layer needs docker. Criterion 4's refusal and criterion 7's purge
refusal need no docker and run untagged:
`TestNamespaceTeardownReservedNameRefused`,
`TestCoordTeardownReservedNamespaceRefused`,
`TestStatePathPurgeRefusalNamesThePath` (including through a symlink),
and criterion 8's ordering and rollback:
`TestDependencyOrderPinsApplyAndTeardownOrder`,
`TestApplyAllOrderAndRollback`.

## Which layers need what

| Layer | Needs |
|---|---|
| Pure unit | nothing |
| Real-repo fixtures | git, for the real repositories built in `t.TempDir()` |
| In-process coordinator | nothing |
| Live acceptance | docker, and a coordinator process for the gates (phase 5), `gh` for the gates that check PRs (phase 5/6) |

## Command surface

```sh
go test ./...                                   # layers 1–3, no docker, no gh
go test -tags acceptance ./...                  # layer 4 (from phase 6 in CI)
go test ./internal/spec/ -run <TestName> -v     # one test
```

Tests are table-driven, use no framework, and only `t.TempDir()` for
temporary repositories.
