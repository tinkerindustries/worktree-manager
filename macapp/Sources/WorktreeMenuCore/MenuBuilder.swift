import Foundation

/// One node to place in the menu: either a plain row (a placeholder like
/// "no worktrees registered") or a group (one app's disabled header item
/// carrying a submenu of that app's entries). `MenuBuilder.build` is a
/// pure function from a list of entries to a tree of these values — no
/// AppKit type appears here, which is what makes phase 2's grouping and
/// formatting rules testable without a running application (PLAN.md).
/// Turning this into a real `NSMenu` is a separate, thin step in the
/// `WorktreeMenu` executable target.
public enum MenuNode: Equatable, Sendable {
    case row(MenuRow)
    case group(AppMenuGroup)
}

/// One app's section: the disabled `"<app>   <count>"` header and the
/// submenu items for that app's entries, already ordered the way
/// `internal/cli/fleet.go`'s `writeListTable` orders them.
public struct AppMenuGroup: Equatable, Sendable {
    public let headerTitle: String
    public let rows: [MenuRow]

    public init(headerTitle: String, rows: [MenuRow]) {
        self.headerTitle = headerTitle
        self.rows = rows
    }
}

/// One menu item's worth of rendered data: the row split into the parts
/// an AppKit right tab stop needs, an optional tooltip, whether it should
/// be enabled, and the worktree path a click on it should act on. `path`
/// is `nil` for a row with nothing behind it — the empty-registry
/// placeholder — so `WorktreeMenu` knows not to wire up a click action
/// rather than wiring one up to an empty path.
///
/// AppKit renders a literal tab character as-is rather than as a column
/// stop, so a single `title` string with `\t` baked in cannot actually
/// right-align a port (a bug phase 2 shipped and phase 5 fixes). The fix
/// keeps the layout decision — what the leading text is, what the port
/// text is, what comes after — a pure `MenuBuilder` concern, and leaves
/// only the `NSAttributedString`/tab-stop construction to `WorktreeMenu`.
public struct MenuRow: Equatable, Sendable {
    /// The symbol and slug, e.g. "\u{25CF} macapp".
    public let leadingText: String
    /// The port suffix, e.g. ":7843", when the entry has a port
    /// resource; `nil` when it does not.
    public let portText: String?
    /// The flag-driven suffixes ("(stale)", "(owned by ...)"), already
    /// joined; `nil` when the entry carries none.
    public let trailingText: String?
    public let tooltip: String?
    public let isEnabled: Bool
    public let path: String?

    public init(
        leadingText: String,
        portText: String? = nil,
        trailingText: String? = nil,
        tooltip: String? = nil,
        isEnabled: Bool = true,
        path: String? = nil
    ) {
        self.leadingText = leadingText
        self.portText = portText
        self.trailingText = trailingText
        self.tooltip = tooltip
        self.isEnabled = isEnabled
        self.path = path
    }

    /// The plain-string fallback title, used when there is no port to
    /// align (nothing to align means a literal tab is never rendered) or
    /// when the caller has no attributed-string path available. This is
    /// the same text phase 2 always rendered — a tab character between
    /// the leading text and the port, then the trailing suffixes — kept
    /// for callers that only want a string and for anything that does
    /// not carry a port at all.
    public var title: String {
        var result = leadingText
        if let portText {
            result += "\t" + portText
        }
        if let trailingText {
            result += "  " + trailingText
        }
        return result
    }
}

/// Builds the menu's entry section from a `wt list --json` decode. Every
/// rule below is phase 2's (PLAN.md).
public enum MenuBuilder {
    /// The leading symbol for each state the Go side defines
    /// (`internal/store/registry.go`'s `StateReserving`, `StateActive`,
    /// `StateTearingDown`). A state string outside this set — one a later
    /// Go change might add — falls back to `unknownStateSymbol` rather
    /// than breaking the menu.
    private static let stateSymbols: [String: String] = [
        "active": "\u{25CF}", // ● filled: materialised
        "reserving": "\u{25D0}", // ◐ half filled: still coming up
        "tearing-down": "\u{25D1}", // ◑ half filled: going away
    ]

    /// The fallback for a state this app does not recognise.
    private static let unknownStateSymbol = "\u{25CB}" // ○ neutral

    /// The placeholder row for an empty registry.
    private static let emptyRegistryTitle = "No worktrees registered"

    /// Groups `entries` by `app`, sorts apps alphabetically and each
    /// app's entries by slot then slug (the same order
    /// `internal/cli/fleet.go`'s `writeListTable` uses, so the menu and
    /// the CLI never disagree), and renders each entry's row. An empty
    /// registry produces a single disabled placeholder row instead of any
    /// groups.
    public static func build(from entries: [ListEntry]) -> [MenuNode] {
        guard !entries.isEmpty else {
            return [.row(MenuRow(leadingText: emptyRegistryTitle, isEnabled: false))]
        }
        let byApp = Dictionary(grouping: entries, by: \.app)
        return byApp.keys.sorted().map { app in
            let appEntries = (byApp[app] ?? []).sorted(by: isOrderedBeforeWithinApp)
            let header = "\(app)   \(appEntries.count)"
            return .group(AppMenuGroup(headerTitle: header, rows: appEntries.map(row)))
        }
    }

    /// `writeListTable`'s tiebreak once `app` already matches: slot, then
    /// slug.
    private static func isOrderedBeforeWithinApp(_ lhs: ListEntry, _ rhs: ListEntry) -> Bool {
        if lhs.slot != rhs.slot {
            return lhs.slot < rhs.slot
        }
        return lhs.slug < rhs.slug
    }

    /// Renders one entry: the state symbol, the slug, the first port
    /// found in `resources` right-aligned, and the flag-driven suffixes
    /// and disabling.
    private static func row(for entry: ListEntry) -> MenuRow {
        let flags = Set(entry.flags ?? [])
        let symbol = stateSymbols[entry.state] ?? unknownStateSymbol

        let leadingText = "\(symbol) \(entry.slug)"
        let portText = firstPort(in: entry.resources).map { ":\($0)" }

        var suffixes: [String] = []
        if flags.contains("stale") {
            suffixes.append("(stale)")
        }
        if flags.contains("foreign") {
            suffixes.append("(owned by \(entry.owner))")
        }
        if flags.contains("unverifiable") {
            suffixes.append("(path not visible from here)")
        }
        let trailingText = suffixes.isEmpty ? nil : suffixes.joined(separator: " ")

        // path_visible == false disables the row whatever the flags say:
        // that is the container case, and opening it would silently do
        // nothing (PLAN.md).
        let isEnabled = entry.pathVisible && !flags.contains("stale")

        return MenuRow(
            leadingText: leadingText,
            portText: portText,
            trailingText: trailingText,
            tooltip: entry.description,
            isEnabled: isEnabled,
            path: entry.path
        )
    }

    /// The first port-type resource, chosen by sorting `resources`' keys
    /// first: a Swift `Dictionary` carries no order of its own, so
    /// picking "the first one" needs a deterministic tiebreak or the
    /// result would vary run to run for an entry with several port
    /// resources.
    private static func firstPort(in resources: [String: Resolved]) -> Int? {
        for key in resources.keys.sorted() {
            if case .port(let value) = resources[key]?.value {
                return value
            }
        }
        return nil
    }
}
