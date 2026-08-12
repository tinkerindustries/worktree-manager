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
| Protocol | the wire between `wt` and `wtd` | `internal/protocol` | a message's shape or meaning changes | 1 |
| Registry schema | the shape of every file in the store | `internal/store` | a store file's shape changes | 1 |
| Spec | the `wt.yaml` schema | `internal/spec` | a spec field changes | 1 |

The protocol is carried on the wire as a **range** (`min_version`,
`max_version`), not a single number, so a mismatch names the upgrade in
both directions: a client ahead of the coordinator and a coordinator ahead
of the client each get a usable message. The agreed version is the highest
common one. The negotiation runs on connect, before any request
(`internal/protocol.Agree`; `Handler.Begin`).

The registry schema is carried by every store file's `schema_version`
field. A file written by a newer schema is refused on read with the upgrade
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
| `wt-<version>-darwin-arm64.tar.gz`, `wt-<version>-darwin-amd64.tar.gz` | tar.gz | `wt`, `wtd`, `install.sh`, `README.txt` |
| `wt-<version>-linux-amd64.tar.gz` | tar.gz | `wt`, `wtd`, `install.sh`, `README.txt` |
| `wt-<version>-windows-amd64.zip` | zip | `wt.exe`, `wtd.exe`, `install.ps1`, `README.txt` |
| `SHA256SUMS` | text | the sha256 of every archive, for verification before installing |

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
- `--tcp <addr> --tcp-token <token>` additionally enables the opt-in
  loopback TCP listener (see below). The token is a secret: it is never
  echoed by the installer, the registration file that carries it is
  written 0600 on unix, and it should be generated with a strong random
  source and kept out of shell history (`WT_TCP_TOKEN` exists for that).

### Loopback TCP (opt-in)

A unix socket cannot be shared into a container on every host — Docker
Desktop on macOS shares the workspace through virtiofs, which cannot carry
a live socket, so a container client on a Mac cannot reach the coordinator
over the socket at all. For those hosts the coordinator can additionally
listen on loopback TCP, **off by default**: a coordinator that was not
asked to listen on TCP does not.

- Enable: `install.sh --tcp 127.0.0.1:<port> --tcp-token <token>`, or
  `wt daemon install --tcp ... --tcp-token ...`, or run `wtd --tcp ...` in
  the foreground. The address must be a literal loopback address (the
  surface never reaches the LAN) and the token at least 16 characters.
  Both flags together, or neither: a listener without a token is refused.
- Connect: `WT_SOCKET=tcp://<host>:<port>` plus `WT_CLIENT_TOKEN=<token>`.
  The token is the whole of identity on a TCP connection, because peer
  credentials do not exist there; it is compared in constant time, and a
  missing token and a wrong one get the same refusal. TCP accepts named
  clients only: host identity needs peer credentials, and ephemeral
  clients cannot authenticate over TCP — a disposable container configures
  the token (named) instead. `WT_SOCKET`'s `tcp://` form names the
  coordinator's location exactly as a socket path does, so the token path
  over TCP is the same `WT_CLIENT_TOKEN` machinery the socket uses — one
  identity path, not a second one.
- In a container, dial the host's loopback: on Docker Desktop,
  `tcp://host.docker.internal:<port>`.

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
3. **The store is not touched by a rollback.** `registry.json`,
   `bands.json`, `clients.json` and `specs.json` keep their schema version;
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
| Store | `WT_HOME`, else `$HOME/.wt` — read by `wtd` alone, never by a client |
| Socket | macOS `~/Library/Application Support/wt/sock`; Linux `$XDG_RUNTIME_DIR/wt/sock`; Windows `\\.\pipe\wt`. `WT_SOCKET` overrides everywhere, and its `tcp://host:port` form dials the opt-in loopback TCP listener. The socket sits outside the store so a container mounts the socket alone. |
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
   proves the build, the unit and in-process layers and the named pipe
   instead (`.github/workflows/ci.yml`, the `windows` job), and
   everything unverified is reported `not_run` rather than assumed.
7. A clean machine installs from the release artefacts, adopts a fixture
   repo and passes both acceptance gates by hand; until that run happens,
   the clean-machine criterion is reported unmet.
