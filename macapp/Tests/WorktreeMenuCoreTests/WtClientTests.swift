import XCTest
@testable import WorktreeMenuCore

/// Exercises `WtClient`'s exit-code mapping through an injected stub
/// runner, never a real `wt` binary (PLAN.md).
final class WtClientTests: XCTestCase {
    private let fakeBinary = URL(fileURLWithPath: "/fake/bin/wt")

    private func makeClient(runner: @escaping ProcessRunner) -> WtClient {
        WtClient(runner: runner, locateBinary: { [fakeBinary] in fakeBinary })
    }

    func testExitZeroDecodesResult() async throws {
        let json = """
        {"entries": [{
          "app": "worktree-manager", "slug": "macapp", "slot": 3,
          "state": "active", "path": "/tmp/macapp", "path_visible": true,
          "owner": "geoff", "owner_kind": "host", "ephemeral": false,
          "created_at": "2026-08-01T00:00:00Z", "last_seen": "2026-08-27T00:00:00Z",
          "resources": {}
        }]}
        """
        let expectedBinary = fakeBinary
        let client = makeClient { executable, arguments in
            XCTAssertEqual(executable, expectedBinary)
            XCTAssertEqual(arguments, ["list", "--json"])
            return ProcessResult(exitCode: 0, stdout: Data(json.utf8), stderr: Data())
        }

        let result = try await client.list()
        XCTAssertEqual(result.entries.count, 1)
        XCTAssertEqual(result.entries[0].slug, "macapp")
    }

    func testNeverPassesWide() async throws {
        let client = makeClient { _, arguments in
            XCTAssertFalse(arguments.contains("--wide"))
            return ProcessResult(exitCode: 0, stdout: Data(#"{"entries": []}"#.utf8), stderr: Data())
        }
        _ = try await client.list()
    }

    func testExitFiveMapsToUnreachable() async throws {
        let client = makeClient { _, _ in
            ProcessResult(exitCode: 5, stdout: Data(), stderr: Data("dial tcp 127.0.0.1:7833: connection refused".utf8))
        }

        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch let WtClientError.unreachable(message) {
            XCTAssertEqual(message, "dial tcp 127.0.0.1:7833: connection refused")
        }
    }

    func testGenericFailureCarriesStderr() async throws {
        let client = makeClient { _, _ in
            ProcessResult(exitCode: 1, stdout: Data(), stderr: Data("something went wrong".utf8))
        }

        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch let WtClientError.failed(code, stderr) {
            XCTAssertEqual(code, 1)
            XCTAssertEqual(stderr, "something went wrong")
        }
    }

    func testUsageAndRefusalExitCodesAreAlsoGenericFailures() async throws {
        // Only 0 and 5 get bespoke handling; everything else renders as a
        // generic failure (PLAN.md's data contract).
        for code: Int32 in [2, 3, 4] {
            let client = makeClient { _, _ in
                ProcessResult(exitCode: code, stdout: Data(), stderr: Data("nope".utf8))
            }
            do {
                _ = try await client.list()
                XCTFail("expected an error for exit code \(code)")
            } catch let WtClientError.failed(gotCode, _) {
                XCTAssertEqual(gotCode, code)
            }
        }
    }

    func testMalformedOutputMapsToDecodeError() async throws {
        let client = makeClient { _, _ in
            ProcessResult(exitCode: 0, stdout: Data("not json".utf8), stderr: Data())
        }

        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch is WtClientError {
            // .decode wraps an underlying Error that is not itself
            // Equatable; catching WtClientError is enough to confirm the
            // decode path, not the success path, produced the failure.
        }
    }

    func testMissingBinaryReportsBinaryNotFound() async throws {
        let client = WtClient(
            runner: { _, _ in XCTFail("runner should not be invoked"); return ProcessResult(exitCode: 0, stdout: Data(), stderr: Data()) },
            locateBinary: { nil }
        )

        do {
            _ = try await client.list()
            XCTFail("expected an error")
        } catch WtClientError.binaryNotFound {
            // expected
        }
    }

    func testLocateBinaryOnDiskPrefersUserDefaultsOverride() {
        let defaults = UserDefaults(suiteName: "WorktreeMenuCoreTests.\(UUID().uuidString)")!
        defer { defaults.removePersistentDomain(forName: "WorktreeMenuCoreTests") }

        let tempDir = FileManager.default.temporaryDirectory.appendingPathComponent(UUID().uuidString)
        try? FileManager.default.createDirectory(at: tempDir, withIntermediateDirectories: true)
        let fakeWt = tempDir.appendingPathComponent("wt")
        FileManager.default.createFile(atPath: fakeWt.path, contents: Data())
        try? FileManager.default.setAttributes([.posixPermissions: 0o755], ofItemAtPath: fakeWt.path)

        defaults.set(fakeWt.path, forKey: WtClient.binaryPathDefaultsKey)

        let located = WtClient.locateBinaryOnDisk(defaults: defaults, fileManager: .default)
        XCTAssertEqual(located?.path, fakeWt.path)

        try? FileManager.default.removeItem(at: tempDir)
    }

    func testLocateBinaryOnDiskReturnsNilWhenNothingExists() {
        let defaults = UserDefaults(suiteName: "WorktreeMenuCoreTests.\(UUID().uuidString)")!
        // No override set, and the well-known paths are stubbed out via a
        // FileManager that reports nothing as executable.
        let located = WtClient.locateBinaryOnDisk(defaults: defaults, fileManager: NeverExecutableFileManager())
        XCTAssertNil(located)
    }
}

/// A `FileManager` stand-in that reports every path as non-executable, so
/// `locateBinaryOnDisk`'s "nothing found" branch is reachable in a test
/// without depending on the real machine's filesystem layout.
private final class NeverExecutableFileManager: FileManager, @unchecked Sendable {
    override func isExecutableFile(atPath path: String) -> Bool { false }
}
