import Foundation
@preconcurrency import MLX
import MLXFast
import MLXNN

// MOSS-Audio-Tokenizer (OpenMOSS, Apache-2.0; thread #82): MOSS-TTSD's
// codec, 24 kHz ↔ 12.5 frames a second of 16 codebooks (residual LFQ).
// Encoder and decoder are stacks of causal transformers (RoPE, a 10-second
// attention window) between patch reshapes. Our own port of the official
// modeling_moss_audio_tokenizer.py @ 3cd226b (mlx-audio-swift's keeps its
// layers private and loads no quantized weights):
//
//   - encode runs the whole clip at once, each query seeing the window's
//     keys (computed in blocks of queries, so a long clip needs no
//     clip-length² attention);
//   - decode streams 8-second chunks as the official processor does
//     (decode_audio_codes, chunk_duration=8): each layer keeps the last
//     window of keys (a ring of `context` positions), so a query sees the
//     keys from the chunk's end minus the window up to itself.
//
// The weights (scripts/mossttsd/convert.py): weight norm folded, 1×1
// convolutions as matrices, the first 16 quantizers, Linear layers whose
// input is a multiple of 64 in 8 bits with float32 scales; the rest float32.

final class MossLayerScale: Module {
    var scale: MLXArray

    init(_ n: Int) {
        scale = MLXArray.ones([n])
    }
}

final class MossCodecAttention: Module {
    let heads: Int
    @ModuleInfo(key: "in_proj") var inProj: Linear
    @ModuleInfo(key: "out_proj") var outProj: Linear

    init(dim: Int, heads: Int) {
        self.heads = heads
        _inProj.wrappedValue = Linear(dim, 3 * dim, bias: false)
        _outProj.wrappedValue = Linear(dim, dim, bias: false)
    }
}

final class MossCodecLayer: Module {
    @ModuleInfo(key: "self_attn") var attn: MossCodecAttention
    @ModuleInfo var norm1: LayerNorm
    @ModuleInfo var norm2: LayerNorm
    @ModuleInfo var linear1: Linear
    @ModuleInfo var linear2: Linear
    @ModuleInfo(key: "layer_scale_1") var scale1: MossLayerScale
    @ModuleInfo(key: "layer_scale_2") var scale2: MossLayerScale

    init(dim: Int, heads: Int, ff: Int) {
        _attn.wrappedValue = MossCodecAttention(dim: dim, heads: heads)
        _norm1.wrappedValue = LayerNorm(dimensions: dim, eps: 1e-5)
        _norm2.wrappedValue = LayerNorm(dimensions: dim, eps: 1e-5)
        _linear1.wrappedValue = Linear(dim, ff, bias: false)
        _linear2.wrappedValue = Linear(ff, dim, bias: false)
        _scale1.wrappedValue = MossLayerScale(dim)
        _scale2.wrappedValue = MossLayerScale(dim)
    }
}

final class MossCodecTransformer: Module {
    @ModuleInfo var layers: [MossCodecLayer]

    init(dim: Int, heads: Int, ff: Int, count: Int) {
        _layers.wrappedValue = (0 ..< count).map { _ in MossCodecLayer(dim: dim, heads: heads, ff: ff) }
    }
}

/// A stage of the encoder or decoder: a patch reshape (no weights) or a
/// projected transformer.
final class MossCodecStage: Module {
    let patch: Int // > 0: a patch stage
    let context: Int
    let maxPeriod: Float
    let heads: Int
    @ModuleInfo(key: "input_proj") var inputProj: Linear?
    @ModuleInfo var transformer: MossCodecTransformer?
    @ModuleInfo(key: "output_proj") var outputProj: Linear?

    init(patch: Int) {
        self.patch = patch
        context = 0
        maxPeriod = 0
        heads = 0
    }

    init(_ kw: [String: Any], context: Int) {
        patch = 0
        self.context = context
        let input = kw["input_dimension"] as? Int ?? 0, output = kw["output_dimension"] as? Int ?? 0
        let dim = kw["d_model"] as? Int ?? 0
        heads = kw["num_heads"] as? Int ?? 1
        maxPeriod = Float(kw["max_period"] as? Double ?? Double(kw["max_period"] as? Int ?? 10000))
        _inputProj.wrappedValue = input == dim ? nil : Linear(input, dim, bias: false)
        _transformer.wrappedValue = MossCodecTransformer(dim: dim, heads: heads, ff: kw["dim_feedforward"] as? Int ?? 4 * dim,
                                                          count: kw["num_layers"] as? Int ?? 1)
        _outputProj.wrappedValue = output == dim ? nil : Linear(dim, output, bias: false)
    }
}

final class MossLFQ: Module {
    @ModuleInfo(key: "in_proj") var inProj: Linear
    @ModuleInfo(key: "out_proj") var outProj: Linear
    @ModuleInfo var codebook: Embedding

    init(dim: Int, codebookSize: Int, codebookDim: Int) {
        _inProj.wrappedValue = Linear(dim, codebookDim, bias: true)
        _outProj.wrappedValue = Linear(codebookDim, dim, bias: true)
        _codebook.wrappedValue = Embedding(embeddingCount: codebookSize, dimensions: codebookDim)
    }
}

final class MossRLFQ: Module {
    @ModuleInfo(key: "input_proj") var inputProj: Linear
    @ModuleInfo(key: "output_proj") var outputProj: Linear
    @ModuleInfo var quantizers: [MossLFQ]

    init(input: Int, rvq: Int, output: Int, count: Int, codebookSize: Int, codebookDim: Int) {
        _inputProj.wrappedValue = Linear(input, rvq, bias: true)
        _outputProj.wrappedValue = Linear(rvq, output, bias: true)
        _quantizers.wrappedValue = (0 ..< count).map { _ in MossLFQ(dim: rvq, codebookSize: codebookSize, codebookDim: codebookDim) }
    }
}

/// One layer's streaming keys and values (the last `context` positions).
struct MossKV {
    var keys: MLXArray?
    var values: MLXArray?
    var offset = 0
}

final class MossCodec: Module, @unchecked Sendable {
    static let sampleRate = 24000
    static let frameSamples = 1920
    /// Decode chunk: 8 s (the official processor's chunk_duration).
    static let decodeChunkFrames = 100
    /// Query block of the windowed encoder attention.
    static let encodeBlock = 1024

    @ModuleInfo var encoder: [MossCodecStage]
    @ModuleInfo var quantizer: MossRLFQ
    @ModuleInfo var decoder: [MossCodecStage]
    let numQuantizers: Int

    /// codec_config.json.
    init(config: [String: Any]) throws {
        let rate = Double(config["sampling_rate"] as? Int ?? Self.sampleRate)
        let window = Double(config["causal_transformer_context_duration"] as? Double ?? Double(config["causal_transformer_context_duration"] as? Int ?? 10))
        guard let enc = config["encoder_kwargs"] as? [[String: Any]], let dec = config["decoder_kwargs"] as? [[String: Any]],
              let q = config["quantizer_kwargs"] as? [String: Any] else {
            throw EngineError.loadFailed("MOSS codec: codec_config.json lacks the encoder, decoder or quantizer")
        }
        var frameRate = rate
        var e: [MossCodecStage] = []
        for kw in enc {
            if kw["module_type"] as? String == "PatchedPretransform" {
                let p = kw["patch_size"] as? Int ?? 1
                e.append(MossCodecStage(patch: p))
                frameRate /= Double(p)
            } else {
                e.append(MossCodecStage(kw, context: Int(frameRate * window)))
            }
        }
        var d: [MossCodecStage] = []
        for kw in dec {
            if kw["module_type"] as? String == "PatchedPretransform" {
                let p = kw["patch_size"] as? Int ?? 1
                d.append(MossCodecStage(patch: p))
                frameRate *= Double(p)
            } else {
                d.append(MossCodecStage(kw, context: Int(frameRate * window)))
            }
        }
        numQuantizers = q["num_quantizers"] as? Int ?? 16
        _encoder.wrappedValue = e
        _decoder.wrappedValue = d
        _quantizer.wrappedValue = MossRLFQ(input: q["input_dim"] as? Int ?? 768, rvq: q["rvq_dim"] as? Int ?? 512,
                                           output: q["output_dim"] as? Int ?? 768, count: numQuantizers,
                                           codebookSize: q["codebook_size"] as? Int ?? 1024, codebookDim: q["codebook_dim"] as? Int ?? 8)
    }

    /// Loads codec_config.json and codec.safetensors from dir.
    static func load(_ dir: URL) throws -> MossCodec {
        let data = try Data(contentsOf: dir.appendingPathComponent("codec_config.json"))
        guard let config = try JSONSerialization.jsonObject(with: data) as? [String: Any] else {
            throw EngineError.loadFailed("MOSS codec: codec_config.json is not an object")
        }
        let codec = try MossCodec(config: config)
        let weights = try MLX.loadArrays(url: dir.appendingPathComponent("codec.safetensors"))
        if let q = config["quantization"] as? [String: Any], let g = q["group_size"] as? Int, let b = q["bits"] as? Int {
            // Stage by stage: the stage arrays mix patch stages (no weights)
            // with transformers, which quantize(model:) cannot update in one go.
            for (name, stages) in [("encoder", codec.encoder), ("decoder", codec.decoder)] {
                for (i, s) in stages.enumerated() where s.patch == 0 {
                    quantize(model: s, filter: { path, m in
                        m is Linear && weights["\(name).\(i).\(path).scales"] != nil ? (g, b, .affine) : nil
                    })
                }
            }
        }
        try codec.update(parameters: ModuleParameters.unflattened(weights), verify: .all)
        eval(codec)
        return codec
    }

    // MARK: Layers

    /// One transformer layer on x (1, T, D) at absolute positions offset…;
    /// keys from `minKey` (absolute) on, each query back to its window.
    private func layer(_ l: MossCodecLayer, _ x: MLXArray, stage s: MossCodecStage, kv: inout MossKV, streaming: Bool) -> MLXArray {
        let T = x.dim(1), D = x.dim(2), H = s.heads, Dh = D / H
        let h = l.norm1(x)
        let qkv = l.attn.inProj(h).reshaped(1, T, 3, H, Dh)
        var q = qkv[0..., 0..., 0].transposed(0, 2, 1, 3)
        var k = qkv[0..., 0..., 1].transposed(0, 2, 1, 3)
        let v = qkv[0..., 0..., 2].transposed(0, 2, 1, 3)
        let offset = kv.offset
        q = MLXFast.RoPE(q, dimensions: Dh, traditional: true, base: s.maxPeriod, scale: 1, offset: offset)
        k = MLXFast.RoPE(k, dimensions: Dh, traditional: true, base: s.maxPeriod, scale: 1, offset: offset)
        let scale = 1 / Float(Dh).squareRoot()
        var out: MLXArray
        if streaming {
            // The ring: the last context − T earlier keys, then the chunk's.
            let keep = max(s.context - T, 0)
            var keys = k, values = v, prev = 0
            if let pk = kv.keys, let pv = kv.values, keep > 0 {
                let n = min(pk.dim(2), keep)
                keys = concatenated([pk[0..., 0..., (pk.dim(2) - n)...], k], axis: 2)
                values = concatenated([pv[0..., 0..., (pv.dim(2) - n)...], v], axis: 2)
                prev = n
            }
            let qi = MLXArray(0 ..< T).reshaped(T, 1)
            let ki = MLXArray(-prev ..< T).reshaped(1, prev + T)
            let mask = ki .<= qi
            out = MLXFast.scaledDotProductAttention(queries: q, keys: keys, values: values, scale: scale, mask: .array(mask))
            let n = min(keys.dim(2), s.context)
            kv.keys = keys[0..., 0..., (keys.dim(2) - n)...]
            kv.values = values[0..., 0..., (values.dim(2) - n)...]
        } else {
            // The whole clip: query p sees keys p − context + 1 … p.
            var parts: [MLXArray] = []
            var start = 0
            while start < T {
                let end = min(start + Self.encodeBlock, T)
                let k0 = max(start - s.context + 1, 0)
                let qi = MLXArray(start ..< end).reshaped(end - start, 1)
                let ki = MLXArray(k0 ..< end).reshaped(1, end - k0)
                let delta = qi - ki
                let mask = logicalAnd(delta .>= MLXArray(Int32(0)), delta .< MLXArray(Int32(s.context)))
                parts.append(MLXFast.scaledDotProductAttention(queries: q[0..., 0..., start ..< end], keys: k[0..., 0..., k0 ..< end],
                                                               values: v[0..., 0..., k0 ..< end], scale: scale, mask: .array(mask)))
                start = end
            }
            out = parts.count == 1 ? parts[0] : concatenated(parts, axis: 2)
        }
        kv.offset += T
        let a = l.attn.outProj(out.transposed(0, 2, 1, 3).reshaped(1, T, D))
        var y = x + l.scale1.scale * a
        y = y + l.scale2.scale * l.linear2(gelu(l.linear1(l.norm2(y))))
        return y
    }

    /// A stage on x (1, T, D).
    private func stage(_ s: MossCodecStage, _ x: MLXArray, down: Bool, kv: inout [MossKV], streaming: Bool) -> MLXArray {
        if s.patch > 0 {
            let T = x.dim(1), D = x.dim(2), p = s.patch
            if down { // (T, D) → (T/p, D·p), channel c·p + j
                return x.reshaped(1, T / p, p, D).transposed(0, 1, 3, 2).reshaped(1, T / p, D * p)
            }
            // (T, D·p) → (T·p, D)
            return x.reshaped(1, T, D / p, p).transposed(0, 1, 3, 2).reshaped(1, T * p, D / p)
        }
        var h = s.inputProj.map { $0(x) } ?? x
        let layers = s.transformer!.layers
        if kv.isEmpty { kv = Array(repeating: MossKV(), count: layers.count) }
        for (i, l) in layers.enumerated() {
            h = layer(l, h, stage: s, kv: &kv[i], streaming: streaming)
        }
        return s.outputProj.map { $0(h) } ?? h
    }

    // MARK: Encode and decode

    /// The 16 codebooks of a 24 kHz clip (already loudness-normalized):
    /// frames × 16, as the official batch_encode (the clip padded to whole
    /// frames, the last partial frame dropped).
    func encode(_ samples: [Float], quantizers: Int = 16) -> [[Int]] {
        let frames = samples.count / Self.frameSamples
        guard frames > 0 else { return [] }
        return codes(encoderHidden(samples), frames: frames, quantizers: quantizers)
    }

    /// The encoder's output (1, frames, 768) of a clip padded to whole frames.
    func encoderHidden(_ samples: [Float]) -> MLXArray {
        let padded = (samples.count + Self.frameSamples - 1) / Self.frameSamples * Self.frameSamples
        var x = MLXArray(samples + Array(repeating: 0, count: padded - samples.count)).reshaped(1, padded, 1)
        for s in encoder {
            var kv: [MossKV] = []
            x = stage(s, x, down: true, kv: &kv, streaming: false)
        }
        return x.asType(.float32)
    }

    /// Residual LFQ: the first `frames` frames' codes of the encoder's output.
    func codes(_ hidden: MLXArray, frames: Int, quantizers: Int) -> [[Int]] {
        var residual = quantizer.inputProj(hidden)
        var codes: [MLXArray] = []
        for lfq in quantizer.quantizers.prefix(quantizers) {
            let e = lfq.inProj(residual)
            let en = e / maximum(sqrt(sum(e * e, axis: -1, keepDims: true)), MLXArray(Float(1e-12)))
            let cb = lfq.codebook.weight.asType(.float32)
            let cn = cb / maximum(sqrt(sum(cb * cb, axis: -1, keepDims: true)), MLXArray(Float(1e-12)))
            let dist = sum(en * en, axis: -1, keepDims: true) - 2 * matmul(en, cn.transposed()) + sum(cn * cn, axis: -1).reshaped(1, 1, -1)
            let idx = argMin(dist, axis: -1)
            codes.append(idx)
            residual = residual - lfq.outProj(lfq.codebook(idx).asType(.float32))
        }
        let all = stacked(codes, axis: -1)[0, 0 ..< frames] // (frames, nq)
        eval(all)
        let flat = all.asType(.int32).asArray(Int32.self)
        let nq = codes.count
        return (0 ..< frames).map { t in (0 ..< nq).map { Int(flat[t * nq + $0]) } }
    }

    /// The waveform of frames × 16 codes, decoded in 8-second chunks with
    /// the decoder's streaming state, as the official processor does.
    func decode(_ codes: [[Int]], cancelled: () -> Bool = { false }) throws -> [Float] {
        guard !codes.isEmpty else { return [] }
        let nq = codes[0].count
        var states = Array(repeating: [MossKV](), count: decoder.count)
        var out: [Float] = []
        out.reserveCapacity(codes.count * Self.frameSamples)
        var start = 0
        // The official decode streams only clips longer than one chunk.
        let streaming = codes.count > Self.decodeChunkFrames
        while start < codes.count {
            if cancelled() { throw CancellationError() }
            let end = streaming ? min(start + Self.decodeChunkFrames, codes.count) : codes.count
            let chunk = MLXArray(codes[start ..< end].flatMap { $0.map { Int32($0) } }).reshaped(1, end - start, nq)
            var emb = MLXArray.zeros([1, end - start, quantizer.inputProj.weight.dim(0)], dtype: .float32)
            for (i, lfq) in quantizer.quantizers.prefix(nq).enumerated() {
                emb = emb + lfq.outProj(lfq.codebook(chunk[0..., 0..., i]).asType(.float32))
            }
            var x = quantizer.outputProj(emb)
            for (i, s) in decoder.enumerated() {
                x = stage(s, x, down: false, kv: &states[i], streaming: streaming)
            }
            eval(x)
            out += x.reshaped(-1).asType(.float32).asArray(Float.self)
            for i in states.indices {
                for j in states[i].indices {
                    if let k = states[i][j].keys, let v = states[i][j].values { eval(k, v) }
                }
            }
            start = end
        }
        return out
    }

    /// The official processor's loudness_normalize: to −20 dBFS (mean
    /// square), the gain within ±3 dB.
    static func loudnessNormalize(_ x: [Float]) -> [Float] {
        guard !x.isEmpty else { return x }
        var sum = 0.0
        for v in x { sum += Double(v) * Double(v) }
        let db = 10 * log10(sum / Double(x.count) + 1e-9)
        let gain = min(max(-20 - db, -3), 3)
        let f = Float(pow(10, gain / 20))
        return x.map { $0 * f }
    }
}
