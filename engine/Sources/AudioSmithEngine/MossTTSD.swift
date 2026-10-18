import AudioSmithDialogue
import Foundation
@preconcurrency import MLX
import MLXFast
import MLXLMCommon
import MLXNN
import Tokenizers

// MOSS-TTSD v1.0 (OpenMOSS, Apache-2.0; thread #82): a dialogue model that
// speaks a whole conversation of up to five cloned voices in one track.
// A Qwen3-8B backbone reads rows of a text token plus 16 audio codebooks
// (MOSS-Audio-Tokenizer's, 12.5 frames a second, written with the delay
// pattern) and 17 heads predict the next row. Our own port of the official
// modeling_moss_tts.py @ c7cd852, on MLX/MLXNN/MLXLMCommon's public types
// (mlx-audio-swift's MossTTSModel keeps its layers internal, loads no
// quantized weights and synchronizes 16 times a step):
//
//   - the prompt and the generation's state machine are AudioSmithDialogue's
//     (MossTTSDPrompt, MossTTSDState), the sampling too (MossTTSDSampler, on
//     the CPU: one GPU→CPU copy a step);
//   - a step computes only the logits it needs: the 16 audio heads fused
//     into one matrix, and of the text head the gen and delay slots while
//     inside audio (the official mask leaves nothing else), all 155,648 ids
//     otherwise (the few steps after the audio ends).
//
// The model folder (scripts/mossttsd/convert.py): config.json (upstream's,
// plus quantization), model-*.safetensors (8-bit, upstream's names), the
// tokenizer files, codec_config.json and codec.safetensors (the codec).

final class MossQwenAttention: Module {
    let heads: Int, kvHeads: Int, headDim: Int
    let scale: Float
    @ModuleInfo(key: "q_proj") var q: Linear
    @ModuleInfo(key: "k_proj") var k: Linear
    @ModuleInfo(key: "v_proj") var v: Linear
    @ModuleInfo(key: "o_proj") var o: Linear
    @ModuleInfo(key: "q_norm") var qNorm: RMSNorm
    @ModuleInfo(key: "k_norm") var kNorm: RMSNorm
    let rope: RoPE
    /// Attention without the fused kernel (MOSS_MANUAL_ATTENTION=1; parity checks).
    nonisolated(unsafe) static var manual = ProcessInfo.processInfo.environment["MOSS_MANUAL_ATTENTION"] == "1"

    init(_ c: MossLMConfig) {
        heads = c.heads
        kvHeads = c.kvHeads
        headDim = c.headDim
        scale = 1 / Float(c.headDim).squareRoot()
        _q.wrappedValue = Linear(c.hidden, heads * headDim, bias: false)
        _k.wrappedValue = Linear(c.hidden, kvHeads * headDim, bias: false)
        _v.wrappedValue = Linear(c.hidden, kvHeads * headDim, bias: false)
        _o.wrappedValue = Linear(heads * headDim, c.hidden, bias: false)
        _qNorm.wrappedValue = RMSNorm(dimensions: headDim, eps: c.eps)
        _kNorm.wrappedValue = RMSNorm(dimensions: headDim, eps: c.eps)
        rope = RoPE(dimensions: headDim, traditional: false, base: c.ropeTheta)
    }

    func callAsFunction(_ x: MLXArray, cache: KVCache) -> MLXArray {
        let (B, L) = (x.dim(0), x.dim(1))
        var queries = qNorm(q(x).reshaped(B, L, heads, headDim)).transposed(0, 2, 1, 3)
        var keys = kNorm(k(x).reshaped(B, L, kvHeads, headDim)).transposed(0, 2, 1, 3)
        let values = v(x).reshaped(B, L, kvHeads, headDim).transposed(0, 2, 1, 3)
        queries = rope(queries, offset: cache.offset)
        keys = rope(keys, offset: cache.offset)
        let (ks, vs) = cache.update(keys: keys, values: values)
        let out: MLXArray
        if MossQwenAttention.manual {
            // Plain matmuls and a precise softmax (the float32 parity check).
            let rep = heads / kvHeads
            let k2 = rep > 1 ? MLX.repeated(ks, count: rep, axis: 1) : ks
            let v2 = rep > 1 ? MLX.repeated(vs, count: rep, axis: 1) : vs
            var s = matmul(queries * scale, k2.transposed(0, 1, 3, 2))
            if L > 1 {
                let S = ks.dim(2)
                let qi = MLXArray(0 ..< L).reshaped(L, 1) + (S - L), ki = MLXArray(0 ..< S).reshaped(1, S)
                s = MLX.where(ki .<= qi, s, MLXArray(-Float.infinity))
            }
            out = matmul(softmax(s, axis: -1, precise: true), v2)
        } else {
            out = MLXFast.scaledDotProductAttention(queries: queries, keys: ks, values: vs, scale: scale,
                                                    mask: L > 1 ? .causal : .none)
        }
        return o(out.transposed(0, 2, 1, 3).reshaped(B, L, -1))
    }
}

final class MossQwenMLP: Module {
    @ModuleInfo(key: "gate_proj") var gate: Linear
    @ModuleInfo(key: "up_proj") var up: Linear
    @ModuleInfo(key: "down_proj") var down: Linear

    init(_ c: MossLMConfig) {
        _gate.wrappedValue = Linear(c.hidden, c.intermediate, bias: false)
        _up.wrappedValue = Linear(c.hidden, c.intermediate, bias: false)
        _down.wrappedValue = Linear(c.intermediate, c.hidden, bias: false)
    }

    func callAsFunction(_ x: MLXArray) -> MLXArray { down(silu(gate(x)) * up(x)) }
}

final class MossQwenBlock: Module {
    @ModuleInfo(key: "self_attn") var attn: MossQwenAttention
    @ModuleInfo var mlp: MossQwenMLP
    @ModuleInfo(key: "input_layernorm") var norm1: RMSNorm
    @ModuleInfo(key: "post_attention_layernorm") var norm2: RMSNorm

    init(_ c: MossLMConfig) {
        _attn.wrappedValue = MossQwenAttention(c)
        _mlp.wrappedValue = MossQwenMLP(c)
        _norm1.wrappedValue = RMSNorm(dimensions: c.hidden, eps: c.eps)
        _norm2.wrappedValue = RMSNorm(dimensions: c.hidden, eps: c.eps)
    }

    func callAsFunction(_ x: MLXArray, cache: KVCache) -> MLXArray {
        let h = x + attn(norm1(x), cache: cache)
        return h + mlp(norm2(h))
    }
}

final class MossQwenModel: Module {
    @ModuleInfo(key: "embed_tokens") var embed: Embedding
    @ModuleInfo var layers: [MossQwenBlock]
    @ModuleInfo var norm: RMSNorm

    init(_ c: MossLMConfig) {
        _embed.wrappedValue = Embedding(embeddingCount: c.vocab, dimensions: c.hidden)
        _layers.wrappedValue = (0 ..< c.layers).map { _ in MossQwenBlock(c) }
        _norm.wrappedValue = RMSNorm(dimensions: c.hidden, eps: c.eps)
    }
}

/// MossTTSDelayModel: the backbone, the 16 audio embeddings and the 17 heads.
final class MossTTSDLM: Module {
    @ModuleInfo(key: "language_model") var lm: MossQwenModel
    @ModuleInfo(key: "emb_ext") var embExt: [Embedding]
    @ModuleInfo(key: "lm_heads") var heads: [Linear]

    init(_ c: MossLMConfig) {
        _lm.wrappedValue = MossQwenModel(c)
        _embExt.wrappedValue = (0 ..< c.nVQ).map { _ in Embedding(embeddingCount: c.audioVocab + 1, dimensions: c.hidden) }
        _heads.wrappedValue = [Linear(c.hidden, c.vocab, bias: false)]
            + (0 ..< c.nVQ).map { _ in Linear(c.hidden, c.audioVocab + 1, bias: false) }
    }
}

struct MossLMConfig {
    var hidden = 4096, intermediate = 12288, layers = 36, heads = 32, kvHeads = 8, headDim = 128
    var vocab = 155_648, eps: Float = 1e-6, ropeTheta: Float = 1_000_000
    var nVQ = 16, audioVocab = 1024
    var quantization: (groupSize: Int, bits: Int)?

    init(_ dir: URL) throws {
        let data = try Data(contentsOf: dir.appendingPathComponent("config.json"))
        guard let c = try JSONSerialization.jsonObject(with: data) as? [String: Any],
              let lc = c["language_config"] as? [String: Any] else {
            throw EngineError.loadFailed("MOSS-TTSD: config.json has no language_config")
        }
        func int(_ d: [String: Any], _ k: String, _ v: Int) -> Int { d[k] as? Int ?? v }
        func num(_ d: [String: Any], _ k: String, _ v: Double) -> Double { (d[k] as? NSNumber)?.doubleValue ?? v }
        hidden = int(lc, "hidden_size", hidden)
        intermediate = int(lc, "intermediate_size", intermediate)
        layers = int(lc, "num_hidden_layers", layers)
        heads = int(lc, "num_attention_heads", heads)
        kvHeads = int(lc, "num_key_value_heads", kvHeads)
        headDim = int(lc, "head_dim", hidden / heads)
        vocab = int(lc, "vocab_size", vocab)
        eps = Float(num(lc, "rms_norm_eps", 1e-6))
        ropeTheta = Float(num(lc, "rope_theta", 1_000_000))
        nVQ = int(c, "n_vq", nVQ)
        audioVocab = int(c, "audio_vocab_size", audioVocab)
        if let q = c["quantization"] as? [String: Any], let g = q["group_size"] as? Int, let b = q["bits"] as? Int {
            quantization = (g, b)
        }
        guard nVQ == MossTTSDTokens.nVQ, audioVocab == MossTTSDTokens.audioPad else {
            throw EngineError.loadFailed("MOSS-TTSD: \(nVQ) codebooks of \(audioVocab) codes; the engine reads 16 of 1024")
        }
    }
}

/// A speaker's reference: its clip (24 kHz, as given) and its codes.
struct MossReference {
    let samples: [Float]
    let codes: [[Int]]
}

final class MossTTSDModel: @unchecked Sendable {
    static let sampleRate = 24000
    /// Rows a second (one row a codec frame).
    static let frameRate = 12.5
    /// Longest reference clip.
    static let maxReferenceSeconds = 30.0

    let config: MossLMConfig
    let lm: MossTTSDLM
    /// nil: loaded without it (the language model's checks).
    let codec: MossCodec?
    /// nil: loaded without it (the tiny model's check).
    let tokenizer: (any Tokenizers.Tokenizer)?
    /// The 16 audio heads as one (16 × 1025 rows), and the text head's
    /// gen- and delay-slot rows.
    private let audioHead: (weight: MLXArray, scales: MLXArray?, biases: MLXArray?)
    private let slotHead: (weight: MLXArray, scales: MLXArray?, biases: MLXArray?)
    private var references: [String: MossReference] = [:]

    static func isModel(_ dir: URL) -> Bool {
        let fm = FileManager.default
        guard fm.fileExists(atPath: dir.appendingPathComponent("codec.safetensors").path),
              let data = try? Data(contentsOf: dir.appendingPathComponent("config.json")),
              let c = try? JSONSerialization.jsonObject(with: data) as? [String: Any] else { return false }
        return c["model_type"] as? String == "moss_tts_delay" && c["n_vq"] as? Int == 16
    }

    init(config: MossLMConfig, lm: MossTTSDLM, codec: MossCodec?, tokenizer: (any Tokenizers.Tokenizer)?) {
        self.config = config
        self.lm = lm
        self.codec = codec
        self.tokenizer = tokenizer
        func parts(_ l: Linear) -> (MLXArray, MLXArray?, MLXArray?) {
            if let q = l as? QuantizedLinear { return (q.weight, q.scales, q.biases) }
            return (l.weight, nil, nil)
        }
        let audio = lm.heads.dropFirst().map(parts)
        audioHead = (concatenated(audio.map { $0.0 }, axis: 0), audio[0].1 == nil ? nil : concatenated(audio.map { $0.1! }, axis: 0),
                     audio[0].2 == nil ? nil : concatenated(audio.map { $0.2! }, axis: 0))
        let text = parts(lm.heads[0])
        let rows = MLXArray([Int32(MossTTSDTokens.genSlot), Int32(MossTTSDTokens.delaySlot)])
        slotHead = (text.0.take(rows, axis: 0), text.1?.take(rows, axis: 0), text.2?.take(rows, axis: 0))
        eval(audioHead.weight, slotHead.weight)
        if let s = audioHead.scales { eval(s, audioHead.biases!) }
        if let s = slotHead.scales { eval(s, slotHead.biases!) }
    }

    /// Loads the model folder. codecDir: the codec's folder (default dir);
    /// fp32: scales, biases and norms in float32 (parity checks).
    static func load(_ dir: URL, codecDir: URL? = nil, fp32: Bool = false, withCodec: Bool = true) async throws -> MossTTSDModel {
        let config = try MossLMConfig(dir)
        let lm = MossTTSDLM(config)
        var weights: [String: MLXArray] = [:]
        for f in try FileManager.default.contentsOfDirectory(at: dir, includingPropertiesForKeys: nil)
            where f.pathExtension == "safetensors" && f.lastPathComponent.hasPrefix("model") {
            for (k, v) in try MLX.loadArrays(url: f) { weights[k] = v }
        }
        if let q = config.quantization {
            quantize(model: lm, filter: { path, m in
                (m is Linear || m is Embedding) && weights["\(path).scales"] != nil ? (q.groupSize, q.bits, .affine) : nil
            })
        }
        if fp32 {
            for (k, v) in weights where v.dtype == .bfloat16 || v.dtype == .float16 { weights[k] = v.asType(.float32) }
        }
        try lm.update(parameters: ModuleParameters.unflattened(weights), verify: .all)
        eval(lm)
        let hasTokenizer = FileManager.default.fileExists(atPath: dir.appendingPathComponent("tokenizer.json").path)
        let tokenizer: (any Tokenizers.Tokenizer)? = hasTokenizer ? try await AutoTokenizer.from(modelFolder: dir) : nil
        let codec = withCodec ? try MossCodec.load(codecDir ?? dir) : nil
        return MossTTSDModel(config: config, lm: lm, codec: codec, tokenizer: tokenizer)
    }

    func encode(_ text: String) -> [Int] {
        tokenizer?.encode(text: text, addSpecialTokens: false) ?? []
    }

    // MARK: References

    /// A clip at path (24 kHz mono), cached by path, size and date.
    func reference(_ path: String) async throws -> MossReference {
        let attrs = try? FileManager.default.attributesOfItem(atPath: path)
        let key = "\(path)|\((attrs?[.size] as? NSNumber)?.int64Value ?? 0)|\((attrs?[.modificationDate] as? Date)?.timeIntervalSince1970 ?? 0)"
        if let r = references[key] { return r }
        let samples = try await decodeMono(path, sampleRate: Self.sampleRate)
        let seconds = Double(samples.count) / Double(Self.sampleRate)
        guard seconds <= Self.maxReferenceSeconds else {
            throw EngineError.badRequest(String(format: "The reference %@ is %.0f s long; MOSS-TTSD takes at most %.0f s", path, seconds, Self.maxReferenceSeconds))
        }
        guard seconds >= 1 else { throw EngineError.badRequest("The reference \(path) is shorter than 1 s") }
        guard let codec else { throw EngineError.loadFailed("MOSS-TTSD: loaded without its codec") }
        let r = MossReference(samples: samples, codes: codec.encode(MossCodec.loudnessNormalize(samples)))
        if references.count >= 16 { references.removeAll() }
        references[key] = r
        return r
    }

    // MARK: Language model

    /// The embeddings of rows (T × 17): the text token's plus each codebook's.
    func embed(_ rows: ArraySlice<[Int]>) -> MLXArray {
        let ids = MLXArray(rows.flatMap { $0.map { Int32($0) } }).reshaped(1, rows.count, config.nVQ + 1)
        var h = lm.lm.embed(ids[0..., 0..., 0])
        for (i, e) in lm.embExt.enumerated() { h = h + e(ids[0..., 0..., i + 1]) }
        return h
    }

    /// Reads rows into the cache; returns the last row's final hidden state (1, hidden).
    func read(_ rows: ArraySlice<[Int]>, cache: [KVCache], chunk: Int = 256) -> MLXArray {
        var last: MLXArray?
        var i = rows.startIndex
        while i < rows.endIndex {
            let j = min(i + chunk, rows.endIndex)
            var h = embed(rows[i ..< j])
            for (l, layer) in lm.lm.layers.enumerated() { h = layer(h, cache: cache[l]) }
            last = lm.lm.norm(h[0..., (h.dim(1) - 1)...]).reshaped(1, -1)
            eval(last!)
            i = j
        }
        return last!
    }

    /// The last row's hidden state after the first `layers` layers (no
    /// final norm): the parity checks' first layer.
    func hiddenAfter(_ rows: [[Int]], layers: Int) -> MLXArray {
        var h = embed(rows[...])
        let cache: [KVCache] = (0 ..< config.layers).map { _ in KVCacheSimple() }
        for l in 0 ..< layers { h = lm.lm.layers[l](h, cache: cache[l]) }
        return h[0, h.dim(1) - 1]
    }

    /// Hidden states of every row (no cache): the parity checks' logits.
    func hiddenAll(_ rows: [[Int]]) -> MLXArray {
        let cache: [KVCache] = (0 ..< config.layers).map { _ in KVCacheSimple() }
        var outs: [MLXArray] = []
        var i = 0
        while i < rows.count {
            let j = min(i + 256, rows.count)
            var h = embed(rows[i ..< j])
            for (l, layer) in lm.lm.layers.enumerated() { h = layer(h, cache: cache[l]) }
            let n = lm.lm.norm(h)
            eval(n)
            outs.append(n)
            i = j
        }
        return concatenated(outs, axis: 1)[0]
    }

    private func project(_ h: MLXArray, _ head: (weight: MLXArray, scales: MLXArray?, biases: MLXArray?)) -> MLXArray {
        if let s = head.scales, let q = config.quantization {
            return quantizedMM(h, head.weight, scales: s, biases: head.biases, transpose: true, groupSize: q.groupSize, bits: q.bits)
        }
        return matmul(h, head.weight.transposed())
    }

    /// The audio heads' logits of hidden states (…, hidden): (…, 16, 1025), the
    /// last code of each -infinity as the official forward makes it.
    func audioLogits(_ h: MLXArray) -> MLXArray {
        let l = project(h, audioHead).reshaped(Array(h.shape.dropLast()) + [config.nVQ, config.audioVocab + 1]).asType(.float32)
        let last = MLXArray(0 ..< (config.audioVocab + 1)) .== MLXArray(config.audioVocab)
        return MLX.where(last, MLXArray(-Float.infinity), l)
    }

    func textLogits(_ h: MLXArray) -> MLXArray {
        lm.heads[0](h).asType(.float32)
    }

    /// Generates rows after the prompt rows until <|im_end|> or maxTokens.
    /// Returns the new rows.
    func generate(_ prompt: [[Int]], sampler: inout MossTTSDSampler, onRow: (Int) -> Void = { _ in }) throws -> [[Int]] {
        let cache: [KVCache] = (0 ..< config.layers).map { _ in KVCacheSimple() }
        var hidden = read(prompt[...], cache: cache)
        let n = config.nVQ, codes = config.audioVocab + 1
        var seen0 = [Bool](repeating: false, count: codes), seenRest = [Bool](repeating: false, count: codes)
        for r in prompt {
            seen0[r[1]] = true
            for i in 2 ... n { seenRest[r[i]] = true }
        }
        var state = MossTTSDState(promptText: prompt.map { $0[0] })
        var out: [[Int]] = []
        for _ in 0 ..< sampler.config.maxTokens {
            try Task.checkCancellation()
            let action = state.text
            let channels = state.channels
            let wantAudio = channels.contains(true)
            var audio: MLXArray?, text: MLXArray?
            if wantAudio { audio = audioLogits(hidden).reshaped(-1) }
            if case let .sample(inAudio, _, _) = action {
                text = inAudio ? project(hidden, slotHead).asType(.float32).reshaped(-1) : textLogits(hidden).reshaped(-1)
            }
            var arrays: [MLXArray] = []
            if let audio { arrays.append(audio) }
            if let text { arrays.append(text) }
            eval(arrays)
            var t: Int
            switch action {
            case let .forced(id):
                t = id
            case let .sample(inAudio, noDelay, noEnd):
                let logits = text!.asArray(Float.self)
                if inAudio {
                    let k = sampler.sampleText(logits) { !(noDelay && $0 == 1) }
                    t = k == 0 ? MossTTSDTokens.genSlot : MossTTSDTokens.delaySlot
                } else {
                    let excluded: Set<Int> = [MossTTSDTokens.textPad, MossTTSDTokens.genSlot, MossTTSDTokens.delaySlot, MossTTSDTokens.audioEnd]
                    t = sampler.sampleText(logits) { id in
                        !excluded.contains(id) && !(noEnd && id == MossTTSDTokens.imEnd)
                    }
                }
            }
            var row = [t] + Array(repeating: MossTTSDTokens.audioPad, count: n)
            if let audio {
                let flat = audio.asArray(Float.self)
                for i in 0 ..< n where channels[i] {
                    let logits = Array(flat[(i * codes) ..< ((i + 1) * codes)])
                    row[i + 1] = sampler.sampleAudio(logits, seen: i == 0 ? seen0 : seenRest)
                }
            }
            seen0[row[1]] = true
            for i in 2 ... n { seenRest[row[i]] = true }
            out.append(row)
            state.advance(text: t)
            onRow(out.count)
            if state.stopping { break }
            hidden = read([row][...], cache: cache)
        }
        return out
    }

    // MARK: Audio

    /// The new speech of a generation: the assistant part of the prompt
    /// (from its audio start) and the new rows, undelayed, the first clip
    /// decoded whole (the prompt's clip is the codec's left context) and
    /// trimmed as the official processor does (by the prompt's share of
    /// the clip's frames, the audio-start row counted). Also the frames
    /// from the trim on: the first frame is the cut one, codes[1...] lie
    /// under the samples.
    func speech(prompt: [[Int]], generated: [[Int]], cancelled: () -> Bool = { false }) throws -> (samples: [Float], frames: [[Int]]) {
        guard let imStart = prompt.lastIndex(where: { $0[0] == MossTTSDTokens.imStart }) else { return ([], []) }
        let start = imStart + 3
        let startLength = prompt.count - start
        let clip = MossTTSDPrompt.firstClip(Array(prompt[start...]) + generated)
        guard !clip.isEmpty else { return ([], []) }
        let ratio = max(0, min(Double(startLength) / Double(clip.count), 1))
        guard ratio < 1, let codec else { return ([], []) }
        let samples = try codec.decode(clip, cancelled: cancelled)
        let trim = Int(Double(samples.count) * ratio)
        let first = max(startLength - 1, 0)
        return (Array(samples[trim...]), Array(clip[min(first, clip.count)...]))
    }
}
