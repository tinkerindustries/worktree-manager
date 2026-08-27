import Foundation

/// Enforces that only the most recently *started* refresh's result is ever
/// applied, whichever finishes first. Opening the menu twice quickly, or
/// the periodic timer firing while a menu-open refresh is still in
/// flight, both start overlapping `wt list` runs; without this, whichever
/// happens to finish last would win regardless of which one started first
/// (macapp/PLAN.md).
///
/// Plain, lock-protected state rather than an actor: `begin()` must hand
/// out a strictly increasing ticket to calls made back-to-back on one
/// thread (the main thread, in `AppDelegate`'s case), and an actor's
/// suspension points cannot guarantee that ordering for its callers.
public final class RefreshCoordinator: @unchecked Sendable {
    private let lock = NSLock()
    private var generation = 0

    public init() {}

    /// Call synchronously before starting a refresh, before any `await`.
    /// The returned ticket records this refresh's place in the start
    /// order.
    public func begin() -> Int {
        lock.lock()
        defer { lock.unlock() }
        generation += 1
        return generation
    }

    /// Call after a refresh's work completes, before applying its
    /// result. `true` means no newer refresh has started since `ticket`
    /// was issued, so this result is the one to keep; `false` means a
    /// newer refresh has since started and this one's result must be
    /// discarded.
    public func isCurrent(_ ticket: Int) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return ticket == generation
    }
}
