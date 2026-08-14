# RELEASE.md

Versioning, the cross-compile matrix, the compatibility policy for the three
independent versions, the distribution and the installer, rollback, and how
a release is cut.

## The three independent versions

Three contracts version independently, and each one follows the same
policy: **refuse rather than partially honour** (plan.md §3). A build that
does not understand a newer contract says so and names the upgrade; it
never applies the part it understands.

| Version | What it versions | Lives in | Bumps when | Current |
|---|---|---|---|---|
| API | the HTTP surface between `wt` and `wtd` | `internal/api` | a route or a payload's shape or meaning changes | 1 |
| Store schema | the shape of the coordinator's database | `internal/store` | a table's shape changes | 1 |
| Spec | the `wt.yaml` schema | `internal/spec` | a spec field changes | 1 |

The API version is carried two ways. The path carries the major (`/v1`),
and `GET /version` — unversioned, so it always answers — reports the
**range** the coordinator supports, so a mismatch names the upgrade in
both directions: a client ahead of the coordinator and a coordinator ahead
of the client each get a usable message rather than a bare 404. The client
checks it before its first RPC. This replaced the hello negotiation the
socket protocol used.

The store schema is carried by `meta.schema_version` in the database. A
database written by a newer schema is refused on read with the upgrade
named (`store.VersionError`); writing it back is never attempted. Migration
is phase 3's work.

The spec version is refused at validation: `spec.Parse` refuses any version
other than the one it knows, naming the version found.

## Binary versioning

The two binaries are built from the one module and ship together (one
distribution per platform). The release version is the git tag,
`v0.x.y`; `wtd` logs it at startup (`cmd/wtd`). A release bumps one or more
of the three contract versions above exactly when that contract's shape
changed — an old binary pair must never be able to half-read a new one.

## Cross-compile matrix

Both binaries build statically, `CGO_ENABLED=0`, for every cell:

| GOOS | GOARCH |
|---|---|
| darwin | arm64, amd64 |
| linux | amd64 |
| windows | amd64 |

```sh
CGO_ENABLED=0 GOOS=darwin GOARCH=arm64 go build ./...
CGO_ENABLED=0 GOOS=darwin GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=linux  GOARCH=amd64 go build ./...
CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build ./...
```

CI runs the matrix on every push (`.github/workflows/ci.yml`).

## The distribution

One distribution per platform containing both binaries, built from the one
module by `dist/build.sh` and published by the release workflow
(`.github/workflows/release.yml`, which runs on every `v*` tag push):

| Artefact | Format | Contains |
|---|---|---|
| `wt-<version>-darwin-arm64.tar.gz`, `wt-<version>-darwin-amd64.tar.gz` | tar.gz | `wt`, `wtd`, `install.sh`, `README.txt`, `SHA256SUMS` |
| `wt-<version>-linux-amd64.tar.gz` | tar.gz | `wt`, `wtd`, `install.sh`, `README.txt`, `SHA256SUMS` |
| `wt-<version>-windows-amd64.zip` | zip | `wt.exe`, `wtd.exe`, `install.ps1`, `README.txt`, `SHA256SUMS` |
| `SHA256SUMS` | text | the sha256 of every archive, for verification before installing |

Every archive carries its own `SHA256SUMS` covering the two binaries it
contains — the installer verifies the binaries against it before copying
anything, and an archive without one is refused, not skipped.

The two binaries always ship side by side — `wt daemon install` finds
`wtd` next to `wt` (`platform.CoordinatorBinaryPath`), which is what makes
a one-command install possible. The zip is built by the stdlib-only
`dist/ziphelper.go`, so cutting a release needs no `zip` binary.

Nothing else ships: no Homebrew tap, no winget manifest, no apt or rpm
package, no coordinator container image, no Developer ID signing or
notarisation, and no `wt upgrade` — the PLAN-SCOPE.md non-goals for
packaging. A release is an archive and an installer, and that is
deliberate.

## Installation

The installer drives `wt daemon install` — the per-platform supervisor
registration (launchd on macOS, the systemd user unit with its paired
socket unit on Linux, the logon scheduled task on Windows) is owned by the
verb, never reimplemented. Installation therefore does two things: copies
the binaries into a prefix, and registers and starts the coordinator.

```sh
tar xzf wt-<version>-<os>-<arch>.tar.gz
cd wt-<version>-<os>-<arch>
./install.sh
```

- Without options, `install.sh` installs into `$HOME/.local/bin` (unix) or
  `%LOCALAPPDATA%\wt\bin` (Windows) and registers the coordinator with the
  real supervisor, starting it. `wt daemon status` afterwards should say
  `running`.
- `--prefix <dir>` (unix) / `-Prefix <dir>` (Windows) is a self-contained
  install: the binaries land in `<dir>/bin` and the supervisor
  registration is written under the same prefix without loading anything —
  the `wt daemon install --prefix` rail, which is also how a temp-prefix
  install is verified.
- `--addr <host:port>` pins the address the coordinator listens on. With
  no `--addr` the installer probes for a free port and pins that, saying
  which — the second user on a machine cannot bind the first's port, and a
  registration naming a port it can never take is a coordinator that never
  starts.
- `--container-token <token>` admits container clients. The two flags are
  independent: a custom address needs no token, and a token needs no
  custom address. The token is a secret — never echoed by the installer,
  the registration that carries it is written 0600 on unix, and it should
  be generated with a strong random source and kept out of shell history
  (`WT_CONTAINER_TOKEN` exists for that).
- Before anything is copied, both binaries are verified against the
  archive's `SHA256SUMS` (sha256sum on Linux, shasum -a 256 on macOS,
  Get-FileHash on Windows). A mismatched digest refuses naming the file;
  a missing manifest refuses too — an archive without one is not a
  distribution this installer built. `--skip-verify` / `-SkipVerify` is
  the deliberate override.
- Every install prints what it is replacing and with what, from the
  binaries' own `--version` lines: `installing wt 0.2.0 (a8e3192)` on a
  first install, `replacing wt 0.2.0 (913f21a) with 0.2.0 (a8e3192)`
  when an older wt is already in the prefix. The binaries are replaced by
  copy-to-temp-then-rename, so a running coordinator keeps executing the
  old image until it exits (Windows ends the logon task first, because
  Windows cannot rename over a running executable).
- `--dry-run` / `-DryRun` prints every action — the verification, the
  paths that would be written, the address and container-token decision,
  the supervisor command — and changes nothing on disk. The last line is
  `dry run: nothing was changed`.
- `--uninstall` / `-Uninstall` reverses an install: it drives `wt daemon
  uninstall` (stop the coordinator, deregister it from the supervisor,
  remove the registration file) and then removes the two binaries from
  the prefix. The store is never removed, and the uninstall refuses —
  exit 3, nothing changed — while the registry still holds entries,
  naming `wt list` and `wt rm`, and equally when the coordinator is
  unreachable and the count cannot be established; `wt daemon uninstall
  --force` is the documented way past either refusal.

### Uninstall

`wt daemon uninstall [--prefix <dir>] [--force] [--json]` reverses `wt
daemon install` — launchctl bootout, `systemctl --user disable --now` and
`schtasks /End` + `/Delete /F` per platform, then the registration file is
removed. Deregistration is client-local and needs no route; the entry
count behind the refusal comes from the coordinator's `list`, because the
store database is never client-readable — only `endpoint.json` is. The
consequence is deliberate: with the coordinator down the client cannot
know what is allocated, so it refuses rather than guessing, and `--force`
is how an operator says they accept the risk. The store
(`~/.wt`, or `WT_HOME`) is never removed — `wt.db` is the only record of
what is allocated on the machine, and deleting it would strand every
container, port and VM the tool has handed out — and the verb prints the
store path and says it was left alone. A registry that still holds
entries refuses with exit 3, naming how many and the commands that
resolve them (`wt list`, then `wt rm`); `--force` overrides the refusal
and is the only way past it. Running it twice is a no-op: nothing
registered the second time.

### Container clients

The coordinator listens on loopback HTTP, so a container reaches it over
the network rather than through a shared file. This is simpler than the
unix socket it replaced: Docker Desktop on macOS shares the workspace
through virtiofs, which cannot carry a live socket, so a container client
on a Mac could not reach the coordinator at all without an opt-in TCP
surface. There is no longer anything to opt into.

Containers remain opt-in at the *identity* layer: absent
`--container-token`, the coordinator admits host clients only.

- Enable: `install.sh --container-token <token>`,
  `wt daemon install --container-token ...`, or run
  `wtd --container-token ...` in the foreground. At least 16 characters,
  and no whitespace — it is carried in unit files and command lines.
- Connect: `WT_ENDPOINT=http://<host>:<port>` plus
  `WT_CLIENT_TOKEN=<token>`. The token is the whole of a container's
  identity; it is compared in constant time, and a missing token and a
  wrong one get the same refusal so the surface is not an oracle.
- A disposable container adds `WT_CLIENT_EPHEMERAL=1`. The two now
  compose: the token admits it and the flag marks its entries
  reclaimable. It calls `POST /v1/session` once and uses the issued
  session id as its bearer thereafter.
- In a container, reach the host's loopback: `--network host` on Linux, or
  `http://host.docker.internal:<port>` on Docker Desktop.

## Rollback

Rolling back a release is re-installing the previous one: the distribution
is self-contained (two static binaries plus the installer), so nothing
between releases is shared except the store — which is the point of the
versioning policy above.

1. **Stop the coordinator.** macOS: `launchctl bootout
   gui/$(id -u)/com.mrgeoffrich.wtd`. Linux: `systemctl --user stop
   com.mrgeoffrich.wtd.socket`. Windows: `schtasks /End /TN
   com.mrgeoffrich.wtd`.
2. **Install the previous release's archive** with its installer, exactly
   as a first install: the new registration overwrites the old unit
   (`wt daemon install` writes the plist / service+socket units / task XML
   in place) and starts the previous coordinator binary.
3. **The store is not touched by a rollback.** `wt.db` keeps its
   `meta.schema_version`;
   if the rolled-back release understands them, nothing else is needed.
   If a release between the two bumped the registry schema, the old binary
   refuses with the upgrade named rather than half-reading the store — the
   versioning policy's refusal, working as designed. In that case the
   rollback is a backup restore of the store, and the refusal message says
   so.
4. **An upgrade that interrupted a worktree's `init` is safe on both
   directions of travel.** A restarting coordinator first recovers every
   `reserving` entry: it tears the entry's resources down by handle and
   drops it (or moves it to `tearing-down` with a note when the teardown
   cannot complete), so a client that was mid-operation re-runs `wt init`
   and allocates fresh. See `internal/coord/recover.go`.

## Two users on one machine (R12)

The coordinator is per-user: it lives in the user's store (`~/.wt`),
listens on the user's socket, and holds one band ledger and one registry.
Two developers on one host therefore hold **two ledgers and two
registries** and can allocate the same port, the same compose project name
or the same state path without either coordinator knowing — the same gap
the source implementations had, and deliberately not solved (PLAN-SCOPE.md:
multi-user support is a named non-goal; `docs/ARCHITECTURE.md` §14.1 R12 is
documented rather than built).

**Consequence.** Two worktrees of the same repo on the same machine, one
per user, can derive the same port from the same band base, and a compose
project name can collide across the two users' stacks. Nothing on the
machine arbitrates between the two coordinators; the host's own OS
isolation (separate sockets, separate stores, separate home directories)
is the only boundary.

**What a user should do.**

- Reserve the ports and compose names a co-resident stack needs with
  `wt bands reserve --host` — that stays per-user too, so agree the
  reservations with the other developer and both of you record them, or
  the second user's allocations can still land on the first user's ports.
- For a shared machine, the practical convention is to divide the port
  space: one user's bands below a chosen boundary, the other's above it,
  written into each user's own ledger with `bands reserve --base`.
- `wt list` and `wt doctor` see only the calling user's registry, so they
  cannot tell you the other user is on a port. `ports scan` sees the whole
  machine's listeners (it is a fact-gathering primitive), which is the one
  place to look when a port behaves as if held.

## Where the pieces live

| Piece | Location |
|---|---|
| Store | `WT_HOME`, else `$HOME/.wt`. The database `wt.db` is coordinator-only; `endpoint.json` in the same directory is the one file a host client reads, which is why `WT_HOME` is now read by both binaries. |
| Endpoint | `127.0.0.1:7833` by default, `--addr` to change it, and whatever `endpoint.json` records. `WT_ENDPOINT` overrides for a client. A container is given the endpoint and a token and mounts nothing. |
| Supervisor registration | macOS `~/Library/LaunchAgents/com.mrgeoffrich.wtd.plist` (launchd LaunchAgent; `RunAtLoad` + `KeepAlive` — not socket activation, which needs the C-only `launch_activate_socket`). Linux `~/.config/systemd/user/com.mrgeoffrich.wtd.{service,socket}` (systemd user unit with socket activation; lingering caveat: `loginctl enable-linger <user>`). Windows `%LOCALAPPDATA%\wt\com.mrgeoffrich.wtd.xml` (logon scheduled task, registered via `schtasks`). |

## How a release is cut

1. The full check set passes on the branch:
   `go build ./...`, `go test ./...`, `gofmt -l cmd internal` (empty),
   `go vet ./...`, and the cross-compile matrix above.
2. Tag `v0.x.y` on the merged branch and push the tag.
3. The release workflow builds the distribution (`dist/build.sh`) and
   attaches the archives and `SHA256SUMS` to the tag's release. Cutting by
   hand is the same one command: `dist/build.sh 0.x.y`.
4. Verify the artefacts: `sha256sum -c SHA256SUMS` inside the release
   assets, then a temp-prefix install on each platform —
   `./install.sh --prefix /tmp/wt-prefix` — and `wt daemon status --prefix`
   on the installed binaries. Nothing is loaded under a prefix.
5. The macOS suite — including the launchd-backed `daemon status` states
   and a real `daemon install` — runs by hand on a macOS machine, because
   no test in the suite touches the machine's LaunchAgents.
6. The Windows desktop surfaces — a real `schtasks /Create` registration,
   `taskkill`'s escalation against a real desktop process, and the
   coordinator's reachability of Docker Desktop and WSL2 from the logon
   task — run by hand on an interactive Windows desktop; a hosted runner
   proves the build and the unit and in-process layers instead
   (`.github/workflows/ci.yml`, the `windows` job), and
   everything unverified is reported `not_run` rather than assumed.
7. A clean machine installs from the release artefacts, adopts a fixture
   repo and passes both acceptance gates by hand; until that run happens,
   the clean-machine criterion is reported unmet.
