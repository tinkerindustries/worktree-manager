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
        // The app is the section heading; the slug prefixes its own row.
        XCTAssertTrue(text.contains("worktree-manager"))
        XCTAssertTrue(text.contains("macapp: port in use"))
        XCTAssertTrue(text.contains("wt rm macapp"))
    }

    func testFormatReportAppendsNotes() {
        let result = DoctorResult(findings: [], notes: ["skipped: no gh"])
        XCTAssertTrue(DoctorCore.formatReport(result).contains("skipped: no gh"))
    }

    func testFormatReportListsDetailsUnderTheirFinding() {
        let result = DoctorResult(findings: [drift])
        let text = DoctorCore.formatReport(result)
        XCTAssertTrue(text.contains("docs/wt.md is out of date (2 fields have moved)"))
        XCTAssertTrue(text.contains("- band api: was 8200, now 8300"))
        XCTAssertTrue(text.contains("fix: re-run the generate phase"))
    }

    // MARK: report

    /// A repository-wide drift finding: no slug, a summary shorter than the
    /// message, and the moved fields as details.
    private var drift: DoctorFinding {
        DoctorFinding(
            app: "print-pipeline",
            kind: "generated-file-drift",
            level: .warning,
            summary: "docs/wt.md is out of date (2 fields have moved)",
            message: "the generated file docs/wt.md records spec fields that no longer match the spec",
            remedy: "re-run the generate phase",
            repo: "/repos/print-pipeline",
            path: "/repos/print-pipeline/docs/wt.md",
            details: ["band api: was 8200, now 8300", "resources: was api, now api2"]
        )
    }

    func testReportGroupsFindingsByApp() {
        let result = DoctorResult(findings: [
            finding(level: .warning),
            drift,
            DoctorFinding(app: "print-pipeline", slug: "wt-2", level: .info, message: "unverifiable"),
        ])
        let report = DoctorCore.report(result)
        XCTAssertEqual(report.sections.map(\.title), ["print-pipeline", "worktree-manager"])
        XCTAssertEqual(report.sections[0].rows.count, 2)
    }

    func testReportFilesAnAppLessFindingUnderTheCoordinator() {
        let result = DoctorResult(findings: [
            DoctorFinding(kind: "helpers-unreachable", level: .warning, message: "cannot reach gh"),
        ])
        let report = DoctorCore.report(result)
        XCTAssertEqual(report.sections.map(\.title), [DoctorCore.coordinatorSection])
    }

    func testReportOrdersSectionsAndRowsWorstFirst() {
        let result = DoctorResult(findings: [
            DoctorFinding(app: "quiet-app", level: .warning, message: "a warning"),
            DoctorFinding(app: "broken-app", slug: "b", level: .warning, message: "a warning"),
            DoctorFinding(app: "broken-app", slug: "a", level: .error, message: "an error"),
        ])
        let report = DoctorCore.report(result)
        // The app carrying the error sorts above the one carrying only a
        // warning, whatever the alphabet says.
        XCTAssertEqual(report.sections.map(\.title), ["broken-app", "quiet-app"])
        XCTAssertEqual(report.sections[0].rows.map(\.level), [.error, .warning])
    }

    func testRowPrefixesTheSlugAndCarriesTheDetails() {
        let report = DoctorCore.report(DoctorResult(findings: [
            DoctorFinding(app: "a", slug: "wt-1", level: .error,
                          summary: "its worktree directory is gone", message: "the worktree directory /gone is gone",
                          remedy: "wt rm --slug wt-1", path: "/gone"),
        ]))
        let row = report.sections[0].rows[0]
        XCTAssertEqual(row.title, "wt-1: its worktree directory is gone")
        XCTAssertEqual(row.message, "the worktree directory /gone is gone")
        XCTAssertEqual(row.remedy, "wt rm --slug wt-1")
        XCTAssertEqual(row.path, "/gone")
    }

    /// A coordinator too old to send a summary still renders: the message
    /// becomes the title, and the row does not then repeat it as its body.
    func testRowFallsBackToTheMessageWhenNoSummaryIsSent() {
        let report = DoctorCore.report(DoctorResult(findings: [
            DoctorFinding(app: "a", level: .warning, message: "something moved"),
        ]))
        let row = report.sections[0].rows[0]
        XCTAssertEqual(row.title, "something moved")
        XCTAssertNil(row.message)
    }

    func testRowFallsBackToTheRepoWhenTheFindingNamesNoPath() {
        let report = DoctorCore.report(DoctorResult(findings: [
            DoctorFinding(app: "a", level: .warning, message: "m", repo: "/repos/a"),
        ]))
        XCTAssertEqual(report.sections[0].rows[0].path, "/repos/a")
    }

    // MARK: headline

    func testHeadlineOfACleanReport() {
        XCTAssertEqual(DoctorCore.headline(DoctorResult(findings: [])), "Nothing to fix")
    }

    func testHeadlineCountsEveryLevelIncludingObservations() {
        let result = DoctorResult(findings: [
            finding(level: .error), finding(level: .warning), finding(level: .warning), finding(level: .info),
        ])
        XCTAssertEqual(DoctorCore.headline(result), "1 error, 2 warnings and 1 observation")
    }

    func testHeadlineOfASingleFinding() {
        XCTAssertEqual(DoctorCore.headline(DoctorResult(findings: [finding(level: .error)])), "1 error")
    }
}
