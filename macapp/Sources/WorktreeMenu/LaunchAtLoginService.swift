import ServiceManagement
import WorktreeMenuCore

/// The live binding to `SMAppService.mainApp` — the one system surface
/// the launch-at-login preference touches. Everything else about the
/// preference (the status text, the failure text) is pure and lives in
/// `WorktreeMenuCore.LaunchAtLoginPreference`, per PLAN.md's split.
struct SMAppServiceLaunchAtLogin: LaunchAtLoginRegistering {
    var currentStatus: LaunchAtLoginStatus {
        switch SMAppService.mainApp.status {
        case .enabled:
            return .enabled
        case .requiresApproval:
            return .requiresApproval
        case .notFound:
            return .notFound
        case .notRegistered:
            return .notRegistered
        @unknown default:
            return .notRegistered
        }
    }

    func register() throws {
        try SMAppService.mainApp.register()
    }

    func unregister() throws {
        try SMAppService.mainApp.unregister()
    }
}
