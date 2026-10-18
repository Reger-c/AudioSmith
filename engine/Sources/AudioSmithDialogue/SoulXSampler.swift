import Foundation

// SoulX-Podcast's sampling (thread #80), without MLX so the tests can check
// it against transformers. Upstream (Soul-AILab/SoulX-Podcast, pinned
// transformers 4.57.1) samples each speech token as follows:
//
//  1. `generate()` builds its logits processors for do_sample (captured
//     from generate() itself by scripts/soulx/reference.py):
//     MinLengthLogitsProcessor and MinNewTokensLengthLogitsProcessor (both
//     make EOS 151675 -inf until 8 new tokens), then the caller's RepetitionPenaltyLogitsProcessor (1.25, over
//     this turn's generated tokens only: prompt_ignore_length = the prompt),
//     then the warpers TemperatureLogitsWarper (0.6), TopKLogitsWarper (100)
//     and TopPLogitsWarper (0.9), and hands the list to SoulX's custom loop;
//  2. the loop (`_ras_sample_hf_engine`, VALL-E 2's repetition aware
//     sampling) draws a candidate from softmax(processed); when it occurs
//     with the candidate counted at least win_size × tau_r (25 × 0.2 = 5)
//     times in the last 25 ids of the sequence, the step samples from the
//     raw logits instead (no processors at all);
//  3. the token is a new multinomial draw from that distribution;
//  4. it stops at EOS or after 3000 new tokens.
//
// The community MLX port dropped step 1's warpers (CER 1 % → 4–10 %,
// research/dialogue-port-eval.md §5.3). One deliberate difference: the
// engine computes logits only for the speech tokens and EOS (the "local"
// vocabulary, SoulXVocab), so neither a processed nor a raw draw can pick a
// text token.

/// SoulX-Podcast's token ids.
public enum SoulXVocab {
    /// Speech token k is id speechOffset + k.
    public static let speechOffset = 152_927
    public static let speechCount = 6561
    /// `<|semantic_token_end|>`: the end of a turn's speech.
    public static let eos = 151_675
    /// Local indices: 0..<speechCount are the speech tokens, speechCount is EOS.
    public static let localCount = speechCount + 1
    public static let localEOS = speechCount

    public static func global(_ local: Int) -> Int {
        local == localEOS ? eos : speechOffset + local
    }

    /// The local index of a vocabulary id, nil for text tokens.
    public static func local(_ id: Int) -> Int? {
        if id == eos { return localEOS }
        let k = id - speechOffset
        return k >= 0 && k < speechCount ? k : nil
    }
}

public struct SoulXSampling: Equatable, Sendable {
    public var temperature: Float = 0.6
    public var repetitionPenalty: Float = 1.25
    public var topK = 100
    public var topP: Float = 0.9
    public var minTokens = 8
    public var maxTokens = 3000
    /// Repetition aware sampling: window and threshold ratio (0 window = off).
    public var rasWindow = 25
    public var rasTau: Float = 0.2

    public init() {}
}

/// A seedable generator (xoshiro256**, seeded by splitmix64): the same seed
/// gives the same draws on every Mac.
public struct SeededGenerator: RandomNumberGenerator, Sendable {
    private var s: (UInt64, UInt64, UInt64, UInt64)

    public init(seed: UInt64) {
        var x = seed
        func splitmix() -> UInt64 {
            x &+= 0x9E37_79B9_7F4A_7C15
            var z = x
            z = (z ^ (z >> 30)) &* 0xBF58_476D_1CE4_E5B9
            z = (z ^ (z >> 27)) &* 0x94D0_49BB_1331_11EB
            return z ^ (z >> 31)
        }
        s = (splitmix(), splitmix(), splitmix(), splitmix())
    }

    public mutating func next() -> UInt64 {
        let result = ((s.1 &* 5) << 7 | (s.1 &* 5) >> 57) &* 9
        let t = s.1 << 17
        s.2 ^= s.0
        s.3 ^= s.1
        s.1 ^= s.2
        s.0 ^= s.3
        s.2 ^= t
        s.3 = s.3 << 45 | s.3 >> 19
        return result
    }

    /// Uniform in [0, 1).
    public mutating func unit() -> Double {
        Double(next() >> 11) * 0x1.0p-53
    }
}

public struct SoulXSampler: Sendable {
    public var config: SoulXSampling
    public var rng: SeededGenerator
    /// Steps that fell back to the raw logits (RAS), for logs.
    public private(set) var rasFallbacks = 0

    public init(config: SoulXSampling = SoulXSampling(), seed: UInt64) {
        self.config = config
        self.rng = SeededGenerator(seed: seed)
    }

    /// The processed scores of one step (transformers' order): EOS masked
    /// while fewer than minTokens tokens are generated, the repetition
    /// penalty on the distinct generated tokens, temperature, top-k, top-p.
    /// logits and the result are over the local vocabulary; generated holds
    /// this turn's tokens as local indices. Removed entries are -infinity.
    public func process(_ logits: [Float], generated: [Int]) -> [Float] {
        var s = logits
        let eos = s.count - 1
        if generated.count < config.minTokens { s[eos] = -.infinity }
        if config.repetitionPenalty != 1 {
            let p = config.repetitionPenalty
            for t in Set(generated) where t >= 0 && t < s.count {
                let v = s[t]
                s[t] = v < 0 ? v * p : v / p
            }
        }
        if config.temperature != 1, config.temperature > 0 {
            let inv = config.temperature
            for i in s.indices { s[i] = s[i] / inv }
        }
        if config.topK > 0, config.topK < s.count {
            // scores < the k-th largest are removed (ties with it stay).
            let kth = s.sorted(by: >)[config.topK - 1]
            for i in s.indices where s[i] < kth { s[i] = -.infinity }
        }
        if config.topP < 1 {
            // Ascending order, cumulative softmax; entries whose cumulative
            // probability is ≤ 1 - top_p go, the largest always stays.
            // (Removed entries add nothing to the sums: only the rest is sorted.)
            let order = s.indices.filter { s[$0] > -.infinity }.sorted { s[$0] < s[$1] }
            let probs = softmax(order.map { s[$0] })
            var cum = 0.0
            let limit = Double(1 - config.topP)
            for (n, i) in order.enumerated() {
                cum += probs[n]
                if n < order.count - 1, cum <= limit { s[i] = -.infinity }
            }
        }
        return s
    }

    /// Draws the next token (a local index). window is the last ids of the
    /// whole sequence (prompt and this turn so far, vocabulary ids), at most
    /// rasWindow of them; generated is this turn's tokens as local indices.
    public mutating func next(_ logits: [Float], window: [Int], generated: [Int]) -> Int {
        let processed = process(logits, generated: generated)
        var probs = softmax(processed)
        if config.rasWindow > 0 {
            let candidate = draw(probs)
            let id = SoulXVocab.global(candidate)
            let repeats = window.suffix(config.rasWindow).filter { $0 == id }.count + 1
            if Float(repeats) >= Float(config.rasWindow) * config.rasTau {
                probs = softmax(logits)
                rasFallbacks += 1
            }
        }
        return draw(probs)
    }

    mutating func draw(_ probs: [Double]) -> Int {
        let u = rng.unit()
        var cum = 0.0
        var last = 0
        for (i, p) in probs.enumerated() where p > 0 {
            cum += p
            last = i
            if u < cum { return i }
        }
        return last
    }
}

/// softmax in double precision; -infinity gives 0.
public func softmax(_ x: [Float]) -> [Double] {
    let m = x.max() ?? 0
    guard m.isFinite else { return x.map { $0 == m ? 1 / Double(x.count) : 0 } }
    var e = x.map { $0 == -.infinity ? 0 : exp(Double($0 - m)) }
    let sum = e.reduce(0, +)
    for i in e.indices { e[i] /= sum }
    return e
}
