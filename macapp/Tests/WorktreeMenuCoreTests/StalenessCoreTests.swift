import XCTest
@testable import WorktreeMenuCore

/// Exercises `StalenessCore`'s age formatting and the combined
/// stale-snapshot line, including the just-now and hours-old boundary
/// cases the acceptance checks call for (macapp phase 5).
final class StalenessCoreTests: XCTestCase {
    private let reference = Date(timeIntervalSince1970: 1_000_000)

    // MARK: formatAge

    func testJustNowUnderAMinute() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(30))
        XCTAssertEqual(age, "just now")
    }

    func testZeroSecondsIsJustNow() {
        XCTAssertEqual(StalenessCore.formatAge(from: reference, to: reference), "just now")
    }

    func testAFutureDateClampsToJustNowRatherThanGoingNegative() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(-30))
        XCTAssertEqual(age, "just now")
    }

    func testMinutesAgo() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(5 * 60))
        XCTAssertEqual(age, "5 minutes ago")
    }

    func testOneMinuteIsSingular() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(60))
        XCTAssertEqual(age, "1 minute ago")
    }

    func testHoursOld() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(3 * 3600))
        XCTAssertEqual(age, "3 hours ago")
    }

    func testOneHourIsSingular() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(3600))
        XCTAssertEqual(age, "1 hour ago")
    }

    func testJustUnderAnHourIsStillMinutes() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(59 * 60))
        XCTAssertEqual(age, "59 minutes ago")
    }

    func testDaysOld() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(2 * 86400))
        XCTAssertEqual(age, "2 days ago")
    }

    func testJustUnderADayIsStillHours() {
        let age = StalenessCore.formatAge(from: reference, to: reference.addingTimeInterval(23 * 3600))
        XCTAssertEqual(age, "23 hours ago")
    }

    // MARK: staleLine

    func testStaleLineCombinesAgeAndReason() {
        let line = StalenessCore.staleLine(
            lastUpdated: reference,
            now: reference.addingTimeInterval(2 * 3600),
            reason: "Coordinator unreachable"
        )
        XCTAssertEqual(line, "Showing data from 2 hours ago — Coordinator unreachable")
    }

    func testStaleLineJustNowCase() {
        let line = StalenessCore.staleLine(
            lastUpdated: reference,
            now: reference.addingTimeInterval(5),
            reason: "wt list timed out"
        )
        XCTAssertEqual(line, "Showing data from just now — wt list timed out")
    }
}
