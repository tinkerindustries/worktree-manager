import Foundation

/// The badge and report-text decisions built from a `wt doctor --json`
/// decode. Pure: no `NSStatusItem`, no `NSAlert` — only what the AppKit
/// layer should show, computed from the report alone (phase 5 of
/// macapp/PLAN.md).
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

    /// Renders the full report as plain text: one line per finding
    /// (level, the app/slug it is about when present, the message, and
    /// the remedy when there is one), followed by the bounded-coverage
    /// notes. This is what the "open the full report" menu item shows.
    public static func formatReport(_ result: DoctorResult) -> String {
        if result.findings.isEmpty, (result.notes ?? []).isEmpty {
            return "No findings."
        }
        var lines: [String] = []
        for finding in result.findings {
            var line = "[\(finding.level.rawValue)]"
            if let app = finding.app, let slug = finding.slug {
                line += " \(app)/\(slug):"
            } else if let app = finding.app {
                line += " \(app):"
            }
            line += " \(finding.message)"
            if let remedy = finding.remedy, !remedy.isEmpty {
                line += "\n    fix: \(remedy)"
            }
            lines.append(line)
        }
        if let notes = result.notes, !notes.isEmpty {
            if !lines.isEmpty {
                lines.append("")
            }
            lines.append(contentsOf: notes.map { "note: \($0)" })
        }
        return lines.joined(separator: "\n")
    }
}
