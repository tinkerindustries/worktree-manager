import XCTest
@testable import WorktreeMenuCore

/// Exercises `DoctorCore`'s severity mapping and badge decision directly
/// against `DoctorResult` values — no AppKit, no subprocess (macapp
/// phase 5).
final class DoctorCoreTests: XCTestCase {
    private func finding(level: DoctorLevel, message: String = "something") -> DoctorFinding {
        DoctorFinding(app: "worktree-manager", slug: "macapp", level: level, message: message, remedy: nil)
    }

    // MARK: shouldBadge

    func testEmptyReportDoesNotBadge() {
        XCTAssertFalse(DoctorCore.shouldBadge(DoctorResult(findings: [])))
    }

    func testInfoOnlyReportDoesNotBadge() {
        let result = DoctorResult(findings: [finding(level: .info), finding(level: .info)])
        XCTAssertFalse(DoctorCore.shouldBadge(result))
    }

    func testReportWithAWarningBadges() {
        let result = DoctorResult(findings: [finding(level: .info), finding(level: .warning)])
        XCTAssertTrue(DoctorCore.shouldBadge(result))
    }

    func testReportWithAnErrorBadges() {
        let result = DoctorResult(findings: [finding(level: .error)])
        XCTAssertTrue(DoctorCore.shouldBadge(result))
    }

    // MARK: highestLevel

    func testHighestLevelOfEmptyReportIsNil() {
        XCTAssertNil(DoctorCore.highestLevel(DoctorResult(findings: [])))
    }

    func testHighestLevelPicksTheWorstFinding() {
        let result = DoctorResult(findings: [finding(level: .warning), finding(level: .info), finding(level: .error)])
        XCTAssertEqual(DoctorCore.highestLevel(result), .error)
    }

    // MARK: level ordering

    func testLevelsOrderInfoBelowWarningBelowError() {
        XCTAssertLessThan(DoctorLevel.info, .warning)
        XCTAssertLessThan(DoctorLevel.warning, .error)
        XCTAssertLessThan(DoctorLevel.info, .error)
    }

    // MARK: summaryLine

    func testSummaryLineForCleanReport() {
        XCTAssertEqual(DoctorCore.summaryLine(DoctorResult(findings: [])), "doctor: no findings")
    }

    func testSummaryLineIgnoresInfoFindings() {
        let result = DoctorResult(findings: [finding(level: .info), finding(level: .info)])
        XCTAssertEqual(DoctorCore.summaryLine(result), "doctor: no findings")
    }

    func testSummaryLineCountsWarningsAndErrorsSeparately() {
        let result = DoctorResult(findings: [
            finding(level: .warning), finding(level: .warning), finding(level: .error),
        ])
        XCTAssertEqual(DoctorCore.summaryLine(result), "doctor: 1 error, 2 warnings")
    }

    // MARK: formatReport

    func testFormatReportOfEmptyResultSaysNoFindings() {
        XCTAssertEqual(DoctorCore.formatReport(DoctorResult(findings: [])), "No findings.")
    }

    func testFormatReportIncludesLevelAppSlugMessageAndRemedy() {
        let result = DoctorResult(findings: [
            DoctorFinding(app: "worktree-manager", slug: "macapp", level: .error, message: "port in use", remedy: "wt rm macapp"),
        ])
        let text = DoctorCore.formatReport(result)
        XCTAssertTrue(text.contains("[error]"))
        XCTAssertTrue(text.contains("worktree-manager/macapp"))
        XCTAssertTrue(text.contains("port in use"))
        XCTAssertTrue(text.contains("wt rm macapp"))
    }

    func testFormatReportAppendsNotes() {
        let result = DoctorResult(findings: [], notes: ["skipped: no gh"])
        XCTAssertTrue(DoctorCore.formatReport(result).contains("skipped: no gh"))
    }
}
