# RELEASE.md

How a version is cut, what it produces, how it is installed on a host and
in a container, and how to get back to the previous one.

Nothing here is deployed: the coordinator installs onto a developer's
machine. [`HOSTING.md`](HOSTING.md) says why that file is empty.

## Versioning

The version is a **semantic version** ([semver.org](https://semver.org)
2.0.0) and the release tag is that version with a `v` in front — `v0.2.0`,
`v1.0.0-rc.1`. Two places refuse anything else, so a misnamed tag never
reaches a binary: the release workflow checks the tag before it builds, and
`dist/build.sh` checks the version it is handed.

| Bump | When |
|---|---|
| MAJOR | a documented verb, flag, exit code, descriptor field or spec field is removed or changes meaning |
| MINOR | a verb, flag or spec field is added, backwards compatibly |
| PATCH | a fix that changes no interface |
| `-rc.N` etc. | a release cut for testing; published as a GitHub prerelease, so it is never "latest" |

While MAJOR is 0, breaking changes ride the MINOR, as semver allows. The
three contract versions below are what actually gate compatibility.

Both binaries carry the version and the commit, stamped by `dist/build.sh`
with `-ldflags -X`; `wt --version` and `wtd --version` print them and `wtd`
logs them at startup. With no version argument `build.sh` derives one from
the checkout, and what it derives is always semver: an exact tag is that
release (`0.2.0`), a commit past one is `0.2.0-dev.7+gabc1234`, and a
checkout with no tags is `0.0.0-dev+g<commit>`.

The menu bar app is the exception: `macapp/Resources/Info.plist` carries
`CFBundleShortVersionString` and `CFBundleVersion` as literals and nothing
derives them. `bundle.sh` copies the file as it stands and `sign.sh
--version` only names the zip, so both are bumped by hand in the commit
that gets tagged. The app reports its own version and `wt`'s separately in
its menu, which is where a missed bump shows up.

### The three contract versions

These version independently of the release, and each follows the same
policy: **refuse rather than partially honour**. A build that does not
understand a newer contract says so and names the upgrade; it never applies
the part it understands.

| Contract | Versions | Carried by | Bumps when |
|---|---|---|---|
| API | the HTTP surface between `wt` and `wtd` | the `/v1` path, plus `GET /version` reporting the supported range | a route or payload changes shape or meaning |
| Store schema | the coordinator's database | `meta.schema_version` in `wt.db` | a table changes shape |
| Spec | the `wt.yaml` schema | the spec's own version field | a spec field changes |

`GET /version` is unversioned so it always answers, and it reports a
*range*, which is what lets a mismatch name the upgrade in both directions
rather than returning a bare 404. The client checks it before its first
RPC. A newer database is refused on read (`store.VersionError`) and never
written back. A spec version `spec.Parse` does not know is refused at
validation, naming the version found.

A release bumps a contract exactly when that contract's shape changed. An
old binary pair must never be able to half-read a new one.

## Cutting a release

**The trigger is pushing the tag.** Nothing else publishes.

```sh
# 1. bump macapp/Resources/Info.plist — CFBundleShortVersionString to the
#    new version and CFBundleVersion to the next integer. Nothing stamps
#    them, so the app reports the previous release until you do.

# 2. the full gate set passes on main
go build ./... && go test -race ./... && go vet ./... && staticcheck ./...
gofmt -l cmd internal          # must print nothing
for p in darwin/arm64 darwin/amd64 linux/amd64 linux/arm64 windows/amd64; do
  CGO_ENABLED=0 GOOS="${p%/*}" GOARCH="${p#*/}" go build ./... || echo "FAILED $p"
done

# the two generated surfaces are committed, and CI fails on drift in
# either; regenerate and check the tree is unchanged
go run ./cmd/wtgen
go run github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1 generate
git status --porcelain   # must print nothing

# 3. tag and push — this runs .github/workflows/release.yml
git tag -a v0.2.0 -m "Worktree Manager 0.2.0"
git push origin v0.2.0
```

The workflow runs `dist/build.sh`, then attaches the archives and
`SHA256SUMS` to the tag's GitHub release. A tag carrying a prerelease
identifier is published `--prerelease`.

There is no `CHANGELOG.md` and no changelog automation: the release notes
are written by the workflow and say what the distribution contains.

### Cutting by hand

The same script is the whole release when the workflow cannot run — a
runner outage, or a repository whose Actions minutes are unavailable:

```sh
# on a Mac, so the darwin archives can carry the menu bar app
macapp/Scripts/bundle.sh --configuration release
WT_MACAPP_BUNDLE="$PWD/macapp/.build/WorktreeMenu.app" dist/build.sh 0.2.0
gh release create v0.2.0 dist/out/wt-*.tar.gz dist/out/wt-*.zip \
  dist/out/SHA256SUMS --title "Worktree Manager v0.2.0"
```

One machine can build every cell this way, because the Go half
cross-compiles and only the app needs a Mac. `WT_CELLS` restricts the run
to some of them, which is what the two release jobs pass. Without
`WT_MACAPP_BUNDLE` the darwin archives carry the two binaries alone.

A local build needs no release at all: the archives in `dist/out/` *are*
the distribution, and a Dockerfile can `COPY` one in rather than
downloading it.

### Verifying the artefacts

1. `sha256sum -c SHA256SUMS` against the release assets.
2. A temp-prefix install on each platform — `./install.sh --prefix
   /tmp/wt-prefix`, then `wt daemon status --prefix /tmp/wt-prefix`.
   Nothing is loaded under a prefix.
3. The macOS launchd states and a real `daemon install`, by hand on a
   macOS machine: no test in the suite touches the machine's LaunchAgents.
4. The Windows desktop surfaces — a real `schtasks /Create`, `taskkill`
   against a real desktop process, Docker Desktop and WSL2 reachability
   from the logon task — by hand on an interactive Windows desktop. A
   hosted runner proves the build and the unit layers instead, and
   everything unverified is reported `not_run` rather than assumed.
5. A clean machine installs from the release artefacts, adopts a fixture
   repo and passes both acceptance gates by hand.

> **TODO:** steps 3–5 have never been run — no macOS release install, no
> Windows machine, no clean-machine run. The clean-machine criterion is
> unmet until one happens.

## What gets published

| Artefact | Contains |
|---|---|
| `wt-<version>-darwin-arm64.tar.gz`, `-darwin-amd64.tar.gz` | `wt`, `wtd`, `install.sh`, `README.txt`, `SHA256SUMS`, `WorktreeMenu.app` |
| `wt-<version>-linux-amd64.tar.gz`, `-linux-arm64.tar.gz` | the same without the app |
| `wt-<version>-windows-amd64.zip` | `wt.exe`, `wtd.exe`, `install.ps1`, `README.txt`, `SHA256SUMS` |
| `SHA256SUMS` | the sha256 of every archive |

Every archive nests those files under one directory named for the archive,
so `tar xzf wt-<version>-<os>-<arch>.tar.gz` produces
`wt-<version>-<os>-<arch>/` and unpacking one in a directory that holds
other things scatters nothing. Both installers resolve the binaries and the
manifest relative to their own location, so the directory is theirs to sit
in and they never look outside it.

Everything builds `CGO_ENABLED=0`, so an archive needs nothing installed to
run. `linux/arm64` is not only a Linux desktop cell: it is what a container
built on an Apple Silicon machine runs, so it is the cell that puts the
client into a local Docker image.

Every archive carries its own `SHA256SUMS` covering the two binaries; the
installer verifies against it before copying anything, and an archive
without one is refused rather than skipped. The two binaries always ship
side by side, because `wt daemon install` finds `wtd` next to `wt`. The zip
is built by the stdlib-only `dist/ziphelper.go`, so cutting a release needs
no `zip` binary.

Nothing else ships: no Homebrew tap, no winget manifest, no apt or rpm
package, no coordinator image and no `wt upgrade` — the PLAN-SCOPE.md
packaging non-goals. A release is an archive and an installer. The macOS
half is signed where a Developer ID is configured, which is the one
addition to that list; see "The menu bar app" below.

## Installing on a host

```sh
tar xzf wt-<version>-<os>-<arch>.tar.gz
cd wt-<version>-<os>-<arch>
./install.sh
```

The installer copies the binaries into a prefix and then drives
`wt daemon install`, which owns the per-platform registration — launchd on
macOS, the systemd user unit and its paired socket unit on Linux, the logon
scheduled task on Windows. It is never reimplemented here. `wt daemon
status` should then say `running`.

| Flag (unix / Windows) | Effect |
|---|---|
| `--prefix` / `-Prefix` | self-contained install: binaries in `<dir>/bin`, registration written under the same prefix and **nothing loaded** |
| `--addr` / `-Addr` | pin the listen address. Absent, the installer probes for a free port and pins that, saying which — a second user on a machine cannot bind the first's port |
| `--container-token` / `-ContainerToken` | admit container clients (16+ characters, no whitespace). `WT_CONTAINER_TOKEN` sets it without putting it in shell history |
| `--allow-host` / `-AllowHost` | a `Host` header value the coordinator accepts beyond loopback and its own address (repeatable) — see below |
| `--client-only` / `-ClientOnly` | install the `wt` client alone: no `wtd`, no registration |
| `--no-menubar` / — | skip the macOS menu bar app, which a darwin archive otherwise installs into `/Applications` and starts |
| `--skip-verify` / `-SkipVerify` | install without checking the binaries against `SHA256SUMS` |
| `--dry-run` / `-DryRun` | print every action, change nothing |
| `--uninstall` / `-Uninstall` | drive `wt daemon uninstall`, then remove the binaries |

The settings are independent: a custom address needs no token, a token
needs no custom address, and either can be given alone.

Every install prints what it replaces and with what, from the binaries'
own `--version` lines. Binaries are replaced copy-to-temp-then-rename, so a
running coordinator keeps executing the old image until it exits; Windows
ends the logon task first, because it cannot rename over a running
executable.

An install then runs `wt claude install --refresh-only`, which brings an
existing Claude Code integration up to date. The two hook scripts and the
onboarding skill are embedded in the binary, so a new `wt` carries new
copies while the ones on disk stay at whatever version wrote them; without
this an upgrade never reaches them. It creates no installation — the hooks
are the user's opt-in, made by `wt claude install`, and an upgrade does not
make it for them — and it leaves a file the user made their own alone,
naming what it skipped. A `--prefix` install is self-contained and loads
nothing, so it does not touch `~/.claude`, and `--client-only` never gets
this far.

Whether a file is the user's is decided by what wt recorded writing, never
by whether the file differs from what this build would write. A hook script
carries a managed block and is recognised by its markers. The skill files
are documents copied verbatim — one of them quotes those markers in an
example of the convention — so their digests are recorded in
`~/.claude/skills/worktree-onboarding/.wt-installed.json`, written with the
files and removed with them. Content equality cannot stand in for either:
a file differing from this build's copy is *either* an older release's or
an edit, and the two want opposite treatment. An installation predating
that record still upgrades cleanly where its files match this build's; one
holding older text is genuinely ambiguous and is reported as such, with
`--force` named.

`--uninstall` never removes the store, and it refuses — exit 3, nothing
changed — while the registry still holds entries, naming `wt list` and
`wt rm`. The entry count comes from the coordinator's `list`, because the
database is never client-readable, so an unreachable coordinator is a
refusal too. `wt daemon uninstall --force` is the only way past either.

## The menu bar app

`WorktreeMenu.app` ships inside the two macOS archives, and `install.sh`
installs it into `/Applications` and starts it. `--no-menubar` skips it.
It is skipped anyway under `--client-only`, because a container has no
desktop, and under `--prefix`, because a prefix install is self-contained
and `/Applications` sits outside any prefix.

The app is a Swift build and cannot be cross-compiled, which is why the
release splits across two runners: the linux and windows archives are
assembled on Linux, the darwin archives on macOS with the built bundle
staged into them through `WT_MACAPP_BUNDLE`, and a publish job joins the
halves into one release with one `SHA256SUMS` over every archive.

The bundle carries no `SHA256SUMS` line of its own. A code signature
covers the whole bundle and says who signed it, which a digest of one file
inside it does not, so `install.sh` checks it with `codesign` and removes
it again when it does not verify.

Replacing a running copy is part of installing it. The installer asks the
app to quit, waits, and terminates it by name when it does not answer — a
background-only app does not reliably respond to the request, and a
process that keeps running while its bundle is replaced underneath it
leaves `open` re-activating the old build with nothing to show that
anything went wrong.

### Signing

With the six signing secrets configured on the repository the app is
signed with a Developer ID, notarized and stapled, and Gatekeeper accepts
it without asking Apple at launch. Without them it is ad-hoc signed, which
runs only where a quarantine attribute is absent, so `install.sh` clears
that attribute for an ad-hoc bundle and says it did, and leaves a
Developer ID bundle alone.

| Secret | What it is |
|---|---|
| `MACAPP_SIGNING_IDENTITY` | the identity codesign signs with, e.g. `Developer ID Application: Name (TEAMID)` |
| `MACAPP_CERTIFICATE_P12` | the Developer ID Application certificate and its key, exported as a `.p12` and base64 encoded |
| `MACAPP_CERTIFICATE_PASSWORD` | the password that `.p12` was exported with |
| `MACAPP_NOTARY_KEY_ID` | the App Store Connect API key id |
| `MACAPP_NOTARY_KEY_ISSUER` | that key's issuer id |
| `MACAPP_NOTARY_KEY_P8` | the contents of the key's `.p8` file |

The same identity signs `wt` and `wtd` in the darwin archives, through
`build.sh`'s `WT_DARWIN_SIGNING_IDENTITY`.

### Building it locally

For a development loop, `Scripts/bundle.sh` ad-hoc signs a bundle in
`macapp/.build`:

```sh
macapp/Scripts/bundle.sh --configuration release
```

Installing that bundle by hand is the same three steps the installer
takes, and a running copy has to go first or `open` re-activates it:

```sh
pkill -f /Applications/WorktreeMenu.app/Contents/MacOS/WorktreeMenu
rm -rf /Applications/WorktreeMenu.app
cp -R macapp/.build/WorktreeMenu.app /Applications/
open /Applications/WorktreeMenu.app
```

The app shells out to `wt` and renders what comes back, so it uses
whichever binary the host install put on the path.

## Installing into a container

A container runs the **client alone**. It never runs `wtd`: a coordinator
inside the image would own its own store and hand out resources the host
knows nothing about, which is the thing the design exists to prevent.

Three things must line up, and each is a deliberate opt-in at the host's
coordinator:

1. **Admission.** Absent `--container-token`, only host clients are
   admitted. The token is the whole of a container's identity; it is
   compared in constant time, and a missing token and a wrong one get the
   same refusal so the surface is not an oracle.
2. **The `Host` header.** The coordinator refuses any request whose `Host`
   is neither loopback nor its own address — the DNS-rebinding guard that
   keeps a page in the user's browser out of the API. A container reaching
   the host by name sends that name, so the name must be allowed:
   `--allow-host host.docker.internal`. It widens the guard by exact name
   and by nothing else; a `Host` that was not named is still refused, and
   the refusal names the flag that would admit it.
3. **Reachability.** `--network host` on Linux, or `host.docker.internal`
   on Docker Desktop (add `--add-host host.docker.internal:host-gateway`
   on plain Docker).

Set the host up once:

```sh
./install.sh --container-token "$(openssl rand -hex 24)" \
             --allow-host host.docker.internal
```

Put the client in the image — the linux archive matching the container's
architecture, which on an Apple Silicon machine is `linux-arm64`:

```dockerfile
FROM debian:bookworm-slim
ARG WT_VERSION=0.2.0
ARG TARGETARCH
ADD https://github.com/mrgeoffrich/worktree-manager/releases/download/v${WT_VERSION}/wt-${WT_VERSION}-linux-${TARGETARCH}.tar.gz /tmp/wt.tar.gz
RUN set -eu; \
    mkdir -p /tmp/wt && tar xzf /tmp/wt.tar.gz -C /tmp/wt; \
    /tmp/wt/install.sh --client-only --prefix /usr/local; \
    rm -rf /tmp/wt /tmp/wt.tar.gz
```

`TARGETARCH` is Docker's own build argument and is already `amd64` or
`arm64` — the spelling `GOARCH` uses — so the archive name interpolates
directly. The installer verifies the binary against the archive's
`SHA256SUMS` before copying, so the build fails on a tampered or truncated
download rather than baking one into the image.

Then run it pointed at the host:

```sh
docker run --rm \
  -e WT_ENDPOINT=http://host.docker.internal:7833 \
  -e WT_CLIENT_TOKEN="$WT_CONTAINER_TOKEN" \
  -e WT_CLIENT_EPHEMERAL=1 \
  --add-host host.docker.internal:host-gateway \
  myimage wt list
```

- The port is the one the install pinned. `wt daemon status` on the host
  says which, as does `base_url` in `endpoint.json`; the installer prints
  it too.
- `WT_CLIENT_EPHEMERAL=1` marks a disposable container. It composes with
  the token rather than conflicting: the token admits it, the flag marks
  its entries reclaimable. It calls `POST /v1/session` once and uses the
  issued session id as its bearer thereafter, so a coordinator restart
  mid-`init` does not strand it.
- The store is never shared into a container. It gets `WT_ENDPOINT` and
  `WT_CLIENT_TOKEN`, and mounts nothing.

## Rolling back

A rollback is installing the previous release. The distribution is
self-contained, so nothing is shared between releases except the store —
which is the point of the versioning policy above.

1. **Stop the coordinator.** macOS `launchctl bootout
   gui/$(id -u)/com.mrgeoffrich.wtd`; Linux `systemctl --user stop
   com.mrgeoffrich.wtd.socket`; Windows `schtasks /End /TN
   com.mrgeoffrich.wtd`.
2. **Install the previous archive** with its own installer, exactly as a
   first install. The new registration overwrites the old unit and starts
   the previous binary.
3. **The store is not touched.** If a release between the two bumped the
   store schema, the older binary refuses with the upgrade named rather
   than half-reading it — the refusal working as designed. That rollback
   is a backup restore of the store, and the refusal says so.

An upgrade that interrupted a worktree's `init` is safe in both
directions: a starting coordinator first tears down every `reserving`
entry by handle and drops it, or moves it to `tearing-down` with a note
when teardown cannot complete. The client re-runs `wt init` and allocates
fresh.

## Where the installer put things

| Piece | Location |
|---|---|
| Store | `WT_HOME`, else `$HOME/.wt`. `wt.db` is coordinator-only; `endpoint.json` beside it is the one file a host client reads, which is why `WT_HOME` is read by both binaries |
| Endpoint | `127.0.0.1:7833` by default, or whatever `endpoint.json` records. `WT_ENDPOINT` overrides it for a client |
| Registration (macOS) | `~/Library/LaunchAgents/com.mrgeoffrich.wtd.plist` — `RunAtLoad` + `KeepAlive`, not socket activation, which needs the C-only `launch_activate_socket` |
| Registration (Linux) | `~/.config/systemd/user/com.mrgeoffrich.wtd.{service,socket}`, with socket activation. A user unit stops at logout unless `loginctl enable-linger <user>` is set |
| Registration (Windows) | `%LOCALAPPDATA%\wt\com.mrgeoffrich.wtd.xml`, registered via `schtasks` |

A registration carrying a container token is written 0600 on unix, and the
token is never echoed.

Two developers on one machine each get their own coordinator, store and
ledger, and neither can see the other's allocations —
[`ARCHITECTURE.md`](ARCHITECTURE.md) "Two users on one machine (R12)".
