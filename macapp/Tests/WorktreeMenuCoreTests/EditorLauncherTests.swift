import XCTest
@testable import WorktreeMenuCore

/// Exercises `EditorLauncher`'s two pure halves directly: template
/// substitution/percent-encoding, and modifier-key routing. Neither
/// touches AppKit — that is the phase-3 architecture requirement
/// (macapp/PLAN.md).
final class EditorLauncherTests: XCTestCase {
    // MARK: template substitution, one per built-in scheme

    func testVSCodeTemplateSubstitutesThePath() {
        let url = EditorLauncher.url(forTemplate: "vscode://file{path}", path: "/Users/geoff/repo")
        XCTAssertEqual(url?.absoluteString, "vscode://file/Users/geoff/repo")
    }

    func testCursorTemplateSubstitutesThePath() {
        let url = EditorLauncher.url(forTemplate: "cursor://file{path}", path: "/Users/geoff/repo")
        XCTAssertEqual(url?.absoluteString, "cursor://file/Users/geoff/repo")
    }

    func testZedTemplateSubstitutesThePath() {
        let url = EditorLauncher.url(forTemplate: "zed://file{path}", path: "/Users/geoff/repo")
        XCTAssertEqual(url?.absoluteString, "zed://file/Users/geoff/repo")
    }

    func testJetBrainsTemplateSubstitutesThePath() {
        let url = EditorLauncher.url(
            forTemplate: "jetbrains://idea/navigate/reference?path={path}",
            path: "/Users/geoff/repo"
        )
        XCTAssertEqual(url?.absoluteString, "jetbrains://idea/navigate/reference?path=/Users/geoff/repo")
    }

    func testBuiltInsListsExactlyTheFourDocumentedSchemes() {
        let templates = EditorLauncher.builtIns.map(\.template)
        XCTAssertEqual(templates, [
            "vscode://file{path}",
            "cursor://file{path}",
            "zed://file{path}",
            "jetbrains://idea/navigate/reference?path={path}",
        ])
    }

    // MARK: percent-encoding

    func testPercentEncodesSpacesInThePath() {
        let url = EditorLauncher.url(forTemplate: "vscode://file{path}", path: "/Users/geoff/My Worktrees/app")
        XCTAssertEqual(url?.absoluteString, "vscode://file/Users/geoff/My%20Worktrees/app")
    }

    func testPercentEncodesNonASCIICharactersInThePath() {
        let url = EditorLauncher.url(forTemplate: "vscode://file{path}", path: "/Users/geoff/café")
        XCTAssertEqual(url?.absoluteString, "vscode://file/Users/geoff/caf%C3%A9")
    }

    func testEncodesQueryDelimitersSoAQueryTemplateSurvives() {
        // The JetBrains template puts the path in a query parameter. An
        // unencoded ampersand there would end the parameter early and
        // hand the editor a truncated path.
        let url = EditorLauncher.url(
            forTemplate: "jetbrains://idea/navigate/reference?path={path}",
            path: "/Users/geoff/foo & bar/app"
        )
        XCTAssertEqual(
            url?.absoluteString,
            "jetbrains://idea/navigate/reference?path=/Users/geoff/foo%20%26%20bar/app"
        )
    }

    func testEncodesPlusAndEqualsInThePath() {
        // A query parser reads a bare plus as a space and a bare equals
        // as the start of a value.
        let url = EditorLauncher.url(
            forTemplate: "jetbrains://idea/navigate/reference?path={path}",
            path: "/Users/geoff/c++/a=b"
        )
        XCTAssertEqual(
            url?.absoluteString,
            "jetbrains://idea/navigate/reference?path=/Users/geoff/c%2B%2B/a%3Db"
        )
    }

    func testDoesNotEncodeThePathSeparator() {
        // The slashes that separate path components must survive
        // encoding, or the result would not point at the right directory.
        let url = EditorLauncher.url(forTemplate: "vscode://file{path}", path: "/a/b/c")
        XCTAssertEqual(url?.absoluteString, "vscode://file/a/b/c")
    }

    // MARK: malformed and empty templates

    func testEmptyTemplateProducesNoURL() {
        XCTAssertNil(EditorLauncher.url(forTemplate: "", path: "/Users/geoff/repo"))
    }

    func testTemplateWithoutAPlaceholderProducesNoURL() {
        // A template with nothing to substitute into is not usable —
        // it would open the same location for every worktree.
        XCTAssertNil(EditorLauncher.url(forTemplate: "vscode://file", path: "/Users/geoff/repo"))
    }

    func testTemplateWithNoSchemeProducesNoURL() {
        // A bare path with a placeholder but no "scheme:" prefix is not
        // something NSWorkspace could open as an application URL.
        XCTAssertNil(EditorLauncher.url(forTemplate: "{path}", path: "/Users/geoff/repo"))
    }

    // MARK: modifier routing

    func testPlainClickOpensTheEditor() {
        XCTAssertEqual(EditorLauncher.action(for: []), .openEditor)
    }

    func testOptionOpensTerminal() {
        XCTAssertEqual(EditorLauncher.action(for: [.option]), .openTerminal)
    }

    func testShiftRevealsInFinder() {
        XCTAssertEqual(EditorLauncher.action(for: [.shift]), .revealInFinder)
    }

    func testOptionAndShiftTogetherRevealsInFinder() {
        // Documented precedence (EditorLauncher.swift): shift wins over
        // option, because revealing in Finder has no side effect beyond
        // changing focus, while opening Terminal starts a new process.
        XCTAssertEqual(EditorLauncher.action(for: [.option, .shift]), .revealInFinder)
    }

    // MARK: resolving which template a click should use

    private let vscode = EditorDefinition(id: "vscode", displayName: "VS Code", scheme: "vscode", template: "vscode://file{path}")
    private let zed = EditorDefinition(id: "zed", displayName: "Zed", scheme: "zed", template: "zed://file{path}")

    func testResolvesTheSelectedInstalledEditor() {
        let template = EditorLauncher.resolveTemplate(selectedID: "zed", customTemplate: nil, installed: [vscode, zed])
        XCTAssertEqual(template, "zed://file{path}")
    }

    func testFallsBackToTheFirstInstalledEditorWhenTheSelectionIsNoLongerInstalled() {
        let template = EditorLauncher.resolveTemplate(selectedID: "cursor", customTemplate: nil, installed: [vscode, zed])
        XCTAssertEqual(template, "vscode://file{path}")
    }

    func testUsesTheCustomTemplateWhenTheCustomEscapeHatchIsSelected() {
        let template = EditorLauncher.resolveTemplate(
            selectedID: EditorLauncher.customEditorID,
            customTemplate: "myeditor://open?file={path}",
            installed: [vscode]
        )
        XCTAssertEqual(template, "myeditor://open?file={path}")
    }

    func testFallsBackWhenTheCustomEscapeHatchIsSelectedButEmpty() {
        let template = EditorLauncher.resolveTemplate(
            selectedID: EditorLauncher.customEditorID,
            customTemplate: "",
            installed: [vscode]
        )
        XCTAssertEqual(template, "vscode://file{path}")
    }

    func testResolvesToNilWhenNothingIsInstalledAndNoCustomTemplateIsSet() {
        let template = EditorLauncher.resolveTemplate(selectedID: nil, customTemplate: nil, installed: [])
        XCTAssertNil(template)
    }
}
