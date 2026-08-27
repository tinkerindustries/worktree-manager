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
    /// Makes overlapping refreshes resolve deterministically: whichever
    /// refresh started last is the one whose result is kept, regardless
    /// of which finishes first (WorktreeMenuCore.RefreshCoordinator).
    private let refreshCoordinator = RefreshCoordinator()

    func applicationDidFinishLaunching(_ notification: Notification) {
        let item = NSStatusBar.system.statusItem(withLength: NSStatusItem.squareLength)
        if let button = item.button {
            let image = NSImage(
                systemSymbolName: "point.3.connected.trianglepath.dotted",
                accessibilityDescription: "Worktree Manager"
            )
            image?.isTemplate = true
            button.image = image
        }

        let menu = NSMenu()
        menu.delegate = self
        item.menu = menu
        statusItem = item

        refreshTimer = Timer.scheduledTimer(withTimeInterval: 60, repeats: true) { [weak self] _ in
            Task { @MainActor in
                self?.refresh(updatingOpenMenu: true)
            }
        }
        refresh(updatingOpenMenu: false)
    }

    func applicationWillTerminate(_ notification: Notification) {
        refreshTimer?.invalidate()
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

    private func render(into menu: NSMenu) {
        menu.removeAllItems()

        if let snapshot = lastSnapshot {
            appendEntries(snapshot.entries, to: menu)
            if let lastError {
                menu.addItem(.separator())
                menu.addItem(disabledItem("Last refresh failed: \(describe(lastError))"))
            }
        } else if let lastError {
            menu.addItem(disabledItem(describe(lastError)))
        } else {
            menu.addItem(disabledItem("Loading…"))
        }

        menu.addItem(.separator())
        let quitItem = NSMenuItem(title: "Quit", action: #selector(quit), keyEquivalent: "q")
        quitItem.target = self
        menu.addItem(quitItem)
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
                let header = NSMenuItem(title: group.headerTitle, action: nil, keyEquivalent: "")
                header.isEnabled = false
                let submenu = NSMenu()
                for row in group.rows {
                    submenu.addItem(menuItem(for: row))
                }
                header.submenu = submenu
                menu.addItem(header)
            }
        }
    }

    private func menuItem(for row: MenuRow) -> NSMenuItem {
        let item = NSMenuItem(title: row.title, action: nil, keyEquivalent: "")
        item.isEnabled = row.isEnabled
        item.toolTip = row.tooltip
        return item
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
