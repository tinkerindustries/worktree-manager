# The primitives the onboarding skill reads

Every verb offers `--json`: exactly one JSON object on stdout, nothing
else. Results go to stdout; diagnostics (notes, bounded-coverage
statements) go to stderr. Nothing is interactive.

## Exit codes (skills and CI branch on these)

| Code | Meaning |
|---|---|
| 0 | success |
| 1 | failure |
| 2 | usage error |
| 3 | refused by a safety check — **stop and ask, never work around it** |
| 4 | required context unavailable (not adopted, tool missing, check cannot run) |
| 5 | coordinator unreachable — the remedy names the start command |

## The verbs

### `wt ports scan [--json]`

What is listening on this machine right now, in the coordinator's network
namespace: `{"listeners":[{"port":..,"pid":..,"command":".."}],"notes":[]}`,
sorted by port. It reports facts and never reserves anything. A scan that
cannot see every listener (the discovery tool is missing, or the
coordinator runs inside a container) says so in `notes` and on stderr.

### `wt bands list [--json]`

The band ledger: `{"bands":[{"app":"..","bases":{"api":8200}}],
"reservations":[{"ports":[5319],"names":[],"note":".."}]}` — the bases
each app holds and the host-global reservations no app may allocate from.

### `wt bands suggest [--spec <file>] [--json]`

Propose where the spec's port bases could sit:
`{"app":"..","suggestions":[{"resource":"api","base":8200,"span":32,"low":8200,"high":8231}],"notes":[]}`.
The coordinator computes the required size from the spec (slot ceiling ×
ports per slot); a suggestion collides with no existing band and no host
reservation. Group-form resources share one base; independent resources
keep disjoint ranges. The skill chooses only where the bases go, never
how large they are.

### `wt bands reserve --base <name>=<port>... [--json]`

Register the repo's band from its committed spec (found by walking up
from cwd). Re-registration replaces in place — a re-run fixes a bad band.
The reply carries the spans. The host form, `wt bands reserve --host
--port <p>... [--name <project>...] --note <text>`, reserves ports no app
may allocate from (and compose project names no teardown may reach); the
note is required and names what holds the range.

### `wt spec validate [path]`

Parse and validate the spec; every refusal names the field and the
reason. Exit 4 outside an adopted repository.

### `wt spec explain --slot N --slug S [--base <name>=<port>]... [--json]`

The resolved resource table for one slot, allocating nothing:
`{"app":"..","slug":"..","slot":1,"resources":{"api":{"type":"port","value":8201}}}`.
Reading slot 1 and slot 2 side by side is the collision check before
anything is built.

### `wt init --description <text> [--slug <s>] [--cwd <dir>]`

Attach to the current tree: allocate, materialise, emit the descriptor
and the `.env` block, run the hooks, activate. Idempotent — re-running
repairs. The slug defaults to the directory basename.

### `wt start [--cwd <dir>]`

Run the bring-up hooks (start, seed, health). No allocation, no
emission, no coordinator — local.

### `wt rm --slug <s> [--cwd <dir>]`

Safety checks (uncommitted changes, unpushed commits, open PR via `gh`),
then reap, tear down and deallocate in the coordinator, then `git
worktree remove`. Exit 3 = a check refused; the remove skill stops and
asks. Exit 4 = a check could not run (`gh` missing); fail closed.

### `wt show [--json] [--cwd <dir>]`

The descriptor read back, local and coordinator-free: the source of every
value the artefacts and briefings state. `{"found":true,
"descriptor":{...}}`, or `{"found":false,"reason":"..."}` with exit 4 in
an un-initialised worktree — the briefing refuses to render on that.

### `wt guard [--json]`

The enforcement hook's engine: classify cwd, contain the file_path,
deny with a reason naming the correct root verbatim. Local, opens no
socket. Exit 3 = denied. Reads the descriptor fresh on every call; the
classification is cached per session when `WT_GUARD_CACHE` is set.

### `wt doctor [--json]`, `wt list [--json]`, `wt reconcile`, `wt clients`

Fleet state. Doctor reads everything and writes nothing; every finding
names the command that fixes it — including the generated-artefact drift
finding, which names the generated file and the field that moved and
fixes with this skill's generate phase.

## The managed block — exact format

Every generated file closes with:

```
# --- managed by wt; edits below are overwritten ---
# wt-field: app=<app>
# wt-field: descriptor=<emit.descriptor.filename>
# wt-field: resources=<names, comma-joined, declaration order>
# wt-field: band <port resource>=<ledger base>
# wt-field: shared=<shared names, comma-joined>
<the file's own fact lines, if any>
# --- end ---
```

Regeneration replaces only the block; hand edits outside it survive. The
markers are the `.env` block's own — one marker convention in the system.
Unbalanced or nested markers refuse the write, naming the line numbers.
Anything a generated file needs at runtime reads its own block (the
tripwire reads the descriptor filename from its block).

## The environment variables

`WT_ENDPOINT` (the coordinator's base URL, e.g. `http://127.0.0.1:7833`),
`WT_HOME` (the store directory — read by both binaries, though a client
reads only `endpoint.json` from it and never the database),
`WT_STANDALONE`, `WT_CLIENT_EPHEMERAL`, `WT_CLIENT_TOKEN` (the container
token), and `WT_GUARD_CACHE`, the per-session classification cache
directory the guard hook sets.

A container is given `WT_ENDPOINT` and `WT_CLIENT_TOKEN` and mounts
nothing. `WT_CLIENT_TOKEN` and `WT_CLIENT_EPHEMERAL=1` compose: the token
admits the client and the flag marks its entries reclaimable.
