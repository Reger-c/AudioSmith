import Foundation

// The local language model (docs/protocol-v2.md §5.4): the Swift mirror of
// internal/engine/proto/llm.go. Optional here = omitempty there.

public struct LLMMessage: Codable, Sendable {
    public var role: String
    public var content: String

    public init(role: String, content: String) {
        self.role = role
        self.content = content
    }
}

public struct LLMGenerateParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var messages: [LLMMessage]
    public var maxTokens: Int?
    public var temperature: Double?
    public var topP: Double?
    public var seed: Int64?
    public var stop: [String]?
    public var stream: Bool?
    public var json: Bool?
    public var schema: JSONValue?
    /// Samples a constrained reply with temperature/topP/seed as a free
    /// one (default: greedy, the same reply every time).
    public var sampleConstrained: Bool?
}

extension LLMGenerateParams {
    /// The JSON Schema text the grammar is compiled from (any object or
    /// array without a schema). xgrammar writes an object's properties in
    /// the order the schema lists them, so that order is fixed here: an
    /// object's required properties in the order "required" names them
    /// (the order the app's prompts describe), then the others by name.
    /// Encoded as decoded, a Swift dictionary's order changed with every
    /// engine process (seeded hashing): the same request wrote its keys in
    /// another order on each launch ("lines" before "scene", "intensity"
    /// before "n"), and greedy replies differed from run to run.
    public var grammarSchema: String {
        guard let schema else { return #"{"type":["object","array"]}"# }
        return Self.schemaText(schema)
    }

    static func schemaText(_ v: JSONValue, order: [String]? = nil) -> String {
        switch v {
        case .object(let o):
            var keys = o.keys.sorted()
            if let order {
                let first = order.filter { o[$0] != nil }
                keys = first + keys.filter { !first.contains($0) }
            }
            var required: [String]?
            if case .array(let r)? = o["required"] {
                required = r.compactMap { if case .string(let s) = $0 { return s } else { return nil } }
            }
            let parts = keys.map { k in
                scalarText(.string(k)) + ":" + schemaText(o[k]!, order: k == "properties" ? required : nil)
            }
            return "{" + parts.joined(separator: ",") + "}"
        case .array(let a):
            return "[" + a.map { schemaText($0) }.joined(separator: ",") + "]"
        default:
            return scalarText(v)
        }
    }

    private static func scalarText(_ v: JSONValue) -> String {
        let encoder = JSONEncoder()
        encoder.outputFormatting = [.withoutEscapingSlashes]
        guard let data = try? encoder.encode(v), let s = String(data: data, encoding: .utf8) else { return "null" }
        return s
    }
}

public struct LLMGenerateResult: Codable, Sendable {
    public var text: String
    public var finishReason: String
    public var model: String?
    public var promptTokens: Int
    public var completionTokens: Int
    public var seconds: Double?
    public var tokensPerSecond: Double?
    public var constrained: Bool?

    public init(text: String, finishReason: String, model: String?, promptTokens: Int, completionTokens: Int,
                seconds: Double?, tokensPerSecond: Double?, constrained: Bool?) {
        self.text = text
        self.finishReason = finishReason
        self.model = model
        self.promptTokens = promptTokens
        self.completionTokens = completionTokens
        self.seconds = seconds
        self.tokensPerSecond = tokensPerSecond
        self.constrained = constrained
    }
}

public struct LLMTokenParams: Codable, Sendable {
    public var job: String
    public var text: String

    public init(job: String, text: String) {
        self.job = job
        self.text = text
    }
}
