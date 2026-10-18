import Foundation

// MOSS-TTSD's prompt and its delay-pattern generation (thread #82), without
// MLX so the tests can check them. The official code (OpenMOSS-Team/
// MOSS-TTSD-v1.0 @ c7cd852: processing_moss_tts.py, modeling_moss_tts.py)
// works on rows of 17 ids: a text token and 16 audio codebooks (1024 = no
// code). Audio is written with the delay pattern: codebook i of frame t
// sits in row t + i, so a clip of L frames takes L + 15 rows.
//
// The prompt (the Space's voice-clone-and-continuation mode) is two chat
// messages:
//   user       "<user_inst>\n- Reference(s):\n[S1]:\n<audio>\n[S2]:\n<audio>…
//              - Instruction:\nNone … - Scene:\nNone\n- Text:\n{text}\n</user_inst>"
//              with each speaker's reference clip in user slots;
//   assistant  the speakers' clips concatenated (re-encoded as one clip),
//              cut after its last frame's first codebook: the model goes on
//              from there, finishing the clip's last frames and speaking the
//              dialogue after the transcripts.

public enum MossTTSDTokens {
    public static let nVQ = 16
    public static let audioPad = 1024
    public static let codebookSize = 1025
    public static let textPad = 151_643
    public static let imStart = 151_644
    public static let imEnd = 151_645
    public static let audioStart = 151_652
    public static let audioEnd = 151_653
    public static let userSlot = 151_654
    public static let genSlot = 151_656
    public static let delaySlot = 151_662
    public static let vocabSize = 155_648
    public static let maxSpeakers = 5
}

/// A prompt piece: text to tokenize, a special token, or a clip.
enum MossPiece {
    case text(String)
    case token(Int)
    case audio(codes: [[Int]], user: Bool)
}

public enum MossTTSDPrompt {
    /// The rows of the continuation prompt. references: per speaker, its
    /// clip's codes (frames × 16); prompt: the concatenated clips' codes;
    /// text: MossTTSDText.prompt(…); encode tokenizes plain text.
    public static func rows(text: String, references: [[[Int]]], prompt: [[Int]], encode: (String) -> [Int]) -> [[Int]] {
        var user: [MossPiece] = [.token(MossTTSDTokens.imStart), .text("user\n<user_inst>\n- Reference(s):\n")]
        for (i, r) in references.enumerated() {
            user.append(.text((i > 0 ? "\n" : "") + "[S\(i + 1)]:\n"))
            user.append(.audio(codes: r, user: true))
        }
        if references.isEmpty { user.append(.text("None")) }
        user.append(.text("\n- Instruction:\nNone\n- Tokens:\nNone\n- Quality:\nNone\n- Sound Event:\nNone\n- Ambient Sound:\nNone\n- Language:\nNone\n- Scene:\nNone\n- Text:\n\(text)\n</user_inst>"))
        user += [.token(MossTTSDTokens.imEnd), .text("\n")]
        let assistant: [MossPiece] = [.token(MossTTSDTokens.imStart), .text("assistant\n"), .audio(codes: prompt, user: false)]
        return message(user, truncate: false, encode: encode) + message(assistant, truncate: true, encode: encode)
    }

    /// A message's rows (_get_unified_codes): text and audio blocks; truncate
    /// (the continued assistant message) stops after the last clip's gen slots.
    static func message(_ pieces: [MossPiece], truncate: Bool, encode: (String) -> [Int]) -> [[Int]] {
        var out: [[Int]] = []
        let pad = Array(repeating: MossTTSDTokens.audioPad, count: MossTTSDTokens.nVQ)
        for p in pieces {
            switch p {
            case let .text(s):
                for t in encode(s) { out.append([t] + pad) }
            case let .token(t):
                out.append([t] + pad)
            case let .audio(codes, isUser):
                out.append([MossTTSDTokens.audioStart] + pad)
                let delayed = delay(codes)
                let gen = isUser ? MossTTSDTokens.userSlot : MossTTSDTokens.genSlot
                let del = isUser ? MossTTSDTokens.userSlot : MossTTSDTokens.delaySlot
                let rows = truncate ? codes.count : delayed.count
                for r in 0 ..< rows {
                    out.append([r < codes.count ? gen : del] + delayed[r])
                }
                if !truncate { out.append([MossTTSDTokens.audioEnd] + pad) }
            }
        }
        return out
    }

    /// The delay pattern: frames × 16 → (frames + 15) × 16, padded with 1024.
    public static func delay(_ codes: [[Int]]) -> [[Int]] {
        let n = MossTTSDTokens.nVQ
        guard !codes.isEmpty else { return [] }
        var out = Array(repeating: Array(repeating: MossTTSDTokens.audioPad, count: n), count: codes.count + n - 1)
        for (t, frame) in codes.enumerated() {
            for i in 0 ..< n { out[t + i][i] = frame[i] }
        }
        return out
    }

    /// The inverse (apply_de_delay_pattern): rows × 16 → (rows − 15) × 16.
    public static func undelay(_ rows: [[Int]]) -> [[Int]] {
        let n = MossTTSDTokens.nVQ
        guard rows.count >= n else { return [] }
        return (0 ..< rows.count - n + 1).map { t in (0 ..< n).map { rows[t + $0][$0] } }
    }

    /// The first clip of a generation (_parse_audio_codes): the assistant
    /// rows from its audio start (row 0), undelayed; frames that are all
    /// 1024 separate clips. Returns the first clip's frames and how many of
    /// them the prompt's clip took (start rows), for the trim.
    public static func firstClip(_ assistant: [[Int]]) -> [[Int]] {
        let frames = undelay(assistant.map { Array($0.dropFirst()) })
        var start = -1, end = frames.count
        for (t, f) in frames.enumerated() {
            let pad = f.allSatisfy { $0 == MossTTSDTokens.audioPad }
            if start < 0 {
                if !pad { start = t }
            } else if pad {
                end = t
                break
            }
        }
        return start < 0 ? [] : Array(frames[start ..< end])
    }
}

/// The delay-pattern generation's state (MossTTSDelayModel.generate, batch
/// 1): what the next row's text token is (forced, or sampled under which
/// mask) and which codebooks are sampled; the rest of the row is 1024.
public struct MossTTSDState: Sendable {
    public enum Text: Equatable, Sendable {
        case forced(Int)
        /// Sampled. inAudio: only the gen and delay slots; else every token
        /// but the pad, slots and audio end. noDelay: not the delay slot
        /// (the first step); noEnd: not <|im_end|> (the first 17 steps).
        case sample(inAudio: Bool, noDelay: Bool, noEnd: Bool)
    }

    public private(set) var audioLengths = 0
    /// nil: not in the delay tail (int64 max upstream).
    public private(set) var delayed: Int?
    public private(set) var inAudio = false
    public private(set) var stopping = false
    public private(set) var step = 0

    /// From the prompt's text column.
    public init(promptText: [Int]) {
        let last = promptText.last ?? 0
        let continuation = last == MossTTSDTokens.audioStart || last == MossTTSDTokens.genSlot
        if continuation, let start = promptText.lastIndex(of: MossTTSDTokens.audioStart) {
            audioLengths = promptText.count - start
            inAudio = true
        }
    }

    public var text: Text {
        let n = MossTTSDTokens.nVQ
        if stopping { return .forced(MossTTSDTokens.textPad) }
        if let d = delayed, d < n { return .forced(MossTTSDTokens.delaySlot) }
        if delayed == n { return .forced(MossTTSDTokens.audioEnd) }
        return .sample(inAudio: inAudio, noDelay: step == 0, noEnd: step <= n)
    }

    /// The codebooks this row samples (before the row's text is known).
    public var channels: [Bool] {
        (0 ..< MossTTSDTokens.nVQ).map { i in audioLengths > i && (delayed == nil || i > delayed! - 1) }
    }

    /// The row's text token is t: update for the next row.
    public mutating func advance(text t: Int) {
        let n = MossTTSDTokens.nVQ
        if delayed == n { inAudio = false } // set with the forced audio end
        if t == MossTTSDTokens.audioStart { inAudio = true }
        if t == MossTTSDTokens.imEnd { stopping = true }
        if [MossTTSDTokens.audioStart, MossTTSDTokens.genSlot, MossTTSDTokens.delaySlot].contains(t) { audioLengths += 1 }
        if t == MossTTSDTokens.audioEnd { audioLengths = 0 }
        if delayed == nil, t == MossTTSDTokens.delaySlot { delayed = 0 }
        if let d = delayed { delayed = d + 1 > n ? nil : d + 1 }
        step += 1
    }
}

public struct MossTTSDSampling: Equatable, Sendable {
    public var audioTemperature: Float = 1.1
    public var audioTopP: Float = 0.9
    public var audioTopK = 50
    public var audioRepetitionPenalty: Float = 1.1
    public var textTemperature: Float = 1.1
    public var textTopP: Float = 0.9
    public var textTopK = 50
    /// Rows to generate at most (12.5 a second; the Space's default 2000).
    public var maxTokens = 2000

    public init() {}
}

/// The official sampling (inference_utils.sample_token), on the CPU: the
/// logits divided by the temperature, the code 1024 removed (audio), the
/// repetition penalty on every code seen before in the sequence (prompt
/// included: codebook 0 over its own column, codebooks 1–15 over the union
/// of theirs, as upstream's masked 2-D call does), top-k, top-p (the
/// smallest set whose probability passes p), then a draw. Temperature 0:
/// the argmax after the penalty.
public struct MossTTSDSampler: Sendable {
    public var config: MossTTSDSampling
    public var rng: SeededGenerator

    public init(config: MossTTSDSampling = MossTTSDSampling(), seed: UInt64) {
        self.config = config
        rng = SeededGenerator(seed: seed)
    }

    /// One audio row's processed logits (removed entries -infinity).
    public func processAudio(_ logits: [Float], seen: [Bool]) -> [Float] {
        let t = config.audioTemperature > 0 ? config.audioTemperature : 1
        var s = logits.map { $0 / t }
        s[MossTTSDTokens.audioPad] = -.infinity
        let p = config.audioRepetitionPenalty
        if p != 1 {
            for i in s.indices where i < seen.count && seen[i] {
                s[i] = s[i] > 0 ? s[i] / p : s[i] * p
            }
        }
        if config.audioTemperature <= 0 { return s }
        return Self.topKP(s, k: config.audioTopK, p: config.audioTopP)
    }

    /// The text row's processed logits; allowed says which ids may be drawn.
    public func processText(_ logits: [Float], allowed: (Int) -> Bool) -> [Float] {
        let t = config.textTemperature > 0 ? config.textTemperature : 1
        var s = logits.map { $0 / t }
        for i in s.indices where !allowed(i) { s[i] = -.infinity }
        if config.textTemperature <= 0 { return s }
        return Self.topKP(s, k: config.textTopK, p: config.textTopP)
    }

    static func topKP(_ x: [Float], k: Int, p: Float) -> [Float] {
        var s = x
        let order = s.indices.filter { s[$0] > -.infinity }.sorted { s[$0] > s[$1] }
        if k > 0, order.count > k {
            for i in order[k...] { s[i] = -.infinity }
        }
        if p < 1 {
            let kept = Array(order.prefix(k > 0 ? k : order.count))
            let probs = softmax(kept.map { s[$0] })
            var cum = 0.0
            for (n, i) in kept.enumerated() {
                if n > 0, cum > Double(p) { s[i] = -.infinity }
                cum += probs[n]
            }
        }
        return s
    }

    /// Draws from processed logits (argmax when the temperature is 0).
    public mutating func draw(_ processed: [Float], greedy: Bool) -> Int {
        if greedy {
            var best = 0
            for i in processed.indices where processed[i] > processed[best] { best = i }
            return best
        }
        let probs = softmax(processed)
        let u = rng.unit()
        var cum = 0.0
        var last = 0
        for (i, q) in probs.enumerated() where q > 0 {
            cum += q
            last = i
            if u < cum { return i }
        }
        return last
    }

    public mutating func sampleAudio(_ logits: [Float], seen: [Bool]) -> Int {
        draw(processAudio(logits, seen: seen), greedy: config.audioTemperature <= 0)
    }

    public mutating func sampleText(_ logits: [Float], allowed: (Int) -> Bool) -> Int {
        draw(processText(logits, allowed: allowed), greedy: config.textTemperature <= 0)
    }
}
