import XCTest
@testable import WorktreeMenuCore

/// Exercises the pure half of the launch-at-login preference: the status
/// and failure text (`LaunchAtLoginPreference`). The live half
/// (`SMAppServiceLaunchAtLogin`, in `WorktreeMenu`) calls the real
/// `SMAppService.mainApp` and is deliberately not exercised here — this
/// is the split PLAN.md's phase-4 note describes.
final class LaunchAtLoginTests: XCTestCase {
    func testEnabledStatusDescribesLaunchingAtLogin() {
        XCTAssertEqual(
            LaunchAtLoginPreference.statusDescription(.enabled),
            "Worktree Menu will launch at login."
        )
    }

    func testNotRegisteredStatusDescribesNotLaunching() {
        XCTAssertEqual(
            LaunchAtLoginPreference.statusDescription(.notRegistered),
            "Worktree Menu will not launch at login."
        )
    }

    func testRequiresApprovalStatusPointsAtSystemSettings() {
        XCTAssertEqual(
            LaunchAtLoginPreference.statusDescription(.requiresApproval),
            "Approval needed: turn this on in System Settings > General > Login Items."
        )
    }

    func testNotFoundStatusSaysUnavailable() {
        XCTAssertEqual(
            LaunchAtLoginPreference.statusDescription(.notFound),
            "Launch at login is not available for this build."
        )
    }

    func testFailureDescriptionNamesEnableWhenTurningOn() {
        let error = NSError(domain: "test", code: 1, userInfo: [NSLocalizedDescriptionKey: "denied"])
        XCTAssertEqual(
            LaunchAtLoginPreference.failureDescription(togglingOn: true, error: error),
            "Could not enable launch at login: denied"
        )
    }

    func testFailureDescriptionNamesDisableWhenTurningOff() {
        let error = NSError(domain: "test", code: 1, userInfo: [NSLocalizedDescriptionKey: "denied"])
        XCTAssertEqual(
            LaunchAtLoginPreference.failureDescription(togglingOn: false, error: error),
            "Could not disable launch at login: denied"
        )
    }
}

/// A fake `LaunchAtLoginRegistering` for exercising toggle logic without
/// touching `SMAppService`. Not currently used by `PreferencesWindowController`
/// tests (there is no AppKit test target for that class), but kept small
/// and available here alongside the protocol it fakes, matching
/// `WtClient`'s injectable-runner pattern elsewhere in this package.
final class FakeLaunchAtLoginRegistering: LaunchAtLoginRegistering {
    var currentStatus: LaunchAtLoginStatus
    var registerError: Error?
    var unregisterError: Error?
    private(set) var registerCallCount = 0
    private(set) var unregisterCallCount = 0

    init(currentStatus: LaunchAtLoginStatus = .notRegistered) {
        self.currentStatus = currentStatus
    }

    func register() throws {
        registerCallCount += 1
        if let registerError {
            throw registerError
        }
        currentStatus = .enabled
    }

    func unregister() throws {
        unregisterCallCount += 1
        if let unregisterError {
            throw unregisterError
        }
        currentStatus = .notRegistered
    }
}

final class FakeLaunchAtLoginRegisteringTests: XCTestCase {
    func testRegisterUpdatesStatusToEnabled() throws {
        let fake = FakeLaunchAtLoginRegistering(currentStatus: .notRegistered)
        try fake.register()
        XCTAssertEqual(fake.currentStatus, .enabled)
        XCTAssertEqual(fake.registerCallCount, 1)
    }

    func testUnregisterUpdatesStatusToNotRegistered() throws {
        let fake = FakeLaunchAtLoginRegistering(currentStatus: .enabled)
        try fake.unregister()
        XCTAssertEqual(fake.currentStatus, .notRegistered)
        XCTAssertEqual(fake.unregisterCallCount, 1)
    }

    func testRegisterThrowsTheConfiguredErrorAndLeavesStatusUnchanged() {
        let fake = FakeLaunchAtLoginRegistering(currentStatus: .notRegistered)
        fake.registerError = NSError(domain: "test", code: 1)
        XCTAssertThrowsError(try fake.register())
        XCTAssertEqual(fake.currentStatus, .notRegistered)
    }
}
