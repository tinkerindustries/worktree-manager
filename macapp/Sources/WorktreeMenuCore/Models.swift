import Foundation

/// One resolved resource value. The wire format ties the shape of `value`
/// to `type`: an `int` when `type == "port"`, a `string` otherwise
/// (internal/spec/resolve.go's `Resolved`, PLAN.md's data contract). The
/// enum keeps that tie explicit instead of decoding into `Any`.
public enum ResolvedValue: Equatable, Sendable {
    case port(Int)
    case text(String)
}

/// One row of an entry's resolved resource table, decoded from the
/// `resources` map in `wt list --json`.
public struct Resolved: Equatable, Sendable {
    public let type: String
    public let value: ResolvedValue

    public init(type: String, value: ResolvedValue) {
        self.type = type
        self.value = value
    }
}

extension Resolved: Decodable {
    private enum CodingKeys: String, CodingKey {
        case type
        case value
    }

    public init(from decoder: Decoder) throws {
        let container = try decoder.container(keyedBy: CodingKeys.self)
        let type = try container.decode(String.self, forKey: .type)
        let value: ResolvedValue
        if type == "port" {
            value = .port(try container.decode(Int.self, forKey: .value))
        } else {
            value = .text(try container.decode(String.self, forKey: .value))
        }
        self.init(type: type, value: value)
    }
}

/// One registry entry, decoded from `wt list --json`. Field names match
/// `internal/api.ListEntry`; the decoder converts the wire's snake_case
/// keys to these camelCase properties.
public struct ListEntry: Decodable, Equatable, Sendable {
    public let app: String
    public let slug: String
    public let slot: Int
    public let description: String?
    public let state: String
    public let path: String
    public let pathVisible: Bool
    public let owner: String
    public let ownerKind: String
    public let ephemeral: Bool
    public let createdAt: String
    public let lastSeen: String
    public let resources: [String: Resolved]
    public let secrets: [String: String]?
    public let flags: [String]?

    public init(
        app: String,
        slug: String,
        slot: Int,
        description: String? = nil,
        state: String,
        path: String,
        pathVisible: Bool,
        owner: String,
        ownerKind: String,
        ephemeral: Bool,
        createdAt: String,
        lastSeen: String,
        resources: [String: Resolved] = [:],
        secrets: [String: String]? = nil,
        flags: [String]? = nil
    ) {
        self.app = app
        self.slug = slug
        self.slot = slot
        self.description = description
        self.state = state
        self.path = path
        self.pathVisible = pathVisible
        self.owner = owner
        self.ownerKind = ownerKind
        self.ephemeral = ephemeral
        self.createdAt = createdAt
        self.lastSeen = lastSeen
        self.resources = resources
        self.secrets = secrets
        self.flags = flags
    }
}

/// The whole registry, across every repo. This is the entire shape of
/// `wt list --json`'s stdout (PLAN.md's data contract).
public struct ListResult: Decodable, Equatable, Sendable {
    public let entries: [ListEntry]

    public init(entries: [ListEntry]) {
        self.entries = entries
    }
}

/// A doctor finding's severity, decoded from `internal/api.DoctorFinding`'s
/// `level` field. Ordered `info < warning < error` so the badge decision
/// (phase 5) is "does anything outrank info" rather than a manual
/// enumeration of the two levels that do.
public enum DoctorLevel: String, Decodable, Equatable, Comparable, Sendable {
    case info
    case warning
    case error

    private var rank: Int {
        switch self {
        case .info: return 0
        case .warning: return 1
        case .error: return 2
        }
    }

    public static func < (lhs: DoctorLevel, rhs: DoctorLevel) -> Bool {
        lhs.rank < rhs.rank
    }
}

/// One row of `wt doctor --json`'s report. `app` and `slug` are absent for
/// a finding that is not about one particular entry; `remedy` is absent
/// for an info-level observation, which has nothing to fix
/// (`internal/api.DoctorFinding`).
public struct DoctorFinding: Decodable, Equatable, Sendable {
    public let app: String?
    public let slug: String?
    public let level: DoctorLevel
    public let message: String
    public let remedy: String?

    public init(app: String? = nil, slug: String? = nil, level: DoctorLevel, message: String, remedy: String? = nil) {
        self.app = app
        self.slug = slug
        self.level = level
        self.message = message
        self.remedy = remedy
    }
}

/// The whole shape of `wt doctor --json`'s stdout: every finding, plus the
/// bounded-coverage notes about what doctor could not check
/// (`internal/api.DoctorResult`).
public struct DoctorResult: Decodable, Equatable, Sendable {
    public let findings: [DoctorFinding]
    public let notes: [String]?

    public init(findings: [DoctorFinding], notes: [String]? = nil) {
        self.findings = findings
        self.notes = notes
    }
}
