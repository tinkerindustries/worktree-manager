import Foundation

/// Whether the app is registered to launch at login, in `SMAppService`'s
/// own terms. Defined here rather than by importing `ServiceManagement`
/// into every caller, so the mapping from status to preference text is
/// testable without touching the real service — the phase-4 shape of
/// PLAN.md's rule that pure logic lives in `WorktreeMenuCore` and only
/// the code that must touch a live system surface goes in the executable.
public enum LaunchAtLoginStatus: Equatable, Sendable {
    /// Registered and will launch at login.
    case enabled
    /// Not registered.
    case notRegistered
    /// Registered, but the user has not approved it in System Settings >
    /// General > Login Items. `SMAppService` cannot flip this on for
    /// itself — only the user can, outside the app.
    case requiresApproval
    /// The app is not a shape `SMAppService` can register (not running
    /// from an installed `.app`, or missing the launch agent
    /// configuration). Distinct from `notRegistered` so the preference
    /// can say the feature is unavailable rather than implying a toggle
    /// would work.
    case notFound
}

/// The live half of the launch-at-login preference: reading the current
/// registration and changing it. `WorktreeMenu` (the executable) supplies
/// the `SMAppService.mainApp`-backed implementation; tests supply a fake,
/// so the toggle and status-text logic below is exercised without ever
/// calling into `SMAppService` (PLAN.md's "handle the failure case" —
/// registration can throw, including because the user denied it).
public protocol LaunchAtLoginRegistering {
    var currentStatus: LaunchAtLoginStatus { get }
    func register() throws
    func unregister() throws
}

/// The user-facing text for each `LaunchAtLoginStatus` and for a failed
/// registration attempt. Kept as a pure mapping so the preferences window
/// says the right thing without a live `SMAppService` in the test target.
public enum LaunchAtLoginPreference {
    /// Describes the current registration state, for a status label next
    /// to the launch-at-login checkbox.
    public static func statusDescription(_ status: LaunchAtLoginStatus) -> String {
        switch status {
        case .enabled:
            return "Worktree Menu will launch at login."
        case .notRegistered:
            return "Worktree Menu will not launch at login."
        case .requiresApproval:
            return "Approval needed: turn this on in System Settings > General > Login Items."
        case .notFound:
            return "Launch at login is not available for this build."
        }
    }

    /// Describes a failed `register()`/`unregister()` call. `SMAppService`
    /// throws an `NSError` whose `localizedDescription` already names the
    /// reason (denied in System Settings, or the launch agent
    /// configuration is missing); this just labels which direction failed.
    public static func failureDescription(togglingOn: Bool, error: Error) -> String {
        let verb = togglingOn ? "enable" : "disable"
        return "Could not \(verb) launch at login: \(error.localizedDescription)"
    }
}
