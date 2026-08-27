import XCTest
@testable import WorktreeMenuCore

/// Proves the overlapping-refresh ordering rule directly: whichever
/// refresh started last is the one whose result is kept, regardless of
/// which one finishes first (macapp/PLAN.md).
final class RefreshCoordinatorTests: XCTestCase {
    func testTicketsIncreaseWithEachCallToBegin() {
        let coordinator = RefreshCoordinator()
        XCTAssertEqual(coordinator.begin(), 1)
        XCTAssertEqual(coordinator.begin(), 2)
        XCTAssertEqual(coordinator.begin(), 3)
    }

    func testOnlyTheMostRecentlyStartedTicketIsCurrent() {
        let coordinator = RefreshCoordinator()
        let first = coordinator.begin()
        let second = coordinator.begin()
        XCTAssertFalse(coordinator.isCurrent(first))
        XCTAssertTrue(coordinator.isCurrent(second))
    }

    /// Simulates the actual failure mode: two refreshes overlap, the one
    /// that started first happens to finish last, and its result must
    /// still be discarded because a newer refresh started after it.
    func testOlderRefreshLosesEvenWhenItFinishesAfterTheNewerOne() async {
        let coordinator = RefreshCoordinator()
        let applied = AppliedResult()

        let firstTicket = coordinator.begin()
        let secondTicket = coordinator.begin()

        // The first (older) refresh finishes later than the second
        // (newer) one — the reverse of start order.
        async let firstCompletion: Void = finishSlowly(
            ticket: firstTicket, marker: 1, coordinator: coordinator, applied: applied
        )
        async let secondCompletion: Void = finishImmediately(
            ticket: secondTicket, marker: 2, coordinator: coordinator, applied: applied
        )
        _ = await (firstCompletion, secondCompletion)

        // Regardless of finishing order, only ticket 2 (the one that
        // started last) was ever allowed to apply.
        let result = await applied.value
        XCTAssertEqual(result, 2)
    }

    func testAThirdRefreshStartingWhileTwoAreInFlightSupersedesBothOlderOnes() {
        let coordinator = RefreshCoordinator()
        let first = coordinator.begin()
        let second = coordinator.begin()
        let third = coordinator.begin()
        XCTAssertFalse(coordinator.isCurrent(first))
        XCTAssertFalse(coordinator.isCurrent(second))
        XCTAssertTrue(coordinator.isCurrent(third))
    }
}

/// Free functions, not test-case methods: capturing `self` (an
/// `XCTestCase`) across an `async let` is what the strict-concurrency
/// checker objects to, and these need no test-case state anyway.
private func finishSlowly(
    ticket: Int, marker: Int, coordinator: RefreshCoordinator, applied: AppliedResult
) async {
    try? await Task.sleep(nanoseconds: 100_000_000)
    if coordinator.isCurrent(ticket) {
        await applied.set(marker)
    }
}

private func finishImmediately(
    ticket: Int, marker: Int, coordinator: RefreshCoordinator, applied: AppliedResult
) async {
    if coordinator.isCurrent(ticket) {
        await applied.set(marker)
    }
}

/// A single slot two concurrent tasks race to write, standing in for
/// `AppDelegate`'s `lastSnapshot`/`lastError` in the simulated-overlap
/// test above.
private actor AppliedResult {
    var value: Int?

    func set(_ v: Int) {
        value = v
    }
}
