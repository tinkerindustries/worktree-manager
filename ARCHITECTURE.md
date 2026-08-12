# ARCHITECTURE.md

The as-built structure of this repository. `docs/ARCHITECTURE.md` is the
architecture of record for the system being built — this file is the code
map and changes as the code does; that one does not move. The two files are
deliberately both named `ARCHITECTURE.md` (plan.md §7).

## Packages

One module, `github.com/mrgeoffrich/worktree-manager`, two binaries, two
internal packages:

```
cmd/wt        the client: verb dispatch into internal/cli, then os.Exit
cmd/wtd       the coordinator skeleton: prints a version line, starts nothing
internal/cli  verb dispatch, flag parsing, output, the exit-code error type
internal/spec the wt.yaml schema: parser, validator, template evaluator,
              the walk-up finder, the quoted YAML emitter
```

Import rules, fixed for the whole plan:

- `cmd/wt` → `internal/cli` → `internal/spec`. Nothing else.
- Both binaries link `internal/spec`; later phases add `internal/coord`,
  `internal/store`, `internal/driver` and `internal/fleet`, which are
  coordinator-only and which `cmd/wt` may never import.
- `internal/platform` is the only package that may branch on `GOOS`; it
  arrives when a phase needs a platform branch.
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

## The client (`internal/cli`, `cmd/wt`)

- One exported error type carries the exit code, the message and the
  remedy; `main` maps the code to the process exit status. An error that
  reaches the user with an empty remedy is reported as a defect — the
  "every error names the command that fixes it" rule (plan.md §3) is
  structural, not a habit.
- Exit codes are fixed: 0 success, 1 failure, 2 usage, 3 refused by a
  safety check, 4 required context unavailable, 5 coordinator unreachable.
- Results go to stdout; diagnostics to stderr. `--json` prints exactly one
  JSON object on stdout and nothing else. No colour, no spinner, no
  prompt.
- Verbs are hand-dispatched with one `flag.FlagSet` per verb. This phase
  has exactly two: `spec validate` and `spec explain`.

## Invariants (plan.md §3), as they bind this phase

- Both binaries perform no inference.
- The system refuses rather than partially honouring: a spec with a field
  this schema does not know is refused whole.
- `unavailable` is distinct from failure; an absent `wt.yaml` is reported
  as not adopted (exit 4), never as an obscure error.
- Hooks never run in the coordinator — and this phase runs nothing at all:
  the semantics of hooks and emission land in phases 4 and 5. Only the
  field set is frozen.

## Gotchas

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
