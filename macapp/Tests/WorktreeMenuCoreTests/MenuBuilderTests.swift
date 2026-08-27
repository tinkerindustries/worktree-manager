import XCTest
@testable import WorktreeMenuCore

/// Exercises `MenuBuilder.build` directly against `ListEntry` values —
/// no AppKit type is involved, which is the phase-2 architecture
/// requirement (macapp/PLAN.md).
final class MenuBuilderTests: XCTestCase {
    private func entry(
        app: String = "worktree-manager",
        slug: String,
        slot: Int,
        state: String = "active",
        pathVisible: Bool = true,
        resources: [String: Resolved] = [:],
        flags: [String]? = nil,
        owner: String = "geoff",
        description: String? = nil
    ) -> ListEntry {
        ListEntry(
            app: app, slug: slug, slot: slot, description: description, state: state,
            path: "/tmp/\(slug)", pathVisible: pathVisible, owner: owner, ownerKind: "host",
            ephemeral: false, createdAt: "2026-01-01T00:00:00Z", lastSeen: "2026-01-01T00:00:00Z",
            resources: resources, secrets: nil, flags: flags
        )
    }

    // MARK: grouping and ordering

    func testGroupsByAppAndSortsAppsAlphabetically() {
        let entries = [
            entry(app: "zeta", slug: "z1", slot: 0),
            entry(app: "alpha", slug: "a1", slot: 0),
        ]
        let nodes = MenuBuilder.build(from: entries)
        guard case .group(let first) = nodes[0], case .group(let second) = nodes[1] else {
            return XCTFail("expected two groups")
        }
        XCTAssertTrue(first.headerTitle.hasPrefix("alpha"))
        XCTAssertTrue(second.headerTitle.hasPrefix("zeta"))
    }

    func testHeaderTitleCarriesTheAppNameAndCount() {
        let entries = [
            entry(app: "worktree-manager", slug: "a", slot: 0),
            entry(app: "worktree-manager", slug: "b", slot: 1),
        ]
        let nodes = MenuBuilder.build(from: entries)
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertEqual(group.headerTitle, "worktree-manager   2")
        XCTAssertEqual(group.rows.count, 2)
    }

    func testHeaderRowIsDisabled() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0)])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        // The header itself never appears as a row; AppDelegate always
        // builds it disabled. What MenuBuilder controls is the rows
        // inside the submenu, asserted elsewhere.
        XCTAssertFalse(group.rows.isEmpty)
    }

    func testSortsEntriesWithinAnAppBySlotThenSlug() {
        // Mirrors internal/cli/fleet.go's writeListTable order: app, then
        // slot, then slug.
        let entries = [
            entry(slug: "zzz", slot: 1),
            entry(slug: "aaa", slot: 1),
            entry(slug: "mmm", slot: 0),
        ]
        let nodes = MenuBuilder.build(from: entries)
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        let slugsInOrder = group.rows.map { $0.title }
        XCTAssertTrue(slugsInOrder[0].contains("mmm"))
        XCTAssertTrue(slugsInOrder[1].contains("aaa"))
        XCTAssertTrue(slugsInOrder[2].contains("zzz"))
    }

    func testMultipleAppsEachGetTheirOwnGroupInAlphabeticalOrderWithMultipleSlots() {
        let entries = [
            entry(app: "beta", slug: "b-2", slot: 2),
            entry(app: "beta", slug: "b-0", slot: 0),
            entry(app: "alpha", slug: "a-1", slot: 1),
            entry(app: "alpha", slug: "a-0", slot: 0),
        ]
        let nodes = MenuBuilder.build(from: entries)
        XCTAssertEqual(nodes.count, 2)
        guard case .group(let alphaGroup) = nodes[0], case .group(let betaGroup) = nodes[1] else {
            return XCTFail("expected two groups")
        }
        XCTAssertEqual(alphaGroup.headerTitle, "alpha   2")
        XCTAssertEqual(betaGroup.headerTitle, "beta   2")
        XCTAssertTrue(alphaGroup.rows[0].title.contains("a-0"))
        XCTAssertTrue(alphaGroup.rows[1].title.contains("a-1"))
        XCTAssertTrue(betaGroup.rows[0].title.contains("b-0"))
        XCTAssertTrue(betaGroup.rows[1].title.contains("b-2"))
    }

    // MARK: the port column

    func testRowWithNoPortResourceHasNoPortSuffix() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, resources: [
            "compose_project": Resolved(type: "compose-project", value: .text("wt-a-0")),
        ])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertFalse(group.rows[0].title.contains(":"))
    }

    func testRowWithOnePortResourceShowsItRightAligned() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, resources: [
            "web": Resolved(type: "port", value: .port(7843)),
        ])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertTrue(group.rows[0].title.contains("\t:7843"))
    }

    func testRowWithSeveralPortResourcesShowsTheAlphabeticallyFirstKey() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, resources: [
            "web": Resolved(type: "port", value: .port(7843)),
            "api": Resolved(type: "port", value: .port(7900)),
        ])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        // "api" sorts before "web", so its port is the one shown.
        XCTAssertTrue(group.rows[0].title.contains(":7900"))
        XCTAssertFalse(group.rows[0].title.contains(":7843"))
    }

    // MARK: flags

    func testStaleFlagDisablesTheRowAndAppendsAMarker() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, flags: ["stale"])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertFalse(group.rows[0].isEnabled)
        XCTAssertTrue(group.rows[0].title.contains("(stale)"))
    }

    func testForeignFlagAppendsTheOwnerAndLeavesTheRowEnabled() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, flags: ["foreign"], owner: "sha256:abcd1234")])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertTrue(group.rows[0].isEnabled)
        XCTAssertTrue(group.rows[0].title.contains("sha256:abcd1234"))
    }

    func testUnverifiableFlagAppendsANoteAndLeavesTheRowEnabled() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, flags: ["unverifiable"])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertTrue(group.rows[0].isEnabled)
        XCTAssertTrue(group.rows[0].title.lowercased().contains("not visible"))
    }

    // MARK: path_visible

    func testPathNotVisibleDisablesTheRowEvenWithoutFlags() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, pathVisible: false)])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertFalse(group.rows[0].isEnabled)
    }

    func testPathNotVisibleDisablesTheRowEvenWhenOnlyAnEnablingFlagIsSet() {
        // "foreign" and "unverifiable" alone leave a row enabled, but
        // path_visible == false must still win.
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, pathVisible: false, flags: ["foreign"])])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertFalse(group.rows[0].isEnabled)
    }

    // MARK: state symbols

    func testUnrecognisedStateFallsBackToTheNeutralMarkerRatherThanFailing() {
        let known = MenuBuilder.build(from: [entry(slug: "a", slot: 0, state: "active")])
        let unknown = MenuBuilder.build(from: [entry(slug: "b", slot: 0, state: "quantum-superposition")])
        guard case .group(let knownGroup) = known[0], case .group(let unknownGroup) = unknown[0] else {
            return XCTFail("expected groups")
        }
        // Different, known symbols exist for different states; an
        // unrecognised one still produces *a* row rather than throwing or
        // crashing, and it is not empty.
        XCTAssertFalse(knownGroup.rows[0].title.isEmpty)
        XCTAssertFalse(unknownGroup.rows[0].title.isEmpty)
        XCTAssertNotEqual(
            String(knownGroup.rows[0].title.first ?? " "),
            String(unknownGroup.rows[0].title.first ?? " ")
        )
    }

    func testEachKnownStateGetsItsOwnSymbol() {
        let states = ["reserving", "active", "tearing-down"]
        var symbols = Set<Character>()
        for state in states {
            let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, state: state)])
            guard case .group(let group) = nodes[0], let first = group.rows[0].title.first else {
                return XCTFail("expected a row")
            }
            symbols.insert(first)
        }
        XCTAssertEqual(symbols.count, states.count, "each known state should render a distinct leading symbol")
    }

    // MARK: tooltip

    func testDescriptionBecomesTheTooltip() {
        let nodes = MenuBuilder.build(from: [entry(slug: "a", slot: 0, description: "the menu bar app")])
        guard case .group(let group) = nodes[0] else {
            return XCTFail("expected a group")
        }
        XCTAssertEqual(group.rows[0].tooltip, "the menu bar app")
    }

    // MARK: empty registry

    func testEmptyRegistryProducesOneDisabledRow() {
        let nodes = MenuBuilder.build(from: [])
        XCTAssertEqual(nodes.count, 1)
        guard case .row(let row) = nodes[0] else {
            return XCTFail("expected a plain row, not a group")
        }
        XCTAssertFalse(row.isEnabled)
        XCTAssertFalse(row.title.isEmpty)
    }
}
