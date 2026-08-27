import Foundation

/// Formats the stale-snapshot line: a previous `wt list` result survives,
/// the most recent refresh failed, and the menu shows both the old data
/// and why it is old (phase 5 of macapp/PLAN.md — `AppDelegate.lastUpdated`
/// is tracked and never read until this).
public enum StalenessCore {
    /// How long ago `date` was, relative to `now`, in the coarsest unit
    /// that reads naturally: "just now" under a minute, then minutes,
    /// then hours, then days. A future `date` (a clock adjustment) is
    /// clamped to "just now" rather than printing a negative age.
    public static func formatAge(from date: Date, to now: Date) -> String {
        let seconds = max(0, now.timeIntervalSince(date))
        if seconds < 60 {
            return "just now"
        }
        let minutes = Int(seconds / 60)
        if minutes < 60 {
            return minutes == 1 ? "1 minute ago" : "\(minutes) minutes ago"
        }
        let hours = Int(seconds / 3600)
        if hours < 24 {
            return hours == 1 ? "1 hour ago" : "\(hours) hours ago"
        }
        let days = Int(seconds / 86400)
        return days == 1 ? "1 day ago" : "\(days) days ago"
    }

    /// The full stale-snapshot line: the age of the surviving snapshot and
    /// the reason the latest refresh failed, together — so the menu never
    /// shows one without the other.
    public static func staleLine(lastUpdated: Date, now: Date, reason: String) -> String {
        "Showing data from \(formatAge(from: lastUpdated, to: now)) — \(reason)"
    }
}
