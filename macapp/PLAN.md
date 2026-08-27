# macapp — the worktree-manager menu bar app for macOS

A read-only menu bar app that shows every registered worktree grouped by
repository, and opens one in the user's editor. It is a client: it shells
out to `wt` and renders what comes back. It never reads the store, never
speaks HTTP, and never crawls the filesystem.

This plan is frozen for the duration of the work it describes. Phases are
implemented one at a time and each ends with the acceptance checks passing.

## Decisions already made

**Registry only.** The app's whole picture of the world is
`wt list --json`. A git worktree that was never `wt init`-ed does not
appear. `wt doctor` already reports those ("directory present, no entry"),
so phase 5's badge surfaces them without the app touching the filesystem.

**Unreachable coordinator is an error state.** `wt` exits 5 when it cannot
dial. The menu then shows one row saying so and nothing else.

**URL schemes, not executables.** A LaunchServices-started app does not
inherit the login shell's `PATH`, so `code` and `cursor` are not reachable
by name. The app opens `vscode://file/...` and friends through
`NSWorkspace`, which needs no PATH at all.

**Read-only in v1.** Writes are deferred, and when they come they go
through `wt rm` and `wt cleanup` rather than HTTP, because the removal
policy, the exit-3 refusals and the never-forced `git worktree remove`
live in the client and must not be reimplemented here.

**SwiftPM, not an Xcode project.** An `.xcodeproj` is an opaque file that
is painful to edit and to review. The app is a SwiftPM executable target
plus a script that assembles the `.app` bundle around the built binary.
CI runs `swift build` and `swift test`; the bundle script runs at release.

**The repo row is a header, not a link.** The main checkout is not a
registry entry — `internal/coord/fleet.go` treats it as slot 0, never
managed — and no store column holds its path. Giving the repo row a click
action would need a new `ListEntry` field and a git shell-out per repo per
list. Deferred until it is missed.

## Layout

```
macapp/
  PLAN.md
  Package.swift
  Sources/WorktreeMenu/
    main.swift            App entry, NSApplication setup
    AppDelegate.swift     NSStatusItem, menu delegate, refresh timer
    Models.swift          ListEntry and friends, decoded from wt list --json
    WtClient.swift        Locating wt, running it, mapping exit codes
    MenuBuilder.swift     Snapshot -> menu description
    EditorLauncher.swift  URL scheme templates, modifier-key routing
    Preferences.swift     UserDefaults keys and the preferences window
  Resources/
    Info.plist
    StatusIcon.pdf        Template image, inverts for dark mode
    AppIcon.icns
  Scripts/
    bundle.sh             Assemble WorktreeMenu.app from the built binary
    sign.sh               codesign, notarytool submit, stapler staple
  Tests/WorktreeMenuTests/
```

## Data contract

`wt list --json` prints exactly one object on stdout and exits 0:

```json
{"entries": [{
  "app": "worktree-manager", "slug": "macapp", "slot": 3,
  "description": "...", "state": "active",
  "path": "/Users/geoff/...", "path_visible": true,
  "owner": "...", "owner_kind": "host", "ephemeral": false,
  "created_at": "...", "last_seen": "...",
  "resources": {"web": {"type": "port", "value": 7843}},
  "flags": ["stale"]
}]}
```

`resources` values are `int` for `type == "port"` and `string` otherwise,
so the Swift model needs an enum, not `Any`.

Exit codes (`internal/cli/errors.go`): 0 ok, 1 failure, 2 usage,
3 refused, 4 unavailable, 5 unreachable. Only 0 and 5 get bespoke
handling; the rest render the stderr text as a generic failure.

Never pass `--wide`. Seed credentials must not appear in a menu.

---

## Phase 1 — Skeleton and data path

Goal: an icon in the menu bar that opens a flat list of `app/slug`, and a
visible error state when the coordinator is stopped.

Build:

- `Package.swift`, a macOS 14 executable target named `WorktreeMenu`, no
  external dependencies.
- `Models.swift`: `ListResult`, `ListEntry`, `Resolved` with a
  `ResolvedValue` enum (`.port(Int)`, `.text(String)`). `Codable`, snake
  case decoding strategy.
- `WtClient.swift`:
  - Resolving the binary, in order: `UserDefaults` key `WTBinaryPath`,
    then `~/.local/bin/wt` (what `dist/install.sh` uses), then
    `/usr/local/bin/wt`, then `/opt/homebrew/bin/wt`. None found is its
    own error case, distinct from a failed run.
  - `list() async throws -> ListResult`, running the binary with
    `["list", "--json"]` via `Process` with piped stdout and stderr.
  - An error enum: `.binaryNotFound`, `.unreachable(String)` for exit 5,
    `.failed(code: Int32, stderr: String)`, `.decode(Error)`.
- `AppDelegate.swift`: `NSStatusItem` with a template image, an `NSMenu`
  whose delegate is the app delegate, a Quit item.

Refresh model, which must not change in later phases: the app holds a
cached snapshot and its timestamp. `menuNeedsUpdate` renders the cache
immediately and starts an async refresh; when that returns, the open menu
is mutated in place. A 60-second timer keeps the cache warm. **No
subprocess is ever awaited on the main thread.** A refresh that fails
leaves the previous snapshot in place and records the error, so the menu
can show stale data and the reason it is stale together.

`Info.plist` sets `LSUIElement` to true — no dock icon, no menu bar menus.

Acceptance:
- `swift build` and `swift test` pass.
- Unit tests decode a fixture `wt list --json` payload, including a port
  resource and a string resource, and an entry with flags.
- Unit tests cover the exit-code mapping with a stub runner. `WtClient`
  takes its process-runner as an injectable closure so this is testable
  without a real `wt`.
- Run `Scripts/bundle.sh`, open the app, see entries. Stop `wtd`, reopen
  the menu, see the unreachable row.

Do not: build the submenus, handle clicks, or add preferences yet.

---

## Phase 2 — The menu

Goal: entries grouped by repository with the right visual treatment.

- Group by `app`. One top-level item per app, title `"<app>   <count>"`,
  disabled, with a submenu.
- Sort apps alphabetically, then entries by `slot`, then `slug` — the same
  order `writeListTable` in `internal/cli/fleet.go` uses, so the menu and
  the CLI never disagree.
- Row title is the slug. The first port found in `resources` is shown
  right-aligned as `:7843`. `description` becomes the tooltip.
- `state` drives a leading symbol. Treat the state string as open —
  render a known set and fall back to a neutral marker for anything else,
  so a new state does not break the menu.
- `flags` drive treatment: `stale` disables the row and appends
  `(stale)`; `foreign` appends the owner; `unverifiable` appends a note
  that the path cannot be seen from here.
- `path_visible == false` disables the row whatever the flags say. That
  is the container case and opening it would silently do nothing.
- Empty registry gets one disabled row saying so.

Acceptance:
- `swift build`, `swift test` pass.
- `MenuBuilder` is a pure function from snapshot to a menu description
  value, tested without AppKit. Building the real `NSMenu` from that
  description is a thin separate step.
- Tests cover: grouping and ordering, the port column, each flag, the
  invisible-path case, the empty registry, the unreachable state.

Do not: wire up any click actions.

---

## Phase 3 — Opening things

Goal: clicking a row opens the worktree.

- `EditorLauncher.swift` holds the scheme templates:
  `vscode://file{path}`, `cursor://file{path}`, `zed://file{path}`,
  `jetbrains://idea/navigate/reference?path={path}`. Percent-encode the
  path, substitute, hand to `NSWorkspace.shared.open`.
- Detect installed editors with
  `NSWorkspace.shared.urlForApplication(toOpen:)` per scheme. Offer only
  what is installed, plus a free-text template as the escape hatch.
- Modifier keys, read from `NSEvent.modifierFlags` inside the action:
  plain click opens the editor; option opens Terminal at the path;
  shift reveals in Finder via `activateFileViewerSelecting`.
- Open Terminal with `NSWorkspace.open(_:withApplicationAt:)` against
  `Terminal.app`, not AppleScript. Apple Events would need an Automation
  entitlement and a consent prompt, and buys only a pre-typed command.
- `Preferences.swift`: a small window with the editor choice, the `wt`
  path override, and the refresh interval. `UserDefaults` throughout.

Acceptance:
- `swift build`, `swift test` pass.
- Template substitution and percent-encoding are unit tested, including
  a path with spaces and one with a non-ASCII character.
- Modifier routing is a pure function from `NSEvent.ModifierFlags` to an
  action enum, unit tested.
- By hand: click opens the editor, option opens Terminal, shift reveals
  in Finder.

---

## Phase 4 — Shipping

Goal: a signed, notarized app a stranger can double-click.

- `SMAppService.mainApp.register()` behind a launch-at-login preference.
- `Scripts/bundle.sh`: assemble `WorktreeMenu.app` — binary into
  `Contents/MacOS`, `Info.plist` and resources into `Contents`, icon into
  `Contents/Resources`. Ad-hoc sign so a local build runs.
- `Scripts/sign.sh`: `codesign --options runtime --timestamp` with the
  Developer ID Application identity, zip, `notarytool submit --wait`,
  `stapler staple`. Every credential comes from the environment; nothing
  is hard-coded and nothing is echoed.
- Release workflow: a macOS job on the existing `v*` tag trigger. Import
  the certificate into a temporary keychain from secrets, run the two
  scripts, attach `WorktreeMenu-<version>.zip` to the release, and add
  its digest to the outer `SHA256SUMS`.
- Sign the Go binaries in the same job while the machinery is out.
  `dist/build.sh` produces them unsigned today, which means a
  browser-downloaded release archive is quarantined and `wtd` — a
  resident process registering with launchd — is exactly the shape
  Gatekeeper is suspicious of.

Note for whoever implements this: the signing and notarization steps
cannot be verified here. There is no Developer ID certificate in this
environment and no App Store Connect key. Write the scripts and the
workflow, make them fail loudly and by name when a credential is missing,
and mark the end-to-end run as unverified in the phase report.

Acceptance:
- `swift build`, `swift test` pass.
- `Scripts/bundle.sh` produces an app that launches.
- `Scripts/sign.sh --help` and a dry run explain what they need.
- `shellcheck` clean, matching the existing `dist/` scripts.

---

## Phase 5 — Polish

Goal: the app tells you when something is wrong before you go looking.

- Badge the status icon when `wt doctor --json` reports a finding above
  `info`. A menu item opens the full report. Doctor runs on a slower
  timer than `list` — it reads everything and is not free.
- A menu item showing the app version and the `wt` version it is talking
  to (`wt --version`). When the JSON stops decoding, the app should say
  which of the two moved, not just fail.
- The stale-snapshot line: when the last refresh failed but a previous
  snapshot survives, show both the data and the age and reason.

Acceptance:
- `swift build`, `swift test` pass.
- Doctor severity mapping and the badge decision are unit tested.
- By hand: stop `wtd`, confirm the menu shows stale data with a reason
  rather than an empty list.

---

## Deferred, on purpose

- **Writes.** `wt rm` and `wt cleanup` from the menu, behind a
  confirmation sheet that shows the client's own refusal text on
  failure. Through the CLI, never through HTTP.
- **The repo row opening the main checkout.** Needs a `MainCheckout`
  field on `ListEntry`, filled the way `doctorRepos` derives it. Additive
  to a frozen route, so the route table is safe, but it costs a git
  shell-out per repo per list.
- **Unadopted worktrees.** Visible only through doctor's badge.
- **Windows.** A different tray implementation entirely. The URL-scheme
  launcher is the part that ports.
