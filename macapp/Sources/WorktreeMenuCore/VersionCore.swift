import Foundation

/// `wt --version`'s parsed line: `internal/cli/version_test.go` pins the
/// format to exactly `"wt <version> (<commit>)"` on one line, no JSON.
public struct WtVersion: Equatable, Sendable {
    public let version: String
    public let commit: String

    public init(version: String, commit: String) {
        self.version = version
        self.commit = commit
    }
}

/// What running `wt --version` produced: the parsed version, a line that
/// ran but did not match the expected shape, or the reason it could not
/// run at all. Kept as three distinct cases — rather than folding
/// "ran but unparseable" into "failed" — so the version menu item can say
/// specifically which one it is (macapp task, phase 5).
public enum WtVersionOutcome: Equatable, Sendable {
    case version(WtVersion)
    case unparseable(raw: String)
    case unavailable(reason: String)
}

/// Builds the version menu item's text and parses `wt --version`'s
/// output. Pure: everything here is string handling, no process launch
/// (that is `WtClient.version()`) and no `Bundle` lookup (that is
/// `WorktreeMenu`'s job, since `WorktreeMenuCore` cannot import AppKit).
public enum VersionCore {
    /// Parses `"wt <version> (<commit>)"`. Returns `nil` for anything
    /// else — a wrapped error message, an empty line, a shape the format
    /// might grow into later — so the caller reports "wt's version
    /// output changed" instead of showing a garbled string.
    public static func parseVersionLine(_ line: String) -> WtVersion? {
        let trimmed = line.trimmingCharacters(in: .whitespacesAndNewlines)
        guard trimmed.hasPrefix("wt ") else { return nil }
        let rest = trimmed.dropFirst(3)
        guard rest.hasSuffix(")"), let openParen = rest.lastIndex(of: "(") else { return nil }
        let version = rest[rest.startIndex..<openParen].trimmingCharacters(in: .whitespaces)
        let commitStart = rest.index(after: openParen)
        let commitEnd = rest.index(before: rest.endIndex)
        guard commitStart <= commitEnd else { return nil }
        let commit = String(rest[commitStart..<commitEnd])
        guard !version.isEmpty, !commit.isEmpty else { return nil }
        return WtVersion(version: version, commit: commit)
    }

    /// The version menu item's title. The app's own version and `wt`'s
    /// are reported independently, so when one is missing the text names
    /// which one moved rather than failing outright: an app version that
    /// cannot be read still leaves `wt`'s version visible, and a `wt`
    /// output that no longer parses still leaves the app's version
    /// visible, with a note that `wt`'s output changed rather than a
    /// blanket "version unavailable" covering both.
    public static func versionMenuTitle(appVersion: String?, wt: WtVersionOutcome) -> String {
        let appText = appVersion.map { "app \($0)" } ?? "app version unknown"
        let wtText: String
        switch wt {
        case .version(let parsed):
            wtText = "wt \(parsed.version)"
        case .unparseable:
            wtText = "wt version: unexpected output (wt may have changed)"
        case .unavailable(let reason):
            wtText = "wt version unavailable (\(reason))"
        }
        return "\(appText) · \(wtText)"
    }
}
