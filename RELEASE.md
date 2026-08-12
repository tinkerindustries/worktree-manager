# RELEASE.md

Versioning, the cross-compile matrix, the compatibility policy for the three
independent versions, and how a release is cut.

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
distribution per platform, phase 9). The release version is the git tag,
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

## Where the pieces live

| Piece | Location |
|---|---|
| Store | `WT_HOME`, else `$HOME/.wt` — read by `wtd` alone, never by a client |
| Socket | macOS `~/Library/Application Support/wt/sock`; Linux `$XDG_RUNTIME_DIR/wt/sock`; Windows `\\.\pipe\wt`. `WT_SOCKET` overrides everywhere. The socket sits outside the store so a container mounts the socket alone. |
| Supervisor registration | macOS `~/Library/LaunchAgents/com.mrgeoffrich.wtd.plist` (launchd LaunchAgent; `RunAtLoad` + `KeepAlive` — not socket activation, which needs the C-only `launch_activate_socket`). Linux `~/.config/systemd/user/com.mrgeoffrich.wtd.{service,socket}` (systemd user unit with socket activation; lingering caveat: `loginctl enable-linger <user>`). Windows `%LOCALAPPDATA%\wt\com.mrgeoffrich.wtd.xml` (logon scheduled task, registered via `schtasks`). |

## How a release is cut

1. The full check set passes on the branch:
   `go build ./...`, `go test ./...`, `gofmt -l cmd internal` (empty),
   `go vet ./...`, and the cross-compile matrix above.
2. Tag `v0.x.y` on the merged branch and push the tag.
3. Build the matrix and assemble the distribution: for each platform, the
   two binaries side by side (`wt`, `wtd`), archived per platform. The
   installer (which registers the coordinator with the supervisor and
   starts it) lands with phase 9; until then a release is the archive plus
   the registration commands in `wt daemon install --help`.
4. The macOS suite — including the launchd-backed `daemon status` states
   and a real `daemon install` — runs by hand on a macOS machine, because
   no test in the suite touches the machine's LaunchAgents.
5. The Windows desktop surfaces — a real `schtasks /Create` registration,
   `taskkill`'s escalation against a real desktop process, and the
   coordinator's reachability of Docker Desktop and WSL2 from the logon
   task — run by hand on an interactive Windows desktop; a hosted runner
   proves the build, the unit and in-process layers and the named pipe
   instead (`.github/workflows/ci.yml`, the `windows` job), and
   everything unverified is reported `not_run` rather than assumed.
