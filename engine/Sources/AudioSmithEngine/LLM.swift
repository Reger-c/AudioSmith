import AudioSmithProtocol
import Foundation
@preconcurrency import MLX
import MLXGuidedGeneration
import MLXLLM
import MLXLMCommon
import Tokenizers

// llm.generate (docs/protocol-v2.md §5.4): chat completions on a local MLX
// language model (mlx-swift-lm's MLXLLM: Qwen3, Gemma 4…), loaded from the
// snapshot the app downloaded, its text streamed as llm.token. JSON
// requests are constrained to their schema with xgrammar
// (MLXGuidedGeneration, greedy); when that fails the reply is generated
// freely and the app validates it (internal/llm repairs).

/// A loaded language model, resident in the ModelStore.
final class LocalLLM: @unchecked Sendable {
    let context: ModelContext
    /// The context the engine allows: prompt + reply, in tokens.
    let contextLimit: Int
    private var grammar: GrammarTokenizer?
    private var biases: (closing: MLXArray, whitespace: MLXArray, whitespaceIDs: Set<Int>)?

    init(context: ModelContext, contextLimit: Int) {
        self.context = context
        self.contextLimit = contextLimit
    }

    /// The tokenizer xgrammar masks with, built on the first JSON request
    /// (it scans the vocabulary once).
    func grammarTokenizer() throws -> GrammarTokenizer {
        if let grammar { return grammar }
        let vocab = TokenizerVocabExtractor.extractForGrammar(from: context.tokenizer)
        let g = try GrammarTokenizer(vocab: vocab.vocab, vocabType: vocab.vocabType,
                                     eosTokenId: Int32(context.tokenizer.eosTokenId ?? 0))
        grammar = g
        return g
    }

    /// Logit biases for constrained JSON: towards closing the value near
    /// maxTokens, and against whitespace-only tokens (JSON allows any
    /// amount between tokens, and greedy decoding can loop on it).
    func grammarBiases() -> (closing: MLXArray, whitespace: MLXArray, whitespaceIDs: Set<Int>) {
        if let biases { return biases }
        let closing = ClosingTokenBias.compute(tokenizer: context.tokenizer, eosTokenId: context.tokenizer.eosTokenId)
        let (whitespace, ids) = WhitespaceTokenBias.compute(tokenizer: context.tokenizer)
        let b = (closing: closing, whitespace: whitespace, whitespaceIDs: ids)
        biases = b
        return b
    }

    /// Token ids that end the reply.
    var stopTokenIDs: Set<Int> {
        var ids = context.configuration.eosTokenIds
        if let eos = context.tokenizer.eosTokenId { ids.insert(eos) }
        for t in context.configuration.extraEOSTokens {
            if let id = context.tokenizer.convertTokenToId(t) { ids.insert(id) }
        }
        return ids
    }
}

/// Loads swift-transformers tokenizers for mlx-swift-lm (what its
/// MLXHuggingFace macros generate, without the macro package).
struct TransformersTokenizerLoader: MLXLMCommon.TokenizerLoader {
    func load(from directory: URL) async throws -> any MLXLMCommon.Tokenizer {
        TokenizerBridge(try await AutoTokenizer.from(modelFolder: directory))
    }
}

struct TokenizerBridge: MLXLMCommon.Tokenizer {
    let upstream: any Tokenizers.Tokenizer

    init(_ upstream: any Tokenizers.Tokenizer) { self.upstream = upstream }

    func encode(text: String, addSpecialTokens: Bool) -> [Int] {
        upstream.encode(text: text, addSpecialTokens: addSpecialTokens)
    }

    func decode(tokenIds: [Int], skipSpecialTokens: Bool) -> String {
        upstream.decode(tokens: tokenIds, skipSpecialTokens: skipSpecialTokens)
    }

    func convertTokenToId(_ token: String) -> Int? { upstream.convertTokenToId(token) }
    func convertIdToToken(_ id: Int) -> String? { upstream.convertIdToToken(id) }
    var bosToken: String? { upstream.bosToken }
    var eosToken: String? { upstream.eosToken }
    var unknownToken: String? { upstream.unknownToken }

    func applyChatTemplate(messages: [[String: any Sendable]], tools: [[String: any Sendable]]?,
                           additionalContext: [String: any Sendable]?) throws -> [Int] {
        do {
            return try upstream.applyChatTemplate(messages: messages, tools: tools, additionalContext: additionalContext)
        } catch Tokenizers.TokenizerError.missingChatTemplate {
            throw MLXLMCommon.TokenizerError.missingChatTemplate
        }
    }
}

extension Analyzer {
    /// Language-model families this build loads (catalog.Entry.Family).
    static let llmFamilies = ["qwen3", "gemma4"]

    /// The most context an llm.generate may use (AUDIOSMITH_LLM_CONTEXT):
    /// the KV cache of a 4B model takes about 150 KB a token.
    static var llmContextCap: Int {
        Int(ProcessInfo.processInfo.environment["AUDIOSMITH_LLM_CONTEXT"] ?? "") ?? 16384
    }

    func llmModel(_ model: String, path: String?, job: String, offline: Bool) async throws -> LocalLLM {
        guard let dir = try modelDirectory(model, path: path, offline: offline) else {
            throw EngineError.notDownloaded(model: model, missing: ["config.json"])
        }
        let (m, _) = try await store.acquire(model, task: "llm", job: job, as: LocalLLM.self, sampleRate: { _ in 0 },
                                             estimate: weightBytes(dir)) {
            let context = try await LLMModelFactory.shared.load(from: dir, using: TransformersTokenizerLoader())
            return LocalLLM(context: context, contextLimit: min(Self.positionLimit(dir) ?? Int.max, Self.llmContextCap))
        }
        return m
    }

    /// max_position_embeddings of config.json (or of its text_config).
    static func positionLimit(_ dir: URL) -> Int? {
        guard let data = try? Data(contentsOf: dir.appendingPathComponent("config.json")),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return nil }
        if let n = obj["max_position_embeddings"] as? Int { return n }
        if let text = obj["text_config"] as? [String: Any], let n = text["max_position_embeddings"] as? Int { return n }
        return nil
    }

    /// The prompt's tokens through the model's chat template (thinking
    /// off for hybrid models such as Qwen3). A template that refuses a
    /// system message gets it folded into the first user message.
    func chatTokens(_ llm: LocalLLM, _ messages: [LLMMessage]) throws -> [Int] {
        let extra: [String: any Sendable] = ["enable_thinking": false]
        let msgs: [[String: any Sendable]] = messages.map { ["role": $0.role, "content": $0.content] }
        do {
            return try llm.context.tokenizer.applyChatTemplate(messages: msgs, tools: nil, additionalContext: extra)
        } catch MLXLMCommon.TokenizerError.missingChatTemplate {
            let text = messages.map(\.content).joined(separator: "\n\n")
            return llm.context.tokenizer.encode(text: text)
        } catch {
            let system = messages.filter { $0.role == "system" }.map(\.content).joined(separator: "\n\n")
            guard !system.isEmpty else { throw error }
            var rest = messages.filter { $0.role != "system" }
            if let i = rest.firstIndex(where: { $0.role == "user" }) {
                rest[i].content = system + "\n\n" + rest[i].content
            } else {
                rest.insert(LLMMessage(role: "user", content: system), at: 0)
            }
            return try llm.context.tokenizer.applyChatTemplate(
                messages: rest.map { ["role": $0.role, "content": $0.content] }, tools: nil, additionalContext: extra)
        }
    }

    func generate(_ p: LLMGenerateParams, job: String, offline: Bool) async throws -> LLMGenerateResult {
        guard !p.model.isEmpty else { throw EngineError.badRequest("Missing model") }
        guard !p.messages.isEmpty else { throw EngineError.badRequest("No messages") }
        let llm = try await llmModel(p.model, path: p.modelPath, job: job, offline: offline)
        defer { store.release(p.model) }
        let started = Date()
        let prompt = try chatTokens(llm, p.messages)
        let limit = llm.contextLimit
        var maxTokens = p.maxTokens ?? 0
        if maxTokens <= 0 { maxTokens = min(2048, max(limit - prompt.count, 0)) }
        let needed = prompt.count + max(maxTokens, 256)
        if needed > limit {
            throw RPCError(Code.contextTooLong,
                           "The prompt (\(prompt.count) tokens) and the reply (\(max(maxTokens, 256))) are over the \(limit) tokens this model may use",
                           data: .object(["limit": .int(Int64(limit)), "actual": .int(Int64(needed)), "unit": .string("tokens")]))
        }
        let out = self.out
        let progress = JobProgress(out: out, job: job)
        progress.send("generating", force: true)

        // Text out: streamed in pieces about 20 times a second, holding
        // back what could still turn out to be a stop string.
        let stops = (p.stop ?? []).filter { !$0.isEmpty }
        let holdback = max((stops.map(\.count).max() ?? 0) - 1, 0)
        let stream = p.stream ?? false
        var text = ""
        var streamed = 0
        var lastFlush = Date.distantPast
        var stopped = false
        func flush(final: Bool) {
            guard stream else { return }
            let upTo = final ? text.count : text.count - holdback
            guard upTo > streamed else { return }
            let now = Date()
            guard final || now.timeIntervalSince(lastFlush) >= 0.05 else { return }
            let piece = String(text.dropFirst(streamed).prefix(upTo - streamed))
            streamed = upTo
            lastFlush = now
            out.notify(Method.llmToken, LLMTokenParams(job: job, text: piece))
        }
        func add(_ piece: String) {
            text += piece
            for s in stops {
                if let r = text.range(of: s) {
                    text = String(text[..<r.lowerBound])
                    stopped = true
                    return
                }
            }
            flush(final: false)
        }

        let input = LMInput(tokens: MLXArray(prompt.map { Int32($0) }))
        var completion = 0
        var finish = "length"
        var constrained = false
        let decodeStart = Date()
        var freeRun = true
        // AUDIOSMITH_LLM_GRAMMAR=0 turns the JSON grammar off (the prompt
        // and the app's validation remain).
        let grammarOn = ProcessInfo.processInfo.environment["AUDIOSMITH_LLM_GRAMMAR"] != "0"
        if (p.json == true || p.schema != nil) && grammarOn {
            do {
                let gt = try llm.grammarTokenizer()
                let schema: String
                if let s = p.schema, let data = try? JSONEncoder().encode(s), let str = String(data: data, encoding: .utf8) {
                    schema = str
                } else {
                    schema = #"{"type":["object","array"]}"#
                }
                let constraint = try GrammarConstraint(tokenizer: gt, jsonSchema: schema, fastForward: true,
                                                       hostTokenizer: llm.context.tokenizer)
                let bias = llm.grammarBiases()
                let reserve = CompletionReserve.estimate(schemaJSON: schema, tokenizer: llm.context.tokenizer)
                freeRun = false
                constrained = true
                do {
                    completion = try GuidedGenerationLoop.run(input: input, context: llm.context, constraint: constraint,
                                                              maxTokens: maxTokens, vocabSize: gt.vocabSize,
                                                              completionReserve: max(reserve * 3, maxTokens / 4),
                                                              hardReserve: reserve * 8,
                                                              closingBias: bias.closing, whitespaceBias: bias.whitespace,
                                                              whitespaceTokenIDs: bias.whitespaceIDs) { delta in
                        add(delta)
                        return !stopped && !Task.isCancelled
                    }
                    finish = "stop"
                } catch GuidedGenerationError.incompleteOutput {
                    finish = "length"
                    completion = maxTokens
                } catch GuidedGenerationError.prematureEOS {
                    finish = "stop"
                }
            } catch let error as GrammarError {
                // The schema did not compile (an xgrammar limit): generate
                // freely, the app validates.
                out.logLine("warning", "JSON grammar unavailable, generating unconstrained: \(error)", job: job)
            }
        }
        if freeRun {
            let parameters = GenerateParameters(
                maxTokens: maxTokens,
                temperature: Float(p.temperature ?? 0.7),
                topP: Float(p.topP ?? 0.8),
                seed: p.seed.map { UInt64(bitPattern: $0) })
            var iterator = try TokenIterator(input: input, model: llm.context.model, parameters: parameters)
            var detokenizer = NaiveStreamingDetokenizer(tokenizer: llm.context.tokenizer)
            let stopIDs = llm.stopTokenIDs
            while let token = iterator.next() {
                if Task.isCancelled { break }
                if stopIDs.contains(token) {
                    finish = "stop"
                    break
                }
                completion += 1
                detokenizer.append(token: token)
                if let piece = detokenizer.next() { add(piece) }
                if stopped { break }
            }
        }
        try Task.checkCancellation()
        if stopped { finish = "stop" }
        flush(final: true)
        MLX.Memory.clearCache()
        let decodeSeconds = Date().timeIntervalSince(decodeStart)
        return LLMGenerateResult(text: text, finishReason: finish, model: p.model, promptTokens: prompt.count,
                                 completionTokens: completion, seconds: Date().timeIntervalSince(started),
                                 tokensPerSecond: decodeSeconds > 0 ? Double(completion) / decodeSeconds : nil,
                                 constrained: constrained ? true : nil)
    }
}

/// The bytes of a model's weights on disk: what loading it adds, about.
func weightBytes(_ dir: URL) -> Int64 {
    let fm = FileManager.default
    guard let names = try? fm.contentsOfDirectory(atPath: dir.path) else { return 0 }
    var total: Int64 = 0
    for n in names where n.hasSuffix(".safetensors") {
        // Snapshots hold symlinks to blobs: follow them.
        let path = dir.appendingPathComponent(n).resolvingSymlinksInPath().path
        total += ((try? fm.attributesOfItem(atPath: path)[.size]) as? NSNumber)?.int64Value ?? 0
    }
    return total
}
