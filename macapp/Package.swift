// swift-tools-version: 6.0
// The menu bar app for worktree-manager. It shells out to `wt` and renders
// what comes back; it never reads the store, never speaks HTTP, and never
// crawls the filesystem (PLAN.md).
//
// WorktreeMenuCore holds the code the test target needs: the JSON models
// and WtClient. Executable targets are awkward to import from a test
// target on this toolchain, so the testable half lives in a library and
// the executable is a thin shell around it (PLAN.md's note on SwiftPM
// specifics).
import PackageDescription

let package = Package(
    name: "WorktreeMenu",
    platforms: [
        .macOS(.v14)
    ],
    targets: [
        .target(
            name: "WorktreeMenuCore"
        ),
        .executableTarget(
            name: "WorktreeMenu",
            dependencies: ["WorktreeMenuCore"]
        ),
        .testTarget(
            name: "WorktreeMenuCoreTests",
            dependencies: ["WorktreeMenuCore"]
        ),
    ]
)
