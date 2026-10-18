import AudioSmithDialogue
import Foundation
@preconcurrency import MLX
import MLXAudioCodecs
import MLXLLM
import MLXLMCommon
import MLXNN

// SoulX-Podcast-1.7B (Soul AI Lab, Apache-2.0; thread #80): a dialogue
// model. A Qwen3-1.7B language model writes each turn's speech tokens
// (25 Hz) after hearing every turn before it; CosyVoice2's flow matching
// turns them into a 24 kHz mel, conditioned on the speaker's reference
// tokens, mel and CAM++ x-vector, and HiFT makes the waveform. The acoustic
// parts are mlx-audio-swift's S3Gen types (Chatterbox uses the same ones,
// with patch 0006); the prompts, history window and sampler are
// AudioSmithDialogue's.
//
// The model folder (scripts/soulx/convert.py): the 8-bit language model in
// mlx_lm's layout (config.json, model*.safetensors, tokenizer files),
// s3gen.safetensors (flow, HiFT, CAM++) and soulxpodcast_config.json. The
// S3 tokenizer is the auxiliary repo mlx-community/S3TokenizerV2.

/// A speaker's reference, ready for the model.
struct SoulXReference {
    /// Speech tokens (0..<6561), as many as the mel has frame pairs.
    let tokens: [Int]
    /// The flow's conditioning mel, (1, 2 × tokens, 80).
    let mel: MLXArray
    /// CAM++ x-vector, (1, 192).
    let xvector: MLXArray
    let seconds: Double
}

final class SoulXModel: @unchecked Sendable {
    static let sampleRate = 24000
    static let s3TokenizerRepo = "mlx-community/S3TokenizerV2"
    /// Longest reference: the S3 tokenizer reads 30 s at once.
    static let maxReferenceSeconds = 30.0

    let model: Qwen3Model
    let tokenizer: any MLXLMCommon.Tokenizer
    let flow: CausalMaskedDiffWithXvec
    let s3Tokenizer: S3TokenizerV2
    /// The output rows for the local vocabulary (speech tokens, then EOS).
    private let headWeight: MLXArray
    private let headScales: MLXArray?
    private let headBiases: MLXArray?
    private let groupSize: Int
    private let bits: Int
    private var references: [String: SoulXReference] = [:]

    /// A folder this loader reads.
    static func isModel(_ dir: URL) -> Bool {
        let fm = FileManager.default
        return fm.fileExists(atPath: dir.appendingPathComponent("s3gen.safetensors").path)
            && fm.fileExists(atPath: dir.appendingPathComponent("soulxpodcast_config.json").path)
    }

    init(model: Qwen3Model, tokenizer: any MLXLMCommon.Tokenizer, flow: CausalMaskedDiffWithXvec,
         s3Tokenizer: S3TokenizerV2, quantization: (groupSize: Int, bits: Int)?) throws {
        self.model = model
        self.tokenizer = tokenizer
        self.flow = flow
        self.s3Tokenizer = s3Tokenizer
        let params = Dictionary(uniqueKeysWithValues: model.model.parameters().flattened())
        guard let weight = params["embed_tokens.weight"] else {
            throw EngineError.loadFailed("SoulX-Podcast: no embed_tokens in the language model")
        }
        let rows = MLXArray((Array(SoulXVocab.speechOffset ..< SoulXVocab.speechOffset + SoulXVocab.speechCount) + [SoulXVocab.eos]).map { Int32($0) })
        headWeight = weight.take(rows, axis: 0)
        headScales = params["embed_tokens.scales"]?.take(rows, axis: 0)
        headBiases = params["embed_tokens.biases"]?.take(rows, axis: 0)
        groupSize = quantization?.groupSize ?? 64
        bits = quantization?.bits ?? 8
        if headScales != nil, quantization == nil {
            throw EngineError.loadFailed("SoulX-Podcast: quantized weights without a quantization config")
        }
        eval(headWeight)
        if let headScales { eval(headScales) }
        if let headBiases { eval(headBiases) }
    }

    /// llmDir loads the language model from another folder (parity checks:
    /// upstream's bfloat16 weights, with fp32 cast to float32).
    static func load(_ dir: URL, s3Tokenizer s3Dir: URL, llmDir: URL? = nil, fp32: Bool = false) async throws -> SoulXModel {
        let lmDir = llmDir ?? dir
        let context = try await LLMModelFactory.shared.load(from: lmDir, using: TransformersTokenizerLoader())
        guard let qwen = context.model as? Qwen3Model else {
            throw EngineError.loadFailed("SoulX-Podcast: the language model is not Qwen3")
        }
        if fp32 {
            var cast: [String: MLXArray] = [:]
            for (k, v) in qwen.parameters().flattened() { cast[k] = v.dtype == .bfloat16 || v.dtype == .float16 ? v.asType(.float32) : v }
            try qwen.update(parameters: ModuleParameters.unflattened(cast), verify: [])
            eval(qwen)
        }
        let flow = CausalMaskedDiffWithXvec(meanflow: false, hiftUseInterpolation: true)
        let weights = try MLX.loadArrays(url: dir.appendingPathComponent("s3gen.safetensors"))
        try flow.update(parameters: ModuleParameters.unflattened(weights), verify: [.noUnusedKeys, .shapeMismatch])
        flow.train(false)
        eval(flow)
        let s3 = try S3TokenizerV2.fromModelDirectory(s3Dir)
        s3.train(false)
        return try SoulXModel(model: qwen, tokenizer: context.tokenizer, flow: flow, s3Tokenizer: s3,
                              quantization: quantization(lmDir))
    }

    /// config.json's quantization (mlx_lm), nil when unquantized.
    static func quantization(_ dir: URL) -> (groupSize: Int, bits: Int)? {
        guard let data = try? Data(contentsOf: dir.appendingPathComponent("config.json")),
              let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let q = obj["quantization"] as? [String: Any],
              let g = q["group_size"] as? Int, let b = q["bits"] as? Int else { return nil }
        return (g, b)
    }

    // MARK: References

    /// The reference at path (cached by path, size and date).
    func reference(_ path: String) async throws -> SoulXReference {
        let attrs = try? FileManager.default.attributesOfItem(atPath: path)
        let key = "\(path)|\((attrs?[.size] as? NSNumber)?.int64Value ?? 0)|\((attrs?[.modificationDate] as? Date)?.timeIntervalSince1970 ?? 0)"
        if let r = references[key] { return r }
        let a16 = SoulXAudio.normalizeVolume(try await decodeMono(path, sampleRate: 16000))
        let seconds = Double(a16.count) / 16000
        guard seconds <= Self.maxReferenceSeconds else {
            throw EngineError.badRequest(String(format: "The reference %@ is %.0f s long; SoulX-Podcast takes at most %.0f s", path, seconds, Self.maxReferenceSeconds))
        }
        guard seconds >= 1 else { throw EngineError.badRequest("The reference \(path) is shorter than 1 s") }
        let a24 = SoulXAudio.normalizeVolume(try await decodeMono(path, sampleRate: Self.sampleRate))
        let r = prepare(audio16k: MLXArray(a16), audio24k: MLXArray(a24))
        if references.count >= 16 { references.removeAll() }
        references[key] = r
        return r
    }

    /// Tokens, mel and x-vector of a reference (normalized audio).
    func prepare(audio16k: MLXArray, audio24k: MLXArray) -> SoulXReference {
        let logMel = SoulXAudio.tokenizerMel(audio16k)
        let (codes, lens) = s3Tokenizer.quantize(logMel.expandedDimensions(axis: 0), melLen: MLXArray([Int32(logMel.dim(1))]))
        let n = lens.item(Int.self)
        var tokens = codes[0, 0 ..< n].asArray(Int32.self).map { Int($0) }
        let xvector = flow.speakerEncoder.inference([audio16k])
        var mel = SoulXAudio.flowMel(audio24k)
        // Two mel frames per token (upstream trims whichever is longer).
        if tokens.count * 2 > mel.dim(0) {
            tokens = Array(tokens.prefix(mel.dim(0) / 2))
        } else {
            mel = mel[0 ..< tokens.count * 2]
        }
        mel = mel.expandedDimensions(axis: 0)
        eval(mel, xvector)
        return SoulXReference(tokens: tokens, mel: mel, xvector: xvector, seconds: Double(audio24k.dim(0)) / Double(Self.sampleRate))
    }

    // MARK: Language model

    func encode(_ text: String) -> [Int] {
        tokenizer.encode(text: text, addSpecialTokens: false)
    }

    /// Reads ids into the cache; returns the last position's hidden state (1, hidden).
    func read(_ ids: [Int], cache: [KVCache]) -> MLXArray {
        var last: MLXArray?
        var i = 0
        while i < ids.count {
            let chunk = Array(ids[i ..< min(i + 512, ids.count)])
            let h = model.model(MLXArray(chunk.map { Int32($0) }).expandedDimensions(axis: 0), cache: cache)
            last = h[0..., (h.dim(1) - 1)...].reshaped(1, -1)
            eval(last!)
            i += chunk.count
        }
        return last!
    }

    /// Logits over the local vocabulary (speech tokens, then EOS).
    func logits(_ hidden: MLXArray) -> [Float] {
        let out: MLXArray
        if let headScales {
            out = quantizedMM(hidden, headWeight, scales: headScales, biases: headBiases, transpose: true,
                              groupSize: groupSize, bits: bits)
        } else {
            out = matmul(hidden, headWeight.transposed())
        }
        return out.asType(.float32).reshaped(-1).asArray(Float.self)
    }

    /// Writes one turn: reads the context's unread ids, then samples until
    /// EOS or maxTokens. Returns the turn's vocabulary ids (with the EOS when
    /// the model wrote one); cached counts the ids in the cache.
    func generate(_ context: SoulXContext, cache: [KVCache], cached: inout Int, sampler: inout SoulXSampler,
                  onToken: (Int) -> Void = { _ in }) throws -> [Int] {
        var hidden = read(Array(context.inputs[cached...]), cache: cache)
        cached = context.inputs.count
        var window = Array(context.inputs.suffix(sampler.config.rasWindow))
        var generated: [Int] = []
        while true {
            try Task.checkCancellation()
            let t = sampler.next(logits(hidden), window: window, generated: generated)
            generated.append(t)
            onToken(generated.count)
            window.append(SoulXVocab.global(t))
            if window.count > sampler.config.rasWindow { window.removeFirst() }
            if t == SoulXVocab.localEOS || generated.count >= sampler.config.maxTokens { break }
            hidden = read([SoulXVocab.global(t)], cache: cache)
            cached += 1
        }
        return generated.map(SoulXVocab.global)
    }

    // MARK: Flow and vocoder

    /// The waveform of speech tokens (0..<6561) in the reference's voice.
    func render(_ tokens: [Int], reference r: SoulXReference, steps: Int, noise: MLXArray? = nil) -> [Float] {
        guard !tokens.isEmpty else { return [] }
        let mel = flowMel(tokens, reference: r, steps: steps, noise: noise)
        let (wav, _) = flow.vocoder(mel)
        let out = wav.asType(.float32).reshaped(-1).asArray(Float.self)
        MLX.Memory.clearCache()
        return out
    }

    /// The generated part's mel (1, 80, 2 × tokens); fresh noise unless given.
    func flowMel(_ tokens: [Int], reference r: SoulXReference, steps: Int, noise: MLXArray? = nil) -> MLXArray {
        let total = (r.tokens.count + tokens.count) * 2
        let z = noise ?? MLXRandom.normal([1, 80, total])
        let mel = flow.inference(
            token: MLXArray(tokens.map { Int32($0) }).expandedDimensions(axis: 0),
            tokenLen: MLXArray([Int32(tokens.count)]),
            promptToken: MLXArray(r.tokens.map { Int32($0) }).expandedDimensions(axis: 0),
            promptTokenLen: MLXArray([Int32(r.tokens.count)]),
            promptFeat: r.mel, embedding: r.xvector, nTimesteps: steps, streaming: false, noise: z)
        eval(mel)
        return mel
    }
}
