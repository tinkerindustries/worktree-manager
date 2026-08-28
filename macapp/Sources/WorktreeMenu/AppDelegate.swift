import AppKit
import WorktreeMenuCore

/// Owns the status item and the one menu it shows. The refresh model is
/// fixed for every later phase (PLAN.md): the cached snapshot renders
/// immediately in `menuNeedsUpdate`, an async refresh starts alongside
/// it, and the open menu is mutated in place when that refresh returns.
/// A refresh that fails leaves the previous snapshot in place and
/// records the error, so the menu can show stale data and the reason
/// it is stale together. No subprocess is ever awaited on the main
/// thread: `WtClient`'s runner does its blocking work on a background
/// queue, and this class only ever awaits the result.
@MainActor
final class AppDelegate: NSObject, NSApplicationDelegate, NSMenuDelegate {
    private let client = WtClient()
    private var statusItem: NSStatusItem?

    /// The last successful `list()` result. Kept across a failed
    /// refresh so the menu can keep showing it.
    private var lastSnapshot: ListResult?
    /// The error from the most recent refresh, if it failed. Cleared on
    /// the next success.
    private var lastError: Error?
    private var lastUpdated: Date?
    private var refreshTimer: Timer?
    private var currentRefreshInterval: TimeInterval = PreferencesKeys.defaultRefreshIntervalSeconds
    /// Makes overlapping refreshes resolve deterministically: whichever
    /// refresh started last is the one whose result is kept, regardless
    /// of which finishes first (WorktreeMenuCore.RefreshCoordinator).
    private let refreshCoordinator = RefreshCoordinator()

    /// The last `wt doctor --json` result. `doctor` reads every entry,
    /// not just this app's, so it runs on its own slower timer rather
    /// than on every `list` refresh (PLAN.md phase 5).
    private var lastDoctorResult: DoctorResult?
    private var lastDoctorError: Error?
    /// What `wt --version` last reported, or `nil` before the first
    /// check has completed.
    private var lastWtVersionOutcome: WtVersionOutcome?
    private var doctorTimer: Timer?
    /// A small red dot overlaid on the status button, shown only when
    /// `lastDoctorResult` carries a finding above `info`
    /// (`DoctorCore.shouldBadge`).
    private var doctorBadgeView: NSView?
    /// A separate ticket counter from `refreshCoordinator`'s: the doctor
    /// check and the list refresh run independently, on different
    /// timers, so an overlap in one must not discard the other.
    private let doctorRefreshCoordinator = RefreshCoordinator()
    /// How much slower the doctor/version check runs than the list
    /// refresh: doctor reads everything, not one app's worth, and is not
    /// free (PLAN.md phase 5).
    private static let doctorIntervalMultiplier: TimeInterval = 5

    private var preferencesWindowController: PreferencesWindowController?
    /// The doctor report window, kept across openings so the sections the
    /// reader collapsed stay collapsed.
    private var doctorWindowController: DoctorReportWindowController?
    private var defaultsObserver: NSObjectProtocol?

    func applicationDidFinishLaunching(_ notification: Notification) {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let button = item.button {
            let image = NSImage(
                systemSymbolName: "point.3.connected.trianglepath.dotted",
                accessibilityDescription: "Worktree Manager"
            )
            image?.isTemplate = true
            button.image = image

            let badge = NSView(frame: NSRect(x: button.bounds.width - 8, y: button.bounds.height - 8, width: 7, height: 7))
            badge.autoresizingMask = [.minXMargin, .minYMargin]
            badge.wantsLayer = true
            badge.layer?.backgroundColor = NSColor.systemRed.cgColor
            badge.layer?.cornerRadius = 3.5
            badge.isHidden = true
            button.addSubview(badge)
            doctorBadgeView = badge
        }

        let menu = NSMenu()
        menu.delegate = self
        item.menu = menu
        statusItem = item

        startTimers(interval: readRefreshInterval())
        defaultsObserver = NotificationCenter.default.addObserver(
            forName: UserDefaults.didChangeNotification,
            object: nil,
            queue: .main
        ) { [weak self] _ in
            Task { @MainActor in
                self?.reconcileRefreshInterval()
            }
        }
        refresh(updatingOpenMenu: false)
        refreshDoctorAndVersion(updatingOpenMenu: false)
    }

    func applicationWillTerminate(_ notification: Notification) {
        refreshTimer?.invalidate()
        doctorTimer?.invalidate()
        if let defaultsObserver {
            NotificationCenter.default.removeObserver(defaultsObserver)
        }
    }

    /// Reads the preference's stored refresh interval, falling back to
    /// the phase-1 default when nothing has been set.
    private func readRefreshInterval() -> TimeInterval {
        (UserDefaults.standard.object(forKey: PreferencesKeys.refreshIntervalSeconds) as? Double)
            ?? PreferencesKeys.defaultRefreshIntervalSeconds
    }

    /// Starts both the list-refresh timer and the slower doctor/version
    /// timer from one interval, so a preferences change moves both
    /// together rather than requiring two call sites to stay in sync.
    private func startTimers(interval: TimeInterval) {
        startRefreshTimer(interval: interval)
        startDoctorTimer(interval: interval)
    }

    private func startRefreshTimer(interval: TimeInterval) {
        refreshTimer?.invalidate()
        currentRefreshInterval = interval
        refreshTimer = scheduleOnCommonModes(interval: interval) { [weak self] in
            self?.refresh(updatingOpenMenu: true)
        }
    }

    /// Doctor and version run on `doctorIntervalMultiplier` times the
    /// list interval — slower, since doctor reads everything rather than
    /// one app's worth (PLAN.md phase 5).
    private func startDoctorTimer(interval: TimeInterval) {
        doctorTimer?.invalidate()
        let doctorInterval = interval * Self.doctorIntervalMultiplier
        doctorTimer = scheduleOnCommonModes(interval: doctorInterval) { [weak self] in
            self?.refreshDoctorAndVersion(updatingOpenMenu: true)
        }
    }

    /// Schedules a repeating timer in `.common` run loop modes rather
    /// than the `.default` mode `Timer.scheduledTimer` uses. An open
    /// menu puts the main run loop into event-tracking mode, so a
    /// default-mode timer stops firing for exactly as long as the menu
    /// is on screen — the one time the user is looking at it. The
    /// refresh then only ever happened on open, and a menu held open
    /// never ticked.
    private func scheduleOnCommonModes(
        interval: TimeInterval,
        body: @escaping @MainActor () -> Void
    ) -> Timer {
        let timer = Timer(timeInterval: interval, repeats: true) { _ in
            Task { @MainActor in
                body()
            }
        }
        RunLoop.main.add(timer, forMode: .common)
        return timer
    }

    /// Restarts both timers when the preferences window has changed the
    /// stored interval. `UserDefaults.didChangeNotification` fires on
    /// any default changing, not only this one, so this only acts when
    /// the value actually moved.
    private func reconcileRefreshInterval() {
        let interval = readRefreshInterval()
        guard interval != currentRefreshInterval else { return }
        startTimers(interval: interval)
    }

    /// Renders the cached snapshot immediately, then starts a refresh
    /// that mutates this same menu in place once it returns.
    func menuNeedsUpdate(_ menu: NSMenu) {
        render(into: menu)
        refresh(updatingOpenMenu: true)
    }

    /// Runs `wt list --json` in the background and records the result.
    /// `updatingOpenMenu` re-renders the status item's menu when the
    /// refresh completes — skipped for the startup call, where there is
    /// no open menu yet to mutate.
    ///
    /// `refreshCoordinator` guards every write against a newer refresh
    /// having started in the meantime, so two overlapping runs (the menu
    /// opened twice quickly, or the 60-second timer firing mid-refresh)
    /// cannot let the one that finishes last win when it is not the one
    /// that started last.
    private func refresh(updatingOpenMenu: Bool) {
        let ticket = refreshCoordinator.begin()
        Task {
            do {
                let result = try await client.list()
                guard refreshCoordinator.isCurrent(ticket) else { return }
                lastSnapshot = result
                lastError = nil
                lastUpdated = Date()
            } catch {
                guard refreshCoordinator.isCurrent(ticket) else { return }
                lastError = error
            }
            if updatingOpenMenu, refreshCoordinator.isCurrent(ticket), let menu = statusItem?.menu {
                render(into: menu)
            }
        }
    }

    /// Runs `wt doctor --json` and `wt --version` in the background and
    /// records what they reported. Both are cheap next to `list()` in
    /// isolation, but doctor reads the whole registry rather than one
    /// app's slice of it, so they run on `doctorTimer`'s slower cadence
    /// (PLAN.md phase 5) rather than alongside every `list` refresh.
    private func refreshDoctorAndVersion(updatingOpenMenu: Bool) {
        let ticket = doctorRefreshCoordinator.begin()
        Task {
            do {
                let result = try await client.doctor()
                guard doctorRefreshCoordinator.isCurrent(ticket) else { return }
                lastDoctorResult = result
                lastDoctorError = nil
            } catch {
                guard doctorRefreshCoordinator.isCurrent(ticket) else { return }
                lastDoctorError = error
            }

            do {
                let line = try await client.version()
                guard doctorRefreshCoordinator.isCurrent(ticket) else { return }
                lastWtVersionOutcome = VersionCore.parseVersionLine(line).map(WtVersionOutcome.version)
                    ?? .unparseable(raw: line)
            } catch {
                guard doctorRefreshCoordinator.isCurrent(ticket) else { return }
                lastWtVersionOutcome = .unavailable(reason: describe(error))
            }

            guard doctorRefreshCoordinator.isCurrent(ticket) else { return }
            updateStatusBadge()
            updateDoctorWindow()
            if updatingOpenMenu, let menu = statusItem?.menu {
                render(into: menu)
            }
        }
    }

    /// Shows or hides the status icon's badge dot from the current
    /// doctor result. Called whenever a doctor check completes, and on
    /// nothing else — a `list` refresh alone never changes it.
    private func updateStatusBadge() {
        doctorBadgeView?.isHidden = !(lastDoctorResult.map(DoctorCore.shouldBadge) ?? false)
    }

    private func render(into menu: NSMenu) {
        menu.removeAllItems()
        // Every item here decides its own enabled state: a stale row,
        // an app header, the placeholder lines. AppKit's automatic
        // enabling recomputes all of them from targets and actions and
        // would disagree.
        menu.autoenablesItems = false

        if let snapshot = lastSnapshot {
            appendEntries(snapshot.entries, to: menu)
            if let lastError {
                menu.addItem(.separator())
                let reason = describe(lastError)
                let line = lastUpdated.map {
                    StalenessCore.staleLine(lastUpdated: $0, now: Date(), reason: reason)
                } ?? "Last refresh failed: \(reason)"
                menu.addItem(disabledItem(line))
            }
        } else if let lastError {
            menu.addItem(disabledItem(describe(lastError)))
        } else {
            menu.addItem(disabledItem("Loading…"))
        }

        menu.addItem(.separator())
        menu.addItem(doctorMenuItem())
        menu.addItem(disabledItem(versionMenuTitleText()))

        menu.addItem(.separator())
        let preferencesItem = NSMenuItem(title: "Preferences…", action: #selector(openPreferences), keyEquivalent: ",")
        preferencesItem.target = self
        menu.addItem(preferencesItem)
        let quitItem = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
    }

    /// The doctor summary row: `DoctorCore.summaryLine` when a report
    /// has come back, the error when the check itself failed, or a
    /// "checking…" placeholder before the first one completes. Always
    /// clickable — `openDoctorReport` handles all three states — so the
    /// row's own text is the only thing that changes.
    private func doctorMenuItem() -> NSMenuItem {
        let title: String
        if let lastDoctorResult {
            title = DoctorCore.summaryLine(lastDoctorResult)
        } else if let lastDoctorError {
            title = "Doctor: \(describe(lastDoctorError))"
        } else {
            title = "Doctor: checking…"
        }
        let item = NSMenuItem(title: title, action: #selector(openDoctorReport), keyEquivalent: "")
        item.target = self
        return item
    }

    /// The version row's text: the app's own `CFBundleShortVersionString`
    /// next to what `wt --version` reported, naming which of the two
    /// could not be read rather than one generic failure
    /// (`VersionCore.versionMenuTitle`).
    private func versionMenuTitleText() -> String {
        guard let lastWtVersionOutcome else { return "Checking version…" }
        return VersionCore.versionMenuTitle(appVersion: appVersion(), wt: lastWtVersionOutcome)
    }

    private func appVersion() -> String? {
        Bundle.main.infoDictionary?["CFBundleShortVersionString"] as? String
    }

    /// Opens the full doctor report — the "menu item opens the full
    /// report" requirement (PLAN.md phase 5). A window rather than the
    /// `NSAlert` this started as: an alert renders the whole report as one
    /// unscrollable block of `informativeText`, which a report of any size
    /// outgrows, and it gives the reader nothing to act on but their own
    /// typing. The window groups the findings, scrolls, and puts the fix
    /// behind a button (`DoctorReportWindow.swift`).
    @objc private func openDoctorReport() {
        doctorReportWindow().show(doctorWindowContent())
    }

    /// The report window, made on first use and kept, so re-opening it
    /// returns to the sections the reader left open.
    private func doctorReportWindow() -> DoctorReportWindowController {
        if let doctorWindowController {
            return doctorWindowController
        }
        let controller = DoctorReportWindowController { [weak self] in
            // "Check again" runs the same refresh the timer does; its
            // result reaches the window through updateDoctorWindow.
            self?.refreshDoctorAndVersion(updatingOpenMenu: true)
        }
        doctorWindowController = controller
        return controller
    }

    /// What the window should show right now, from the same three states
    /// the menu row words itself from.
    private func doctorWindowContent() -> DoctorReportWindowController.Content {
        if let lastDoctorResult {
            // A check that failed leaves the previous report on screen, and
            // says so: the same rule the menu's stale line follows.
            return .report(lastDoctorResult, staleReason: lastDoctorError.map(describe))
        }
        if let lastDoctorError {
            return .failed(describe(lastDoctorError))
        }
        return .checking
    }

    /// Pushes a finished doctor check into the window when it is open. A
    /// closed window is left closed: a background refresh is not a reason
    /// to put a window in front of somebody.
    private func updateDoctorWindow() {
        doctorWindowController?.update(doctorWindowContent())
    }

    /// Turns `MenuBuilder`'s pure description into real `NSMenuItem`s —
    /// the one AppKit-touching step the phase-2 architecture requirement
    /// keeps thin and separate from the grouping and formatting rules
    /// themselves.
    private func appendEntries(_ entries: [ListEntry], to menu: NSMenu) {
        for node in MenuBuilder.build(from: entries) {
            switch node {
            case .row(let row):
                menu.addItem(menuItem(for: row))
            case .group(let group):
                // The header carries the app's submenu, so it stays
                // enabled: AppKit will not open the submenu of a
                // disabled item, which left every app's entries
                // unreachable. "Disabled" in PLAN.md phase 2 means the
                // header has no click action of its own, and leaving
                // `action` nil is what provides that.
                let header = NSMenuItem(title: group.headerTitle, action: nil, keyEquivalent: "")
                let submenu = NSMenu()
                submenu.autoenablesItems = false
                for row in group.rows {
                    submenu.addItem(menuItem(for: row))
                }
                header.submenu = submenu
                menu.addItem(header)
            }
        }
    }

    /// Builds the real menu item for one row. A row that is enabled and
    /// carries a path gets a click action; a disabled row (`stale`,
    /// `path_visible == false`) or the placeholder row (no `path`) stays
    /// unclickable, matching phase 2's rule that those rows must not be
    /// wired up (PLAN.md).
    private func menuItem(for row: MenuRow) -> NSMenuItem {
        let item = NSMenuItem(title: row.title, action: nil, keyEquivalent: "")
        item.attributedTitle = attributedTitle(for: row)
        item.isEnabled = row.isEnabled
        item.toolTip = row.tooltip
        if row.isEnabled, let path = row.path {
            item.target = self
            item.action = #selector(rowClicked(_:))
            item.representedObject = path
        }
        return item
    }

    /// The point, in the menu item's own coordinate space, the port text
    /// right-aligns against.
    private static let portColumnLocation: CGFloat = 160
    /// How far past the port column the trailing suffixes ("(stale)",
    /// "(owned by ...)") start, when a row has both.
    private static let trailingColumnGap: CGFloat = 14

    /// Builds a right-aligned `attributedTitle` for a row carrying a
    /// port, using an `NSParagraphStyle` right tab stop — the phase-5 fix
    /// for phase 2's bug: AppKit renders a literal `\t` in a plain
    /// `NSMenuItem.title` as-is, not as a column stop, so the port never
    /// actually lined up. Returns `nil` when the row has no port, so the
    /// caller keeps the plain-string `title` phase 2 always rendered.
    private func attributedTitle(for row: MenuRow) -> NSAttributedString? {
        guard let portText = row.portText else { return nil }

        var text = row.leadingText + "\t" + portText
        var tabStops = [NSTextTab(textAlignment: .right, location: Self.portColumnLocation, options: [:])]
        if let trailingText = row.trailingText {
            // A second, left-aligned stop past the port column: without
            // it, the trailing suffixes would fall inside the same
            // right-aligned run as the port and get pulled right along
            // with it, so their length would shift the port's position
            // instead of leaving it fixed.
            let trailingColumn = Self.portColumnLocation + Self.trailingColumnGap
            tabStops.append(NSTextTab(textAlignment: .left, location: trailingColumn, options: [:]))
            text += "\t" + trailingText
        }

        let paragraphStyle = NSMutableParagraphStyle()
        paragraphStyle.tabStops = tabStops
        paragraphStyle.defaultTabInterval = Self.portColumnLocation

        let attributed = NSMutableAttributedString(string: text)
        attributed.addAttribute(.paragraphStyle, value: paragraphStyle, range: NSRange(location: 0, length: attributed.length))
        return attributed
    }

    /// Reads which modifier keys are held at the moment of the click and
    /// routes to the matching action. The routing decision itself
    /// (`EditorLauncher.action(for:)`) is a pure function tested in
    /// `WorktreeMenuCoreTests`; this method only does the AppKit
    /// translation and the actual `NSWorkspace` call (PLAN.md).
    @objc private func rowClicked(_ sender: NSMenuItem) {
        guard let path = sender.representedObject as? String else { return }

        var modifiers: LaunchModifiers = []
        let flags = NSEvent.modifierFlags
        if flags.contains(.option) {
            modifiers.insert(.option)
        }
        if flags.contains(.shift) {
            modifiers.insert(.shift)
        }

        switch EditorLauncher.action(for: modifiers) {
        case .openEditor:
            openInEditor(path: path)
        case .openTerminal:
            openInTerminal(path: path)
        case .revealInFinder:
            revealInFinder(path: path)
        }
    }

    /// Resolves the user's chosen editor template against the installed
    /// editors, builds the URL, and hands it to `NSWorkspace`. Detection
    /// is redone on every click rather than cached: it is one
    /// `urlForApplication` call per built-in scheme, which is cheap next
    /// to the `wt list` round trip this menu already does on every open.
    private func openInEditor(path: String) {
        let defaults = UserDefaults.standard
        let installed = InstalledEditors.detect()
        guard let template = EditorLauncher.resolveTemplate(
            selectedID: defaults.string(forKey: PreferencesKeys.editorID),
            customTemplate: defaults.string(forKey: PreferencesKeys.customEditorTemplate),
            installed: installed
        ), let url = EditorLauncher.url(forTemplate: template, path: path) else {
            NSSound.beep()
            return
        }
        NSWorkspace.shared.open(url)
    }

    /// Opens Terminal.app at `path` via `NSWorkspace`, not AppleScript:
    /// Apple Events would need an Automation entitlement and a consent
    /// prompt, and would only buy a pre-typed `cd` command (PLAN.md).
    /// Terminal opens a new window at the working directory of a folder
    /// URL handed to it this way, which is the same effect.
    private func openInTerminal(path: String) {
        guard let terminalURL = NSWorkspace.shared.urlForApplication(withBundleIdentifier: "com.apple.Terminal") else {
            NSSound.beep()
            return
        }
        let folderURL = URL(fileURLWithPath: path, isDirectory: true)
        NSWorkspace.shared.open(
            [folderURL],
            withApplicationAt: terminalURL,
            configuration: NSWorkspace.OpenConfiguration()
        )
    }

    private func revealInFinder(path: String) {
        NSWorkspace.shared.activateFileViewerSelecting([URL(fileURLWithPath: path, isDirectory: true)])
    }

    @objc private func openPreferences() {
        if preferencesWindowController == nil {
            preferencesWindowController = PreferencesWindowController()
        }
        NSApp.activate(ignoringOtherApps: true)
        preferencesWindowController?.showWindow(nil)
        preferencesWindowController?.window?.makeKeyAndOrderFront(nil)
    }

    private func disabledItem(_ title: String) -> NSMenuItem {
        let item = NSMenuItem(title: title, action: nil, keyEquivalent: "")
        item.isEnabled = false
        return item
    }

    private func describe(_ error: Error) -> String {
        guard let wtError = error as? WtClientError else {
            return "wt failed: \(error.localizedDescription)"
        }
        switch wtError {
        case .binaryNotFound:
            return "wt not found"
        case .unreachable(let stderr):
            return stderr.isEmpty ? "Coordinator unreachable" : "Coordinator unreachable: \(stderr)"
        case .failed(let code, let stderr):
            return stderr.isEmpty ? "wt failed (exit \(code))" : stderr
        case .decode:
            return "Unexpected response from wt"
        case .timedOut:
            return "wt list timed out"
        }
    }

    @objc private func quit() {
        NSApplication.shared.terminate(nil)
    }
}
