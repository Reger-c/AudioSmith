import Foundation
@preconcurrency import MLX

// AudioSeal (Meta, facebookresearch/audioseal, MIT; weights facebook/
// audioseal, MIT): a SEANet encoder–decoder that makes a watermark for
// 16 kHz speech from a 16-bit message, and a SEANet detector that scores
// every sample and decodes the message. A port of the reference PyTorch
// modules (audioseal/models.py, libs/audiocraft/modules/{seanet,conv,
// lstm}.py; card audioseal_wm_16bits: ratios 8 5 4 2, 32 filters,
// dimension 128, ELU, one residual layer, compress 2, 2 LSTM layers,
// zero padding, non-causal, no normaliser) to MLX, inference only.
//
// Weights: the app converts generator_base.pth / detector_base.pth to
// generator.safetensors / detector.safetensors with weight norm folded
// (internal/torchpt); names are the reference's.

final class AudioSeal: @unchecked Sendable {
    static let sampleRate = 16000
    static let bits = 16
    /// Samples per encoder frame (8·5·4·2).
    static let hop = 320

    private let w: [String: MLXArray]
    /// The detector's head folded into one matrix: its transposed
    /// convolution (stride = kernel = 320, so each encoder frame makes 320
    /// samples on its own) followed by the 1×1 convolution to 2 + 16
    /// channels, as [128, 320·18] and a bias of 18.
    private let head: MLXArray
    private let headBias: MLXArray

    init(directory: URL) throws {
        var all: [String: MLXArray] = [:]
        for name in ["generator", "detector"] {
            let url = directory.appendingPathComponent("\(name).safetensors")
            guard FileManager.default.fileExists(atPath: url.path) else {
                throw EngineError.loadFailed("AudioSeal: \(name).safetensors is missing in \(directory.path)")
            }
            for (k, v) in try loadArrays(url: url) {
                all[k] = Self.layout(k, v)
            }
        }
        w = all
        // reverse_convolution, MLX layout (out 32, k 320, in 128) → (in, k, out).
        let rc = all["detector.0.reverse_convolution.weight"]!.transposed(2, 1, 0)
        let d1 = all["detector.1.weight"]![0..., 0, 0...].T // (32, 18)
        let k = rc.dim(1)
        head = matmul(rc, d1).reshaped([rc.dim(0), k * d1.dim(1)])
        headBias = matmul(all["detector.0.reverse_convolution.bias"]!, d1) + all["detector.1.bias"]!
        eval(Array(w.values) + [head, headBias])
    }

    /// PyTorch conv weights → MLX: Conv1d (out, in, k) → (out, k, in);
    /// ConvTranspose1d (in, out, k) → (out, k, in).
    private static func layout(_ key: String, _ v: MLXArray) -> MLXArray {
        if key.hasSuffix("convtr.weight") || key == "detector.0.reverse_convolution.weight" {
            return v.transposed(1, 2, 0)
        }
        if v.ndim == 3 && key.hasSuffix(".weight") {
            return v.transposed(0, 2, 1)
        }
        return v
    }

    private func p(_ key: String) -> MLXArray {
        guard let a = w[key] else { fatalError("AudioSeal: no weight \(key)") }
        return a
    }

    // MARK: Layers (activations are [1, T, C])

    private func elu(_ x: MLXArray) -> MLXArray {
        which(x .> 0, x, exp(minimum(x, 0)) - 1)
    }

    /// StreamableConv1d, non-causal, zero padding, with the extra right
    /// padding that keeps the last window full.
    private func conv(_ x: MLXArray, _ prefix: String, stride: Int = 1, dilation: Int = 1) -> MLXArray {
        let weight = p(prefix + ".weight")
        let k = (weight.dim(1) - 1) * dilation + 1
        let total = k - stride
        let length = x.dim(1)
        let frames = Double(length - k + total) / Double(stride) + 1
        let ideal = (Int(frames.rounded(.up)) - 1) * stride + (k - total)
        let extra = ideal - length
        let right = total / 2
        let left = total - right
        let padded = MLX.padded(x, widths: [0, IntOrPair((left, right + extra)), 0])
        return conv1d(padded, weight, stride: stride, padding: 0, dilation: dilation) + p(prefix + ".bias")
    }

    /// StreamableConvTranspose1d, non-causal: the fixed padding trimmed.
    private func convTr(_ x: MLXArray, _ prefix: String, stride: Int) -> MLXArray {
        let weight = p(prefix + ".weight")
        let y = convTransposed1d(x, weight, stride: stride) + p(prefix + ".bias")
        let total = weight.dim(1) - stride
        let right = total / 2
        let left = total - right
        return y[0..., left ..< (y.dim(1) - right), 0...]
    }

    private func resBlock(_ x: MLXArray, _ prefix: String) -> MLXArray {
        var h = conv(elu(x), prefix + ".block.1.conv.conv")
        h = conv(elu(h), prefix + ".block.3.conv.conv")
        return x + h
    }

    /// StreamableLSTM: two layers, then the skip connection.
    private func lstm(_ x: MLXArray, _ prefix: String) -> MLXArray {
        var seq = x
        let steps = x.dim(1)
        for layer in 0 ..< 2 {
            let wih = p("\(prefix).weight_ih_l\(layer)")
            let whhT = p("\(prefix).weight_hh_l\(layer)").T
            let pre = matmul(seq, wih.T) + p("\(prefix).bias_ih_l\(layer)") + p("\(prefix).bias_hh_l\(layer)")
            let hidden = whhT.dim(0)
            var h = MLXArray.zeros([x.dim(0), hidden])
            var c = MLXArray.zeros([x.dim(0), hidden])
            var outs: [MLXArray] = []
            outs.reserveCapacity(steps)
            for t in 0 ..< steps {
                let g = pre[0..., t, 0...] + matmul(h, whhT)
                let parts = split(g, parts: 4, axis: -1)
                c = sigmoid(parts[1]) * c + sigmoid(parts[0]) * tanh(parts[2])
                h = sigmoid(parts[3]) * tanh(c)
                outs.append(h)
                if t % 64 == 63 { eval(h, c) }
            }
            seq = stacked(outs, axis: 1)
        }
        return seq + x
    }

    private func encoder(_ x: MLXArray, _ prefix: String) -> MLXArray {
        var h = conv(x, prefix + ".0.conv.conv")
        for (i, ratio) in [(1, 2), (4, 4), (7, 5), (10, 8)] {
            h = resBlock(h, "\(prefix).\(i)")
            h = conv(elu(h), "\(prefix).\(i + 2).conv.conv", stride: ratio)
        }
        h = lstm(h, prefix + ".13.lstm")
        return conv(elu(h), prefix + ".15.conv.conv")
    }

    private func decoder(_ z: MLXArray) -> MLXArray {
        var h = conv(z, "decoder.model.0.conv.conv")
        h = lstm(h, "decoder.model.1.lstm")
        for (i, ratio) in [(3, 8), (6, 5), (9, 4), (12, 2)] {
            h = convTr(elu(h), "decoder.model.\(i).convtr.convtr", stride: ratio)
            h = resBlock(h, "decoder.model.\(i + 1)")
        }
        return conv(elu(h), "decoder.model.15.conv.conv")
    }

    /// The message's bits, most significant first (the model's bit k is
    /// message bit 15 − k).
    static func bits(of message: Int) -> [Int] {
        (0 ..< bits).map { (message >> (bits - 1 - $0)) & 1 }
    }

    /// Windows of equal length as one batch [B, L, 1].
    private func batch(_ xs: [[Float]]) -> MLXArray {
        let length = xs[0].count
        return MLXArray(xs.flatMap { $0 }).reshaped([xs.count, length, 1])
    }

    /// The watermarks of windows of 16 kHz mono audio, all of the same
    /// length, for message (one batch: the LSTMs step through the
    /// windows together).
    func watermark(_ xs: [[Float]], message: Int) -> [[Float]] {
        let length = xs[0].count
        var hidden = encoder(batch(xs), "encoder.model")
        let indices = MLXArray(Self.bits(of: message).enumerated().map { Int32(2 * $0.offset + $0.element) })
        let embedding = p("msg_processor.msg_processor.weight")[indices].sum(axis: 0)
        hidden = hidden + embedding
        let y = decoder(hidden)[0..., 0 ..< length, 0]
        eval(y)
        let flat = y.asArray(Float.self)
        return (0 ..< xs.count).map { Array(flat[$0 * length ..< ($0 + 1) * length]) }
    }

    /// The detector on windows of 16 kHz mono audio of equal length (the
    /// first valid[i] samples of window i are audio, the rest padding):
    /// per window, the probability of each sample being watermarked and the
    /// message logits summed over its valid samples.
    func detect(_ xs: [[Float]], valid: [Int]) -> [(probabilities: [Float], logitSums: [Float])] {
        let length = xs[0].count
        let enc = encoder(batch(xs), "detector.0.model") // [B, F, 128]
        let (b, frames) = (enc.dim(0), enc.dim(1))
        let out = headBias.dim(0)
        var z = matmul(enc, head).reshaped([b, frames * Self.hop, out]) + headBias // [B, F·320, 2 + 16]
        z = z[0..., 0 ..< length, 0...]
        let prob = softmax(z[0..., 0..., 0 ..< 2], axis: -1)[0..., 0..., 1]
        let positions = MLXArray(Int32(0) ..< Int32(length)).reshaped([1, length, 1])
        let mask = (positions .< MLXArray(valid.map(Int32.init)).reshaped([b, 1, 1])).asType(.float32)
        let sums = (z[0..., 0..., 2...] * mask).sum(axis: 1)
        eval(prob, sums)
        let pf = prob.asArray(Float.self), sf = sums.asArray(Float.self)
        return (0 ..< b).map { (Array(pf[$0 * length ..< ($0 + 1) * length]), Array(sf[$0 * Self.bits ..< ($0 + 1) * Self.bits])) }
    }
}
