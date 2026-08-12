# Plan scope — worktree-manager, phases 0 to 9

**Status:** frozen baseline for [`plan.md`](plan.md). Not edited by the runner or by any phase agent once the run starts.
**Date:** 2026-08-12
**Covers:** every phase in `plan.md`, 0 through 9.

## What this delivers

Two static Go binaries built from one module. `wtd` runs as a resident per-user
coordinator holding the store at `~/.wt`, allocating slots and resources, and
performing every privileged operation. `wt` runs once per operation, on a host or
inside a container, and reaches the coordinator over a Unix socket; with the
coordinator stopped, `show` and `guard` still work and every other verb exits 5
naming the start command. A repository is adopted by committing a `wt.yaml` at
its root; from there `wt init` attaches to a worktree something else created,
allocates ports, compose namespaces and state paths that do not collide with
another worktree's, writes a descriptor the application reads, and runs the
repo's install, prepull, build, seed and health hooks. Two worktrees of the same
repository run at once, both healthy, with disjoint resource tables, and neither
reaches a co-resident production stack whose ports a person reserved with
`bands reserve --host`. `wt rm` and `wt reconcile` return every resource and drop
the entry, including when the worktree directory was deleted by something that
never called `wt`. A malformed spec is refused at validation naming the field; an
absent spec means the repository is not adopted and the verbs say so; an absent
descriptor makes the generated reader refuse to start naming `wt init`. Both
acceptance gates in `plan.md` §6 pass against synthetic fixture repositories
committed under `testdata/`, on macOS locally and on a Linux CI runner.

## Non-goals

Absolute. A phase that appears to require one of these stops rather than
overriding it.

**Pilot repositories.** `bacio`, `deepseek-harness` and `mini-infra` are out of
this run entirely. Not cloned, not read, not written to, and no spec committed
into any of them. Integration with real repositories is a separate piece of work
after phase 9. Everywhere `plan.md` names one as a pilot or a proof — phase 0's
three fixture specs, phase 5's side-by-side gate, phase 7's adoption, phase 8's
`machine` and `cidr` proof — a synthetic fixture repository under `testdata/`
stands in, purpose-built to carry the resource shapes that phase needs.

**Any repository other than this one.** No clone, no commit, no branch, no pull
request outside `worktree-manager`.

**A `stop` verb.** R9 is answered here rather than at phase 5: `rm` tears down
and there is no way to stop a worktree's resources while keeping its entry.

**Multi-user support.** R12 stands as written. The two-users-on-one-machine gap
is documented at phase 9 and nothing is built for it.

**Readers in any language but Go.** Phase 5 owes the Go descriptor reader.
Python, TypeScript and a published per-language library are all out, including
as a "nearly free" second template.

**Artefacts for agent tools other than Claude Code.** No Cursor, Copilot, Codex
or Gemini equivalents of the skills, hooks and tripwires.

**Interactive output.** No TUI, spinners, progress bars, colour, or any verb that
asks a question. Every verb is non-interactive and offers `--json`.

**Shell completions and man pages.**

**A metrics endpoint, a dashboard, an audit log, or telemetry.** No Prometheus
exposition, no HTTP or web UI over coordinator state, no durable per-call record
beyond the registry and ordinary logging, and nothing that phones home, opt-in
included.

**New third-party dependencies.** No CLI framework, no validation library, no
logging framework, no test framework. Flags, validation, logging and tests are
hand-rolled against the standard library, plus the YAML package the spec needs.
A dependency that would make a phase easier is a reason to stop and ask, not to
add it.

**Generalisation past the five compiled-in drivers and one spec format.** No
external or plugin drivers, no store backend interface with a second
implementation, no gRPC or protobuf replacing newline-delimited JSON, and no
`wt.json` or `wt.toml` behind a format sniffer.

**Package manager channels, code signing, and self-update.** Phase 9 produces one
distribution per platform containing both binaries. No Homebrew tap, winget
manifest, apt or rpm package, or coordinator Docker image; no Developer ID
signing or notarisation; no `wt upgrade` and no binary that updates itself.

**Verification that needs hardware this machine does not have.** Windows and
Linux code is written and cross-compiled, and proved by whatever a CI runner can
prove. Phase 8's "phases 1 to 6 pass on Windows" and phase 9's clean-machine
install are reported as unmet rather than simulated, faked, or worked around by
building infrastructure to reach them.

**Editing `docs/**`.** The nine module documents and `docs/ARCHITECTURE.md` are
the inputs of record. They are read, never amended to match what got built.

**Opportunistic work on earlier phases' code.** A phase changes code or tests an
earlier phase landed only where it cannot work otherwise, and says so in its pull
request. No refactor, rename, dedupe, extracted helper, hardening of an adjacent
path, or performance fix that its own brief did not require. This applies most at
the late phases, where the branch has accumulated several phases of other agents'
code and cleaning it up reads as the responsible finish.

**Everything the design already ruled out.** Docker-in-Docker; isolating
content-addressed caches; serialising instead of isolating; auto-creating an
environment on first mutation; named-profile indirection; learning any repo's
domain; creating worktrees; translating paths between filesystem namespaces.

## Fences

Little code exists yet, so most of these are contracts rather than paths. Both
kinds are checkable.

- `docs/**` — read-only for the whole run. These documents are what the work is
  checked against; a phase that edits one removes the evidence it drifted.
- `PLAN-SCOPE.md` — this file. Frozen from the moment the run starts. Changing it
  requires stopping the run.
- `plan.md` — no phase agent edits it. Only the orchestrator's between-phase
  reconciliation writes here.
- Any path outside this repository's working tree, other than the machine state
  named below. In particular no other git repository, on disk or remote.
- Containers, networks, volumes, ports and VMs the run did not create. Teardown
  by label is scoped to the fixture projects the run started. This is the one
  operation that could reach a co-resident production stack, and it holds a real
  docker socket while doing it.
- Existing `~/.wt` registry entries and band reservations that the run did not
  create. The run may create the real store and write to it, but it reclaims only
  its own entries.
- Other LaunchAgents. The run may register and start `wtd`; nothing else under
  `~/Library/LaunchAgents` is touched.
- Exit codes 0 to 5 keep the meanings in `plan.md` §3, with 5 reserved for
  coordinator unreachable. Skills and CI branch on these.
- The spec is `wt.yaml` at the repository root, found by walking up from cwd to
  the worktree root and stopping. Name, location and resolution rule are fixed at
  phase 0 and constant everywhere, because the generated reader has to find it
  with nothing installed.
- `WT_HOME` is read by `wtd` alone and is a store-path override. No client reads
  it, and it is never a container mount point.
- No lock file, no generation counter, no view identity, no view scoping.
  Concurrency is one writer in the coordinator.
- `internal/platform` is the only package permitted to branch on `GOOS`.
- The three versioned contracts — spec schema, protocol, registry schema — may
  gain fields and messages freely. Changing or removing anything already frozen
  requires bumping that contract's version and handling the older one; a phase
  that would change one silently stops instead.
- Hooks never run in the coordinator, and neither binary performs inference.
- Git history on the integration branch is not rewritten.

## In scope

- The Go module `github.com/mrgeoffrich/worktree-manager`, `cmd/wt`, `cmd/wtd`,
  and internal packages per module.
- Verbs: `init`, `start`, `show`, `guard`, `rm`, `list`, `doctor`, `reconcile`,
  `cleanup`, `clients`, `spec validate`, `spec explain`, `bands list`,
  `bands suggest`, `bands reserve`, `ports scan`, `daemon status`,
  `daemon install`. Adding a verb beyond this list is scope drift.
- Drivers: `port`, `namespace`, `cidr`, `state-path`, `machine`.
- Generated per adopted repo: the `worktree-create` and `worktree-remove` skills,
  the `SessionStart` tripwire, the opt-in `PreToolUse` guard hook, the reference
  doc with its `CLAUDE.md` tripwire, and the descriptor reader.
- Synthetic fixture repositories under `testdata/`, and the fixture specs under
  `testdata/specs/`.
- `.github/workflows` for build, lint, test, cross-compilation to macOS, Linux
  and Windows, and the two gates on a Linux runner from phase 6.
- The five repository documents in `plan.md` §7: `CLAUDE.md`, root
  `ARCHITECTURE.md`, `TESTING.md`, `RELEASE.md`, and `HOSTING.md` marked
  unwarranted.
- Platforms: macOS and Linux primary, Windows supported with a weaker reaper and
  permission model. A container is Linux with reduced capabilities.

Repo-specific behaviour goes in one of three seams and nowhere else: a driver
allocates a resource type, a hook is a command string the client sequences
without understanding, an emitter carries an allocation to the application.
Policy is spec configuration. Neither binary branches on repo identity.

## Definition of done

Nothing runs today: the repository holds documents only. Phase 0 makes these
commands exist, and they are what the whole run is judged on before the final
pull request.

- `go build ./...` succeeds.
- `go test ./...` passes.
- `gofmt -l cmd internal` prints nothing.
- `go vet ./...` is clean.
- `GOOS=linux GOARCH=amd64 go build ./...` and `GOOS=windows GOARCH=amd64 go build ./...`
  both succeed with `CGO_ENABLED=0`.
- Acceptance gate 1: two worktrees of a fixture repository run side by side, both
  healthy, resource tables disjoint, both torn down leaving no containers, no
  volumes and no registry entries, and `wt doctor` clean afterwards.
- Acceptance gate 2: a container client and a host client allocate against the
  same app concurrently, and the container's slot is unavailable to the host.
- With `wtd` stopped, `wt show` and `wt guard` still work and every other verb
  exits 5 naming the start command.
- `wt spec validate` against a wt.yaml with a template cycle, an unknown
  cross-resource reference, and an over-long resolved name each exit non-zero
  naming the field and the reason.
- In a repository with no `wt.yaml`, the verbs report it as not adopted rather
  than failing obscurely.
- In a linked worktree of an adopted repository with no descriptor, the generated
  reader refuses to start and names `wt init`.
- Deleting a fixture worktree's directory by hand and running `wt reconcile`
  tears its resources down and drops the entry.

Exit criteria in `plan.md` that need a real Windows machine or a clean machine are
reported unmet, with what was verified in their place.

## The drift test

Three questions. Any yes means the scope moved and this file has to change first,
which means stopping the run.

1. Does the work add a verb, a driver, a generated artefact, a platform, or a
   third-party dependency?
2. Does it put repo-specific knowledge anywhere other than a driver, a hook, or
   an emitter?
3. Does it reopen a closed decision, or change a frozen contract without bumping
   that contract's version?

These are not drift and need no amendment: a new spec field, a new `doctor`
finding, a driver's implementation for a platform it already claims, a failure
message, a test, or an answer to one of the twelve risks in `plan.md` §8.

## Decisions that are closed

Reopening one is a scope change, not a design discussion.

| | Decision |
|---|---|
| 1 | The coordinator is required. There is no local fallback for any verb but `show` and `guard`. |
| 2 | There is no `create` verb. `init` attaches to a tree something else made. |
| 3 | A client mutates only the entries it created. |
| 4 | One writer, so no lock file and no generation counter. |
| 5 | No view identity. `owner`, `ephemeral` and `path_visible` replaced it. |
| 6 | The committed spec is the adoption signal. An absent descriptor means nothing on its own. |
| 7 | Slot 0 is the primary checkout, never allocated and never managed. |
| 8 | Neither binary performs inference. All judgment lives in the onboarding skill. |
| 9 | Hooks never run in the coordinator. |
| 10 | The spec is `wt.yaml` at the repository root. The descriptor's name and format are the repo's choice; the spec's are not. |
| 11 | There is no `stop` verb. |
| 12 | The pilot repositories are out. Synthetic fixtures prove every phase. |

Deleted by revision 2 and not to return: `wt schedule install`, `WT_HOME` as a
container mount point, the state directory bind-mounted into a container, the
host docker socket mounted into a client.
