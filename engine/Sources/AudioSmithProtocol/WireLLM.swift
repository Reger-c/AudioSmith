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
