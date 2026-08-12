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
`order_test.go` (the sequencing: apply in dependency order with machine
forced first, teardown in reverse, the reverse-order rollback),
`cidr_test.go` (the /22-per-slot arithmetic out of 172.30.0.0/16, the
overlap probe against the fake docker, the loud shared-pool fallback and
the fail mode) and `machine_test.go` — the phase-8 capacity guard,
ordering, keep flag and bypass command, proved against a fake
`platform.MachineRunner` (the live Colima path cannot run on this Linux
machine and is reported not_run; the seam's parsing half is pinned in
`internal/platform/machine_test.go`).

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

### The adoption layer (phase 7)

The onboarding skill's exit criteria, in the layer each belongs to:

- `internal/managed/managed_test.go` — the block convention:
  `TestReplacePreservesHandEditsOutsideTheBlock` (criterion 4's core:
  regeneration replaces only the block), `TestParseExtractsFieldRecords`
  (the `# wt-field:` records doctor compares), `TestReplaceRefusesUnbalancedMarkers`
  (the refusal names the line numbers), `TestReplaceAppendsBlockToMarkerlessFile`
  (the CLAUDE.md tripwire joining a repo's own text, idempotently).
- `internal/artefact/artefact_test.go` — the generated surface:
  `TestRenderProducesEveryArtefact` (all seven files, the blocks carry
  the field records, the hooks are executable, the PreToolUse entry only
  with the developer's confirmation), `TestApplyRegeneratePreservesHandEdits`
  (criterion 4 end to end through `Apply`), `TestRemoveSkillStopsAndAsksOnExit3`
  (criterion 5: the skill's rule is to stop and ask on exit 3 and
  `--force` never appears as a remedy), `TestBriefingRefusesWithDescriptorMissing`
  (criterion 6: the briefing refuses to render, naming `wt init`),
  `TestTripwireScriptRuntime` (the rendered SessionStart hook against a
  real git worktree: the one-line summary from a descriptor, the sentence
  naming `wt init` without one, silence in the primary checkout — reading
  the facts from its own managed block), `TestGuardHookScriptRuntime`
  (the rendered PreToolUse hook against a fake wt: deny → exit 2 with the
  structured reason, allow → exit 0, missing wt → fail open and say so,
  `WT_GUARD_CACHE` exported).
- `internal/identity/guard_cache_test.go` — the plan.md §9.2 decision:
  `TestGuardCacheReusesThenDropsOnRemoval` (a hit is reused without
  rewriting the entry; a worktree removed mid-session drops the cache,
  reclassifies, and the one-time note reaches the session),
  `TestGuardCachedVerdictCarriesTheCache`.
- `internal/coord/onboard_test.go` — the two primitives:
  `TestPortsScanReportsFacts` (the scan sees the test's own listener as a
  fact, sorted by port), `TestBandsSuggestFindsLowestFreeBase`,
  `TestBandsSuggestSkipsBandsAndReservations`, `TestBandsSuggestGroupSharesOneBase`
  (the group form: one base, interleaved port sets),
  `TestBandsSuggestIndependentStridesGetDisjointBases`,
  `TestBandsSuggestRefusesInvalidSpec`.
- `internal/coord/drift_test.go` — criterion 3: `TestDoctorReportsMovedBandNamesFileAndField`
  (doctor clean, band moves, the finding names the generated file and the
  field that moved), `TestDoctorReportsRenamedResource`.
- `internal/cli/ports_test.go` and the suggest/reserve cases there — the
  verbs' client side: `--json` shapes, the notes to stderr, exit 4 when
  discovery is unavailable, exit 5 with the coordinator stopped, exit 4
  outside an adopted repository.
- **The adoption layer proper** — `internal/cli/adoption_test.go`,
  deliberately **untagged**: plain-app uses no compose, so the whole
  skill flow runs under plain `go test ./...` with no docker, against a
  real coordinator on a temp socket and real git worktrees (the fake gh
  answers the no-PR contract):
  - `TestAcceptancePlainAppAdoptedThroughTheSkill` — exit criterion 1:
    the adopted fixture goes through the skill, proving state-path's
    three seed modes and the `default: shared` resource end to end — the
    db snapshot has the shared source's content, the cache is empty, the
    shared_db is the same path in both worktrees with `isolated: false`
    and a place in the descriptor's shared block, the `.env` seeded from
    the main checkout's unmanaged content with the managed keys from the
    worktree's own allocation — two worktrees side by side, both healthy
    at once, disjoint tables, torn down with the reaper stopping the
    servers, `wt doctor` clean; regeneration preserving a hand edit
    (criterion 4 end to end) and the briefing rendering from `wt show`
    values are exercised along the way.
  - `TestAcceptanceSkillEightPhasesOnSpeclessFixture` — exit criterion 2:
    a copy of plain-app with the spec and the adopted surface removed
    goes through all eight phases — audit (committed default ports,
    `$HOME` paths, what is listening), policy (the decision record),
    bands (suggest, confirm, reserve), spec (written, validated, both
    slot tables read and disjoint), generate (the artefacts, settings and
    ignore line), patch (the serving entry point), prove (two worktrees,
    both healthy, torn down, doctor clean), import (nothing pre-existed,
    stated) — ending with two worktrees side by side.

Run one test:

```sh
go test ./internal/managed/ -run TestReplacePreservesHandEditsOutsideTheBlock -v
go test ./internal/coord/ -run TestDoctorReportsMovedBandNamesFileAndField -v
go test ./internal/cli/ -run TestAcceptancePlainAppAdoptedThroughTheSkill -v
```

The adoption layer needs git and a Go toolchain (the build hook compiles
the fixture's server). The band the adoption tests use is 10000, chosen
from a range free on the machine the tests run on: the fixture's
canonical 8200 can collide with whatever else listens on the machine
(this sandbox runs its own service on 8080, and an interrupted run can
leak a worktree's server), and the held-port skipping is the allocator's
designed behaviour — the tests assert the base-plus-slot derivation,
never a particular slot number.

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
- The coordinator's phase-5 tests in `internal/coord/p5_test.go`, one
  named per rail and criterion: the materialise verb
  (`TestCoordMaterialiseAppliesInDependencyOrder`,
  `TestCoordMaterialiseFailureCleanRollbackLeavesEntryReleasable`,
  `TestCoordMaterialiseFailureWithFailedRollbackMovesToTearingDown`,
  `TestCoordMaterialiseRequiresOwnership`), the rebuild-from-descriptor
  and rule-5 allocate halves (`TestAllocateRebuildsFromSlotHint`,
  `TestAllocateSlotHintHeldSlotRefused`,
  `TestAllocateResultCarriesExistedPathAndShared`), the rm verb
  (`TestRmVerbEntryNotFoundIsDataOutcome`,
  `TestRmVerbDryRunPreviewsWithoutTeardown`,
  `TestRmVerbSequencesReapThenTeardownAndDropsEntry`,
  `TestRmVerbForeignEntryRefused`, `TestRmVerbSurvivorsHoldTheSlot`), and
  the reaper's rails against real processes and real discovery
  (`TestCoordReapSignalsOnlyNamedBinaries`,
  `TestCoordReapNeverSignalsANonSpecHolder`,
  `TestCoordReapNeverTouchesReservedPorts` — both the spec's reserved
  block and the ledger's host-global reservations,
  `TestCoordReapInContainerReportsUnavailable`,
  `TestCoordReapDryRunListsWithoutSignalling`,
  `TestCoordReapKeepProcessesOptsOut`).
- The coordinator's phase-6 tests in `internal/coord/fleet_test.go`, one
  named per marker, criterion and rail:
  - `TestListStaleAndUnverifiableStayDistinct` — exit criterion 4's
    marker half: an invisible path is unverifiable, never stale, and a
    visible gone directory is stale, never unverifiable;
  - `TestListForeignAndReclaimableMarkers` and
    `TestListSecretsRedactedExceptOwnerWide` — foreign/reclaimable
    markers, and the security rail: seed credentials are served to the
    owning client alone and only under `--wide`;
  - `TestDoctorFindingsEachNameACommand` — exit criterion 5: every
    `06-fleet.md` §4 finding this phase produces is made by a fixture and
    names the exact command that fixes it (stale, uninitialised worktree,
    .env duplicate, port drift, band overlap, slot ceiling, reaper
    allowlist), plus `TestDoctorReservingAndTearingDownFindings`,
    `TestDoctorDescriptorMissingAndSpecMissing`,
    `TestDoctorUnverifiableIsObservationNotFinding`,
    `TestDoctorReportsNewerRegistry`;
  - `TestDoctorWritesNothing` — the whole doctor contract: the store's
    files are byte-identical before and after a run that produced
    findings;
  - `TestReconcileTearsDownStaleEntryAndDropsIt` — exit criterion 3's
    coordinator half — and `TestReconcileEligibility` — a live client's
    entry is skipped naming the owner, an aged-out ephemeral owner's is
    torn down by handle, a reserving entry is rolled back only past its
    timeout;
  - `TestClientsListShowsEntriesAndAgedOut`,
    `TestReclaimEphemeralReclaimsAgedOutOnly` (the spec-cache fallback
    for invisible paths) and `TestReclaimEphemeralSkipsWithoutSpec` (a
    skip stated, never a blind teardown);
  - `TestReapBinariesDefaultsToSpecField` and `TestSpecCacheRoundTrip`.
- The coordinator's phase-8 machine and cidr wiring in
  `internal/coord/machine_test.go` (`TestCoordMaterialiseCapacityRefusalExits3`,
  `TestCoordMaterialiseCapacityReRunAllowed`, `TestCoordRmKeepVMLeavesTheInstanceUp`)
  and `internal/coord/cidr_test.go` (`TestAllocateCIDRFallbackIsLoud`,
  `TestAllocateCIDRProbeHoldsOverlappingSlot`), the doctor capacity
  finding (`TestDoctorMachineCapacityFinding`), and the scheduled cleanup
  sweep in `internal/coord/sweep_test.go` (`TestSweepCleanupGhUnavailableCleansNothing`,
  `TestSweepCleanupCleansMergedCleanOwnEntry`,
  `TestSweepCleanupSkipsEverySkipLogged`,
  `TestSweepCleanupForeignEphemeralNeverAdopted` — the sweep never adopts
  a view).
- The client's phase-8 cleanup verb in `internal/cli/cleanup_test.go`
  against a real in-process coordinator and real git worktrees with gh
  faked on PATH: `TestRunCleanupDryRunPreviewsExactlyWhatTheRealRunDoes`
  (exit criterion 2), `TestRunCleanupGhMissingCleansNothingExits4` and
  `TestRunCleanupGhUnauthenticatedCleansNothingExits4` (gh missing or
  unauthenticated cleans nothing, exit 4),
  `TestRunCleanupSkipsAndAppliesFullChecksEvenWhenMerged` (the full rm
  safety checks apply even when the PR is merged; unverifiable entries
  are skipped), `TestRunCleanupSkipsUnpushedBranch`.
- The client's phase-6 verbs in `internal/cli/fleet_test.go`:
  `TestRunListJSONAndTable`, `TestRunDoctorJSONAndClean`,
  `TestRunClientsJSONAndTable`, `TestRunReconcileDryRunAndReal` (dry-run
  previews and sends nothing), `TestRunReconcileTearsDownDeletedWorktree`
  (exit criterion 3 end to end without docker: a real in-process
  coordinator, a real git worktree deleted by hand, reconcile from the
  main checkout dropping the entry, doctor clean afterwards),
  `TestRunListUnreachableExitsFive` and `TestRunReconcileNotAdopted`.
- The reaper's platform half in `internal/platform/listeners_test.go`
  against the real platform: discovery finds a real listener
  (`TestListenersFindsARealListener`,
  `TestListenersMultiplePortsFindsEachHolder`), the TERM-then-KILL
  escalation and the process-group kill
  (`TestSignalTermAndKill`, `TestKillGroupKillsTheWholeTree`).
  On Linux, discovery reads `/proc/net/tcp` plus `/proc/<pid>/fd` —
  the design table's Linux alternative, which is also what makes the
  reaper work where lsof is a busybox applet that ignores its flags;
  macOS uses the platform's lsof.
- The client's lifecycle verbs in `internal/cli/`, driven through
  `cli.Run` against real git worktrees (the fixture layer's builder) and
  the recording fake coordinator (`recordingCoord` in init_test.go) plus
  a fake `gh` on PATH (the real remote service is not part of the unit
  layer; the no-PR contract is exit 1 with "no pull requests found",
  exactly as real gh answers):
  - the hook sequencer in `hooks_test.go`: resolution over resources and
    sticky params, the run contract (cwd, environment, stderr, non-zero
    stops), `--dry-run`, the timeout killing the process group, health
    polling with the tail dump on timeout, and the missing-seed-
    credentials warning-and-skip;
  - `init` in `init_test.go`: the happy path through all seven steps
    (`TestInitAllocatesEmitsActivates`), the required description
    (`TestInitRequiresDescription`), the primary-checkout refusal
    (`TestInitRefusesPrimaryCheckout`), the nested-worktree refusal
    (`TestInitRefusesNestedWorktree`, real clone inside a worktree), the
    slug-collision stop (`TestInitSlugCollisionWithDifferentPathStops`),
    the rollback paths (`TestInitMaterialiseFailureReleasesTheEntry`,
    `TestInitMaterialiseFailureWithFailedRollbackKeepsEntry`,
    `TestInitHealthFailureKeepsTheWorktreeAllocated` — exit criterion 4
    without docker), the two repair outcomes
    (`TestInitReemitsDescriptorFromEntry`,
    `TestInitRebuildsRegistryFromDescriptor` — the slot hint on the
    wire), and `--dry-run`;
  - `start` in `start_test.go`: the bring-up hooks from the descriptor,
    the no-descriptor refusal naming `wt init`, never contacting the
    coordinator, the health failure saying what failed, `--dry-run`, and
    the sticky-param persistence;
  - `rm` in `rm_test.go`, one test per safety check and partial state:
    `TestRmStopsOnUncommittedChanges`, `TestRmStopsOnUnpushedCommits`,
    `TestRmStopsOnAbsentUpstream`, `TestRmStopsOnOpenPR`,
    `TestRmGhMissingExitsFour` (exit criterion 6), `TestRmGhUnauthenticatedExitsFour`,
    `TestRmNoPRPasses`, `TestRmRefusesStandingInTheTarget`,
    `TestRmBySlugWithDirectoryDeleted` (exit criterion 5),
    `TestRmDryRunPreviewsAndChangesNothing`,
    `TestRmDirectoryPresentNoEntry`, `TestRmNeitherIsAMessageAndAStop`,
    `TestRmRequiresATarget`, `TestRmCoordinatorUnreachableExitsFive`;
  - the nested-worktree detection itself against real repositories in
    `internal/identity/nested_test.go` (`TestNestedInsideFindsACloneInsideAWorktree`,
    `TestNestedInsideNegative`, `TestNestedInsideThroughSymlink`).
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
| `TestAcceptanceTwoWorktreesSideBySide` (`internal/cli`) | phase-5 criteria 1, 2 and 5, and phase-6 gate 1 in full: two compose-app worktrees run side by side, both healthy at once, disjoint resource tables, neither reaching the production stack; both tear down cleanly leaving no containers, no volumes and no registry entries; rm by slug with the directory already deleted; `wt doctor` clean afterwards | the untagged twins in `internal/cli/init_test.go`, `start_test.go`, `rm_test.go` and `internal/coord/p5_test.go` |
| `TestAcceptanceGate2ContainerAndHostAllocate` (`internal/cli`) | phase-6 gate 2: a real docker container running the real wt binary (built `CGO_ENABLED=0`) allocates against the same app as a host client, and the container's slot is unavailable to the host; the container's entry is owned by its `WT_CLIENT_TOKEN` identity, the container removes its own entry, and the host then reuses the freed slot | `TestListForeignAndReclaimableMarkers`, `TestReconcileEligibility` (`internal/coord/fleet_test.go`) |

The gates run the real client (`cli.Run`) against a real coordinator
(`coord.Server` on a temp socket, the phase-6 CI arrangement) over real
git worktrees and real docker. The one test double is `gh`: the gates'
branches live on a local bare remote, and the real gh cannot answer "is
there a PR" for a repository that is not on GitHub — a fake gh on PATH
answers the no-PR contract the check reads (exit 1, "no pull requests
found"), exactly as real gh does for a branch without a PR. The
fail-closed side (missing or unauthenticated gh → exit 4) is exercised
untagged in `rm_test.go`. Every container, network and volume the gates
create carries the worktrees' project labels and is removed before the
test returns; nothing with another label is ever touched.

Gate 2's container client runs the real wt binary inside a docker
container with the coordinator's socket shared into it and
`WT_CLIENT_TOKEN` set. The file sharing is chosen by a runtime probe
(`containerTransport` in `acceptance_test.go`): bind mounts of the
workspace paths on a CI runner, where the daemon and the workspace share
one filesystem; or the docker volume mounted at `/data` on a dev machine
whose daemon cannot bind-mount the workspace and whose virtiofs mounts
cannot carry live unix sockets. A listener-socket dial from inside the
container proves the chosen transport before the gate runs; without a
working transport the gate skips with the reason stated. On macOS the
gate runs locally under Docker Desktop, whose `/tmp` file-sharing root is
where the transport's bind-mode base lives.

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
| Adoption layer (phase 7, untagged) | git and a Go toolchain, no docker |
| Live acceptance | docker, and a coordinator process for the gates (phase 5), `gh` for the gates that check PRs (phase 5/6) |

## The Windows layer (phase 8b)

The `windows` job in CI (`windows-latest`) runs `go build ./...` and
`go test ./...` natively — the parts of the Windows surface a hosted
runner can prove (plan.md §5, phase 8's exit criteria):

- the named pipe carrying a full request: `TestPipeCarriesAHello` and
  the already-listening refusal, the pipe-owner identity and the
  SID/SDDL machinery (`internal/platform/pipe_windows_test.go`), and a
  full hello + ping round trip through a real `coord.Server` on a temp
  pipe (`internal/coord/pipe_integration_test.go`);
- the store ACL: `TestEnsurePrivateDirWindowsACL` (set + read-back) and
  `TestEnsurePrivateDirRefusesWhenACLUnsettable` (the refusal, driven
  through the `windowsSetACL` seam), plus `TestAtomicWriteWindows`;
- the logon-task registration: the UTF-16 task XML's shape and the
  status parser (`internal/platform/task_windows_test.go`) — the
  `schtasks /Create` itself is never run on the machine (prefix rail);
- path realisation: long/deep paths, symlinks, and the mapped-drive
  equivalence proven with `subst` (`internal/platform/
  realpath_windows_test.go`), skipping with the reason where `subst`
  cannot run.

Not provable on a runner, and reported `not_run` rather than simulated:
registering the logon task against a real Task Scheduler, `taskkill`'s
escalation against a real desktop process, Docker Desktop pipe and WSL2
reachability from the task. The unix-only reaper tests live in
`internal/coord/reap_unix_test.go` (`//go:build darwin || linux`) for
this reason.

## Command surface

```sh
go test ./...                                   # layers 1–3, no docker, no gh
go test -tags acceptance ./...                  # layer 4 (from phase 6 in CI)
go test ./internal/spec/ -run <TestName> -v     # one test
```

Tests are table-driven, use no framework, and only `t.TempDir()` for
temporary repositories.
