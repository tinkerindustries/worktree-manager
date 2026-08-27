import XCTest
@testable import WorktreeMenuCore

/// Exercises `VersionCore`'s parsing of `wt --version`'s output and the
/// version menu item's text, including the mismatch case where one half
/// cannot be read (macapp phase 5).
final class VersionCoreTests: XCTestCase {
    // MARK: parseVersionLine

    func testParsesTheExactFormatWtVersionPrints() {
        let parsed = VersionCore.parseVersionLine("wt 0.2.0 (abc1234)\n")
        XCTAssertEqual(parsed, WtVersion(version: "0.2.0", commit: "abc1234"))
    }

    func testParsesUnknownCommit() {
        let parsed = VersionCore.parseVersionLine("wt 0.2.0 (unknown)")
        XCTAssertEqual(parsed, WtVersion(version: "0.2.0", commit: "unknown"))
    }

    func testRejectsALineWithNoWtPrefix() {
        XCTAssertNil(VersionCore.parseVersionLine("0.2.0 (abc1234)"))
    }

    func testRejectsALineWithNoParenthesizedCommit() {
        XCTAssertNil(VersionCore.parseVersionLine("wt 0.2.0"))
    }

    func testRejectsEmptyOutput() {
        XCTAssertNil(VersionCore.parseVersionLine(""))
    }

    func testRejectsGarbledOutput() {
        XCTAssertNil(VersionCore.parseVersionLine("wt: command not found"))
    }

    // MARK: versionMenuTitle — the mismatch/failure messages

    func testTitleWhenBothVersionsAreKnown() {
        let title = VersionCore.versionMenuTitle(
            appVersion: "0.1.0",
            wt: .version(WtVersion(version: "0.2.0", commit: "abc1234"))
        )
        XCTAssertEqual(title, "app 0.1.0 · wt 0.2.0")
    }

    func testTitleNamesWtWhenItsOutputDoesNotParse() {
        // The app's own version is still shown; only wt's half is
        // reported as having moved.
        let title = VersionCore.versionMenuTitle(appVersion: "0.1.0", wt: .unparseable(raw: "??"))
        XCTAssertTrue(title.contains("app 0.1.0"))
        XCTAssertTrue(title.lowercased().contains("wt"))
        XCTAssertTrue(title.lowercased().contains("unexpected"))
    }

    func testTitleNamesWtWhenItCouldNotRunAtAll() {
        let title = VersionCore.versionMenuTitle(appVersion: "0.1.0", wt: .unavailable(reason: "wt not found"))
        XCTAssertTrue(title.contains("app 0.1.0"))
        XCTAssertTrue(title.contains("wt not found"))
    }

    func testTitleNamesTheAppWhenItsOwnVersionIsUnknown() {
        // The reverse case: wt resolves fine, but the app's own version
        // could not be read. The message still names which half failed.
        let title = VersionCore.versionMenuTitle(
            appVersion: nil,
            wt: .version(WtVersion(version: "0.2.0", commit: "abc1234"))
        )
        XCTAssertTrue(title.lowercased().contains("app version unknown"))
        XCTAssertTrue(title.contains("wt 0.2.0"))
    }
}
