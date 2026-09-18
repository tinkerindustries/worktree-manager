import XCTest
@testable import WorktreeMenuCore

/// Decodes fixtures shaped like `wt list --json`'s actual output
/// (PLAN.md's data contract): a port resource carries an int value, every
/// other resource type carries a string value, and flags are optional.
final class ModelsTests: XCTestCase {
    private func decode(_ json: String) throws -> ListResult {
        let decoder = JSONDecoder()
        decoder.keyDecodingStrategy = .convertFromSnakeCase
        return try decoder.decode(ListResult.self, from: Data(json.utf8))
    }

    func testDecodesPortAndTextResources() throws {
        let json = """
        {"entries": [{
          "app": "worktree-manager", "slug": "macapp", "slot": 3,
          "description": "the menu bar app", "state": "active",
          "path": "/Users/alex/Repos/worktree-manager/.claude/worktrees/macapp",
          "path_visible": true,
          "owner": "alex", "owner_kind": "host", "ephemeral": false,
          "created_at": "2026-08-01T00:00:00Z", "last_seen": "2026-08-27T00:00:00Z",
          "resources": {
            "web": {"type": "port", "value": 7843},
            "compose_project": {"type": "compose-project", "value": "wt-macapp-3"}
          }
        }]}
        """
        let result = try decode(json)
        XCTAssertEqual(result.entries.count, 1)
        let entry = result.entries[0]
        XCTAssertEqual(entry.app, "worktree-manager")
        XCTAssertEqual(entry.slug, "macapp")
        XCTAssertEqual(entry.slot, 3)
        XCTAssertEqual(entry.pathVisible, true)
        XCTAssertEqual(entry.ownerKind, "host")
        XCTAssertEqual(entry.createdAt, "2026-08-01T00:00:00Z")
        XCTAssertEqual(entry.lastSeen, "2026-08-27T00:00:00Z")
        XCTAssertNil(entry.flags)

        guard let web = entry.resources["web"] else {
            return XCTFail("missing web resource")
        }
        XCTAssertEqual(web.type, "port")
        XCTAssertEqual(web.value, .port(7843))

        guard let composeProject = entry.resources["compose_project"] else {
            return XCTFail("missing compose_project resource")
        }
        XCTAssertEqual(composeProject.type, "compose-project")
        XCTAssertEqual(composeProject.value, .text("wt-macapp-3"))
    }

    func testDecodesEntryWithFlags() throws {
        let json = """
        {"entries": [{
          "app": "worktree-manager", "slug": "old-experiment", "slot": 1,
          "state": "active",
          "path": "/Users/alex/Repos/worktree-manager/.claude/worktrees/old-experiment",
          "path_visible": true,
          "owner": "alex", "owner_kind": "host", "ephemeral": false,
          "created_at": "2026-01-01T00:00:00Z", "last_seen": "2026-01-02T00:00:00Z",
          "resources": {},
          "flags": ["stale"]
        }]}
        """
        let result = try decode(json)
        XCTAssertEqual(result.entries.count, 1)
        XCTAssertEqual(result.entries[0].flags, ["stale"])
        // description is omitempty on the wire; absent means nil, not "".
        XCTAssertNil(result.entries[0].description)
    }

    func testEmptyRegistryDecodes() throws {
        let result = try decode(#"{"entries": []}"#)
        XCTAssertEqual(result.entries, [])
    }
}
