import Foundation

/// One editor the app knows how to open a worktree in: a stable id for
/// `UserDefaults`, a name for the preferences list, the URL scheme
/// `NSWorkspace.shared.urlForApplication(toOpen:)` probes for, and the
/// template `EditorLauncher.url(forTemplate:path:)` fills in. Detection
/// against an installed application is an AppKit call and lives in
/// `WorktreeMenu`; this struct is only the policy of which editors exist
/// and what their templates look like (PLAN.md).
public struct EditorDefinition: Equatable, Sendable {
    public let id: String
    public let displayName: String
    public let scheme: String
    public let template: String

    public init(id: String, displayName: String, scheme: String, template: String) {
        self.id = id
        self.displayName = displayName
        self.scheme = scheme
        self.template = template
    }
}

/// The two modifier keys the click handler cares about, expressed as the
/// app's own option set rather than `NSEvent.ModifierFlags`.
/// `WorktreeMenuCore` stays AppKit-free this way: `WorktreeMenu` translates
/// `NSEvent.modifierFlags` into this set at the point of the click, and
/// everything downstream of that translation is pure and testable.
public struct LaunchModifiers: OptionSet, Equatable, Sendable {
    public let rawValue: UInt8

    public init(rawValue: UInt8) {
        self.rawValue = rawValue
    }

    public static let option = LaunchModifiers(rawValue: 1 << 0)
    public static let shift = LaunchModifiers(rawValue: 1 << 1)
}

/// What a click on an enabled row should do, chosen from the held
/// modifier keys alone.
public enum RowAction: Equatable, Sendable {
    case openEditor
    case openTerminal
    case revealInFinder
}

/// Builds editor URLs from templates and routes a click's modifier keys
/// to an action. Both halves are pure functions (PLAN.md's phase-3
/// requirement): no `NSWorkspace`, `NSEvent`, or `URL` opening happens
/// here, only the string and bit-flag arithmetic that decides what
/// `WorktreeMenu` should do next.
public enum EditorLauncher {
    /// The four editors the app recognises out of the box. Offered only
    /// when `NSWorkspace.shared.urlForApplication(toOpen:)` finds an
    /// application registered for the scheme — that probe is
    /// `WorktreeMenu`'s job, not this list's.
    public static let builtIns: [EditorDefinition] = [
        EditorDefinition(id: "vscode", displayName: "Visual Studio Code", scheme: "vscode", template: "vscode://file{path}"),
        EditorDefinition(id: "cursor", displayName: "Cursor", scheme: "cursor", template: "cursor://file{path}"),
        EditorDefinition(id: "zed", displayName: "Zed", scheme: "zed", template: "zed://file{path}"),
        EditorDefinition(
            id: "jetbrains",
            displayName: "JetBrains IDE",
            scheme: "jetbrains",
            template: "jetbrains://idea/navigate/reference?path={path}"
        ),
    ]

    /// The id `UserDefaults` records when the user has typed their own
    /// template instead of picking one of `builtIns` — the escape hatch
    /// PLAN.md calls for when none of the four match the user's editor.
    public static let customEditorID = "custom"

    /// The placeholder a template substitutes the worktree's path into.
    private static let pathPlaceholder = "{path}"

    /// Fills `path`, percent-encoded, into `template` and parses the
    /// result as a URL. Returns `nil` rather than throwing or crashing
    /// when the template is empty, holds no placeholder to substitute
    /// into, or does not parse into a URL with a scheme — a bare path or
    /// stray text is not something `NSWorkspace.shared.open` could act
    /// on anyway, so treating it the same as "no URL" keeps the caller's
    /// job to one check.
    public static func url(forTemplate template: String, path: String) -> URL? {
        guard !template.isEmpty, template.contains(pathPlaceholder) else {
            return nil
        }
        guard let encodedPath = path.addingPercentEncoding(withAllowedCharacters: .urlPathAllowed) else {
            return nil
        }
        let filled = template.replacingOccurrences(of: pathPlaceholder, with: encodedPath)
        guard let url = URL(string: filled), url.scheme != nil else {
            return nil
        }
        return url
    }

    /// The template a click should actually use: the stored selection
    /// when it is still installed, the stored custom template when the
    /// selection is the escape hatch and the text is non-empty,
    /// otherwise the first installed editor, otherwise `nil`. Which
    /// editors are installed is `WorktreeMenu`'s `NSWorkspace` probe; this
    /// function only combines that list with what is stored in
    /// `UserDefaults`, so it stays pure and testable here.
    public static func resolveTemplate(
        selectedID: String?,
        customTemplate: String?,
        installed: [EditorDefinition]
    ) -> String? {
        if selectedID == customEditorID {
            if let customTemplate, !customTemplate.isEmpty {
                return customTemplate
            }
        } else if let selectedID, let match = installed.first(where: { $0.id == selectedID }) {
            return match.template
        }
        return installed.first?.template
    }

    /// Routes a click's modifier keys to the action it requests. Plain
    /// click opens the editor; option opens Terminal; shift reveals in
    /// Finder. When both option and shift are held, shift wins: revealing
    /// in Finder has no side effect beyond changing focus, while opening
    /// Terminal starts a new process, so the action with no side effect
    /// takes priority when both are requested at once.
    public static func action(for modifiers: LaunchModifiers) -> RowAction {
        if modifiers.contains(.shift) {
            return .revealInFinder
        }
        if modifiers.contains(.option) {
            return .openTerminal
        }
        return .openEditor
    }
}
