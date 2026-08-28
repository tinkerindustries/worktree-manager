import Foundation

/// The badge, the menu line and the structure of the report window, built
/// from a `wt doctor --json` decode. Pure: no `NSStatusItem`, no window —
/// only what the AppKit layer should show, computed from the report alone
/// (phase 5 of macapp/PLAN.md).
///
/// `report` is the shape the window renders: findings grouped by the app
/// they belong to, worst first, each stated in one line with its detail and
/// its fix underneath. The grouping reads the coordinator's own `app`,
/// `level`, `summary` and `details` fields rather than pattern-matching the
/// message text, so a reworded message never rearranges the window.
public enum DoctorCore {
    /// Whether the status icon should carry a badge: doctor found
    /// something above `info`. An info-only report — or an empty one —
    /// gets no badge, since an info finding is an observation rather
    /// than something to fix.
    public static func shouldBadge(_ result: DoctorResult) -> Bool {
        result.findings.contains { $0.level > .info }
    }

    /// The worst level present in the report, or `nil` for an empty one.
    /// Used to word the summary line ("1 error", "2 warnings") without
    /// walking the findings twice.
    public static func highestLevel(_ result: DoctorResult) -> DoctorLevel? {
        result.findings.map(\.level).max()
    }

    /// A one-line summary for the menu: how many findings at or above
    /// `warning`, or "no findings" when the report is clean. Never
    /// mentions info-level findings by count — they are not the reason
    /// for the badge and would make a clean-ish report look alarming.
    public static func summaryLine(_ result: DoctorResult) -> String {
        let actionable = result.findings.filter { $0.level > .info }
        if actionable.isEmpty {
            return "doctor: no findings"
        }
        let errors = actionable.filter { $0.level == .error }.count
        let warnings = actionable.filter { $0.level == .warning }.count
        var parts: [String] = []
        if errors > 0 {
            parts.append(errors == 1 ? "1 error" : "\(errors) errors")
        }
        if warnings > 0 {
            parts.append(warnings == 1 ? "1 warning" : "\(warnings) warnings")
        }
        return "doctor: " + parts.joined(separator: ", ")
    }

    /// The heading a finding is filed under: its app, or the coordinator
    /// itself for the findings that belong to no one app — the band
    /// ledger, the helper binaries, the registry.
    public static let coordinatorSection = "The coordinator"

    /// Builds the report the window renders. Sections are ordered by their
    /// worst finding and then by name, and the rows inside each section the
    /// same way, so the first thing on screen is the worst thing found.
    public static func report(_ result: DoctorResult) -> DoctorReport {
        var rowsBySection: [String: [DoctorRow]] = [:]
        for finding in result.findings {
            let section = finding.app ?? coordinatorSection
            rowsBySection[section, default: []].append(row(for: finding))
        }
        let sections = rowsBySection
            .map { DoctorSection(title: $0.key, rows: $0.value.sorted(by: rowOrder)) }
            .sorted(by: sectionOrder)
        return DoctorReport(
            headline: headline(result),
            sections: sections,
            notes: result.notes ?? []
        )
    }

    /// The window's title line: what doctor found, in words. A clean report
    /// says so plainly rather than showing a zero.
    public static func headline(_ result: DoctorResult) -> String {
        if result.findings.isEmpty {
            return "Nothing to fix"
        }
        var parts: [String] = []
        for level in [DoctorLevel.error, .warning, .info] {
            let count = result.findings.filter { $0.level == level }.count
            guard count > 0 else { continue }
            parts.append("\(count) \(noun(level, count: count))")
        }
        return list(parts)
    }

    /// Renders the report as plain text, in the same order and with the
    /// same words the window shows. This is what the window's "Copy report"
    /// puts on the pasteboard, so what a person pastes into an issue is
    /// what they were looking at.
    public static func formatReport(_ result: DoctorResult) -> String {
        let report = report(result)
        if report.sections.isEmpty, report.notes.isEmpty {
            return "No findings."
        }
        var lines: [String] = [report.headline]
        for section in report.sections {
            lines.append("")
            lines.append(section.title)
            for row in section.rows {
                lines.append("  [\(row.level.rawValue)] \(row.title)")
                if let message = row.message {
                    lines.append("      \(message)")
                }
                for detail in row.details {
                    lines.append("      - \(detail)")
                }
                if let remedy = row.remedy {
                    lines.append("      fix: \(remedy)")
                }
            }
        }
        if !report.notes.isEmpty {
            lines.append("")
            lines.append("What doctor could not check:")
            lines.append(contentsOf: report.notes.map { "  note: \($0)" })
        }
        return lines.joined(separator: "\n")
    }

    // MARK: building one row

    /// One finding as the window shows it. The title is the coordinator's
    /// own summary, prefixed with the worktree when the finding is about
    /// one; `message` is carried only when it says more than the title, so
    /// a row is never the same sentence twice.
    private static func row(for finding: DoctorFinding) -> DoctorRow {
        let summary = finding.summary.flatMap { $0.isEmpty ? nil : $0 } ?? finding.message
        var title = summary
        if let slug = finding.slug, !slug.isEmpty {
            title = "\(slug): \(summary)"
        }
        let message = finding.message == summary ? nil : finding.message
        return DoctorRow(
            level: finding.level,
            title: title,
            message: message,
            details: finding.details ?? [],
            remedy: finding.remedy.flatMap { $0.isEmpty ? nil : $0 },
            path: finding.path.flatMap { $0.isEmpty ? nil : $0 } ?? finding.repo
        )
    }

    // MARK: ordering

    private static func rowOrder(_ a: DoctorRow, _ b: DoctorRow) -> Bool {
        if a.level != b.level { return a.level > b.level }
        return a.title.localizedStandardCompare(b.title) == .orderedAscending
    }

    private static func sectionOrder(_ a: DoctorSection, _ b: DoctorSection) -> Bool {
        let worstA = a.rows.map(\.level).max() ?? .info
        let worstB = b.rows.map(\.level).max() ?? .info
        if worstA != worstB { return worstA > worstB }
        return a.title.localizedStandardCompare(b.title) == .orderedAscending
    }

    // MARK: wording

    private static func noun(_ level: DoctorLevel, count: Int) -> String {
        let singular: String
        switch level {
        case .error: singular = "error"
        case .warning: singular = "warning"
        case .info: singular = "observation"
        }
        return count == 1 ? singular : singular + "s"
    }

    /// Joins the counts the way a person would say them: "1 error",
    /// "1 error and 2 warnings", "1 error, 2 warnings and 3 observations".
    private static func list(_ parts: [String]) -> String {
        switch parts.count {
        case 0: return "Nothing to fix"
        case 1: return parts[0]
        default:
            return parts.dropLast().joined(separator: ", ") + " and " + parts[parts.count - 1]
        }
    }
}

/// One finding, ready to render: the level that colours it, the one line
/// that names it, the fuller message when there is more to say, the items
/// behind it, the command that fixes it, and the path a "Show in Finder"
/// would open.
public struct DoctorRow: Equatable, Sendable {
    public let level: DoctorLevel
    public let title: String
    public let message: String?
    public let details: [String]
    public let remedy: String?
    public let path: String?

    public init(
        level: DoctorLevel,
        title: String,
        message: String? = nil,
        details: [String] = [],
        remedy: String? = nil,
        path: String? = nil
    ) {
        self.level = level
        self.title = title
        self.message = message
        self.details = details
        self.remedy = remedy
        self.path = path
    }
}

/// One app's findings, under the app's name.
public struct DoctorSection: Equatable, Sendable {
    public let title: String
    public let rows: [DoctorRow]

    public init(title: String, rows: [DoctorRow]) {
        self.title = title
        self.rows = rows
    }
}

/// The whole report as the window renders it: the headline, the sections,
/// and the bounded-coverage notes — what doctor could not check, which the
/// window keeps collapsed because it is never the reason anybody opened it.
public struct DoctorReport: Equatable, Sendable {
    public let headline: String
    public let sections: [DoctorSection]
    public let notes: [String]

    public init(headline: String, sections: [DoctorSection], notes: [String]) {
        self.headline = headline
        self.sections = sections
        self.notes = notes
    }
}
