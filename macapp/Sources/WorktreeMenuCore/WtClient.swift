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
    /// The process did not finish within the runner's timeout and was
    /// terminated. Distinct from `.failed` so the menu can say the run
    /// timed out rather than blaming the coordinator for a plain
    /// non-zero exit.
    case timedOut
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
        case (.timedOut, .timedOut):
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
        try await runAndDecode(["list", "--json"], as: ListResult.self)
    }

    /// Runs `wt doctor --json` and decodes its stdout. `wt doctor` uses
    /// the same exit codes as `wt list` (`internal/cli/errors.go`), so
    /// this shares `runAndDecode` rather than re-deriving the mapping.
    /// Doctor reads every entry, not just this app's, so callers are
    /// expected to run it on a slower cadence than `list()`.
    public func doctor() async throws -> DoctorResult {
        try await runAndDecode(["doctor", "--json"], as: DoctorResult.self)
    }

    /// Runs `wt --version` and returns its stdout line verbatim —
    /// `"wt <version> (<commit>)"`, plain text rather than JSON
    /// (`internal/cli/version_test.go`). `VersionCore.parseVersionLine`
    /// is where that line is actually parsed; this only runs the process
    /// and applies the same exit-code mapping every other verb uses.
    /// `wt --version` needs no store, endpoint or coordinator, but a
    /// stopped or renamed binary still fails the same way `list()` and
    /// `doctor()` would.
    public func version() async throws -> String {
        try await runAndDecode(["--version"]) { data in
            String(data: data, encoding: .utf8) ?? ""
        }
    }

    /// Resolves the binary, runs it, and applies the one exit-code
    /// mapping every verb shares: 0 decodes via `decode`, 5 is
    /// `.unreachable`, anything else is `.failed`. `list()`, `doctor()`
    /// and `version()` differ only in the arguments and how stdout is
    /// turned into a result, so that is the only thing each passes in.
    private func runAndDecode<T>(_ arguments: [String], decode: (Data) throws -> T) async throws -> T {
        guard let binary = locateBinary() else {
            throw WtClientError.binaryNotFound
        }
        let result = try await runner(binary, arguments)
        if result.exitCode == 0 {
            do {
                return try decode(result.stdout)
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

    /// The `Decodable` convenience over `runAndDecode`, used by `list()`
    /// and `doctor()`, which both decode JSON with the same snake-case
    /// strategy.
    private func runAndDecode<T: Decodable>(_ arguments: [String], as type: T.Type) async throws -> T {
        try await runAndDecode(arguments) { data in
            let decoder = JSONDecoder()
            decoder.keyDecodingStrategy = .convertFromSnakeCase
            return try decoder.decode(type, from: data)
        }
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

    /// How long the default runner waits before giving up on `wt`.
    /// `wt list` is a local HTTP round trip; ten seconds is generous for
    /// that and still short enough that a hung coordinator does not leave
    /// the menu saying "Loading…" indefinitely.
    public static let defaultTimeout: TimeInterval = 10

    /// The default process runner, run with `defaultTimeout`. This is the
    /// two-argument shape `ProcessRunner` requires, which is what lets it
    /// stand as `WtClient.init`'s default argument.
    public static func runProcess(_ executable: URL, arguments: [String]) async throws -> ProcessResult {
        try await runProcess(executable, arguments: arguments, timeout: defaultTimeout)
    }

    /// Builds a runner with a caller-chosen timeout, so a test can exercise
    /// real process termination without waiting out `defaultTimeout`.
    public static func runProcess(timeout: TimeInterval) -> ProcessRunner {
        { executable, arguments in
            try await runProcess(executable, arguments: arguments, timeout: timeout)
        }
    }

    /// Runs the executable with `Process`, draining stdout and stderr
    /// concurrently so a chatty stderr cannot deadlock a large stdout (or
    /// vice versa). The blocking wait happens on a background queue,
    /// never on the caller's thread. A timer races the process: if it
    /// fires first, the process is terminated and this throws
    /// `WtClientError.timedOut` instead of returning a result.
    private static func runProcess(
        _ executable: URL,
        arguments: [String],
        timeout: TimeInterval
    ) async throws -> ProcessResult {
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

                let timedOut = TimeoutFlag()
                let timeoutTimer = DispatchSource.makeTimerSource(queue: .global(qos: .utility))
                timeoutTimer.schedule(deadline: .now() + timeout)
                timeoutTimer.setEventHandler {
                    timedOut.value = true
                    if process.isRunning {
                        process.terminate()
                    }
                }
                timeoutTimer.resume()

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
                timeoutTimer.cancel()
                group.wait()

                if timedOut.value {
                    continuation.resume(throwing: WtClientError.timedOut)
                    return
                }

                continuation.resume(returning: ProcessResult(
                    exitCode: process.terminationStatus,
                    stdout: stdoutBox.data,
                    stderr: stderrBox.data
                ))
            }
        }
    }
}

/// A single mutable `Bool` slot set by the timeout timer's handler and
/// read after `process.waitUntilExit()` returns. The two never overlap in
/// practice (the timer either fires and terminates the process, which
/// then makes `waitUntilExit` return, or the process exits first and the
/// timer is cancelled before it could fire) but the compiler cannot see
/// that ordering — hence `@unchecked`.
private final class TimeoutFlag: @unchecked Sendable {
    var value = false
}

/// A single mutable `Data` slot passed into a background read closure.
/// The write happens before `group.leave()` and the read happens after
/// `group.wait()`, so the two never overlap even though the compiler
/// cannot see that ordering — hence `@unchecked`.
private final class DataBox: @unchecked Sendable {
    var data = Data()
}
