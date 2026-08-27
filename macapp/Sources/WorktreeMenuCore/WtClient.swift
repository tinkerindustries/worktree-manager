import Foundation

/// The result of running a process: its exit code and the bytes it wrote
/// to stdout and stderr.
public struct ProcessResult: Equatable, Sendable {
    public let exitCode: Int32
    public let stdout: Data
    public let stderr: Data

    public init(exitCode: Int32, stdout: Data, stderr: Data) {
        self.exitCode = exitCode
        self.stdout = stdout
        self.stderr = stderr
    }
}

/// Runs one executable with the given arguments and reports what came
/// back. `WtClient` takes this as an injectable closure so the exit-code
/// mapping is unit-testable without a real `wt` binary on the machine
/// (PLAN.md).
public typealias ProcessRunner = @Sendable (_ executable: URL, _ arguments: [String]) async throws -> ProcessResult

/// Locates the `wt` binary. Returns nil when nothing is found.
public typealias BinaryLocator = @Sendable () -> URL?

/// Everything that can go wrong resolving or running `wt`. Exit codes are
/// `internal/cli/errors.go`'s: 0 ok, 5 unreachable, everything else a
/// generic failure carrying the stderr text (PLAN.md's data contract).
public enum WtClientError: Error, Sendable {
    /// No candidate path held an executable `wt`.
    case binaryNotFound
    /// `wt` exited 5: the coordinator could not be dialed. The string is
    /// whatever `wt` wrote to stderr.
    case unreachable(String)
    /// `wt` exited with any other non-zero code.
    case failed(code: Int32, stderr: String)
    /// `wt` exited 0 but stdout did not decode as a `ListResult`.
    case decode(Error)
}

extension WtClientError: Equatable {
    public static func == (lhs: WtClientError, rhs: WtClientError) -> Bool {
        switch (lhs, rhs) {
        case (.binaryNotFound, .binaryNotFound):
            return true
        case let (.unreachable(l), .unreachable(r)):
            return l == r
        case let (.failed(lCode, lStderr), .failed(rCode, rStderr)):
            return lCode == rCode && lStderr == rStderr
        case (.decode, .decode):
            // The underlying decoding errors are not themselves
            // Equatable; two decode failures compare equal regardless of
            // cause. Tests that care about the cause inspect it directly.
            return true
        default:
            return false
        }
    }
}

/// Resolves the `wt` binary, runs `wt list --json`, and maps the exit
/// code to a `ListResult` or a `WtClientError`. Never calls its runner or
/// waits on the result from the main thread — the caller is expected to
/// `await` this from a background task (PLAN.md's refresh model).
public struct WtClient: Sendable {
    /// The `UserDefaults` key an override binary path is stored under.
    public static let binaryPathDefaultsKey = "WTBinaryPath"

    private let runner: ProcessRunner
    private let locateBinary: BinaryLocator

    public init(
        runner: @escaping ProcessRunner = WtClient.runProcess,
        locateBinary: @escaping BinaryLocator = { WtClient.locateBinaryOnDisk() }
    ) {
        self.runner = runner
        self.locateBinary = locateBinary
    }

    /// Runs `wt list --json` and decodes its stdout. Never passes
    /// `--wide` (PLAN.md: seed credentials must not appear in a menu).
    public func list() async throws -> ListResult {
        guard let binary = locateBinary() else {
            throw WtClientError.binaryNotFound
        }
        let result = try await runner(binary, ["list", "--json"])
        if result.exitCode == 0 {
            do {
                let decoder = JSONDecoder()
                decoder.keyDecodingStrategy = .convertFromSnakeCase
                return try decoder.decode(ListResult.self, from: result.stdout)
            } catch {
                throw WtClientError.decode(error)
            }
        }
        let stderrText = String(data: result.stderr, encoding: .utf8) ?? ""
        if result.exitCode == 5 {
            throw WtClientError.unreachable(stderrText)
        }
        throw WtClientError.failed(code: result.exitCode, stderr: stderrText)
    }

    /// The default binary search order (PLAN.md): an explicit
    /// `UserDefaults` override first, then the path `dist/install.sh`
    /// uses, then the two common Homebrew/system locations. The first
    /// executable file found wins.
    public static func locateBinaryOnDisk(
        defaults: UserDefaults = .standard,
        fileManager: FileManager = .default
    ) -> URL? {
        var candidates: [URL] = []
        if let override = defaults.string(forKey: binaryPathDefaultsKey), !override.isEmpty {
            candidates.append(URL(fileURLWithPath: override))
        }
        let home = fileManager.homeDirectoryForCurrentUser
        candidates.append(home.appendingPathComponent(".local/bin/wt"))
        candidates.append(URL(fileURLWithPath: "/usr/local/bin/wt"))
        candidates.append(URL(fileURLWithPath: "/opt/homebrew/bin/wt"))

        for candidate in candidates where fileManager.isExecutableFile(atPath: candidate.path) {
            return candidate
        }
        return nil
    }

    /// The default process runner: runs the executable with `Process`,
    /// draining stdout and stderr concurrently so a chatty stderr cannot
    /// deadlock a large stdout (or vice versa). The blocking wait happens
    /// on a background queue, never on the caller's thread.
    public static func runProcess(_ executable: URL, arguments: [String]) async throws -> ProcessResult {
        try await withCheckedThrowingContinuation { continuation in
            DispatchQueue.global(qos: .utility).async {
                let process = Process()
                process.executableURL = executable
                process.arguments = arguments

                let stdoutPipe = Pipe()
                let stderrPipe = Pipe()
                process.standardOutput = stdoutPipe
                process.standardError = stderrPipe

                do {
                    try process.run()
                } catch {
                    continuation.resume(throwing: error)
                    return
                }

                let group = DispatchGroup()
                let stdoutBox = DataBox()
                let stderrBox = DataBox()

                group.enter()
                DispatchQueue.global(qos: .utility).async {
                    stdoutBox.data = stdoutPipe.fileHandleForReading.readDataToEndOfFile()
                    group.leave()
                }
                group.enter()
                DispatchQueue.global(qos: .utility).async {
                    stderrBox.data = stderrPipe.fileHandleForReading.readDataToEndOfFile()
                    group.leave()
                }

                process.waitUntilExit()
                group.wait()

                continuation.resume(returning: ProcessResult(
                    exitCode: process.terminationStatus,
                    stdout: stdoutBox.data,
                    stderr: stderrBox.data
                ))
            }
        }
    }
}

/// A single mutable `Data` slot passed into a background read closure.
/// The write happens before `group.leave()` and the read happens after
/// `group.wait()`, so the two never overlap even though the compiler
/// cannot see that ordering — hence `@unchecked`.
private final class DataBox: @unchecked Sendable {
    var data = Data()
}
