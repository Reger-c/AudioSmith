import Foundation
@preconcurrency import MLX
import MLXAudioCore

// SoulX-Podcast's audio front end (thread #80): the reference's loudness,
// the 16 kHz log-mel the S3 tokenizer reads and the 24 kHz mel the flow is
// conditioned on, as upstream computes them (soulxpodcast/utils/audio.py and
// the s3tokenizer package). mlx-audio-swift's s3genMelSpectrogram pads the
// signal twice (its stft centres again) and uses a symmetric window, and its
// S3 tokenizer log-mel keeps the last frame, which whisper's and
// s3tokenizer's drop: these are written here instead.

enum SoulXAudio {
    /// Upstream's audio_volume_normalize: scale so the 90th–99th percentile
    /// of the samples over 0.01 sits at coeff (gain within 0.1–10), never
    /// over full scale. Quiet clips (peak < 0.1) are first raised to 0.1;
    /// the percentiles are still the original clip's, as upstream does.
    static func normalizeVolume(_ input: [Float], coeff: Float = 0.1) -> [Float] {
        var audio = input
        let sorted = audio.map { abs($0) }.sorted()
        guard let peak = sorted.last else { return audio }
        if peak < 0.1 {
            let scale = 0.1 / max(peak, 1e-3)
            audio = audio.map { $0 * scale }
        }
        let loud = sorted.filter { $0 > 0.01 }
        let n = loud.count
        guard n > 10 else { return audio }
        let lo = Int(0.9 * Double(n)), hi = Int(0.99 * Double(n))
        guard hi > lo else { return audio }
        let volume = loud[lo ..< hi].reduce(0, +) / Float(hi - lo)
        let gain = min(max(coeff / volume, 0.1), 10)
        audio = audio.map { $0 * gain }
        let top = audio.map { abs($0) }.max() ?? 0
        if top > 1 { audio = audio.map { $0 / top } }
        return audio
    }

    /// Reflect-pads a 1-D signal by pad on both sides (torch "reflect").
    static func reflectPad(_ x: MLXArray, _ pad: Int) -> MLXArray {
        let n = x.dim(0)
        guard pad > 0, n > pad else { return x }
        let head = x[1 ... pad].take(MLXArray(Array((0 ..< pad).reversed()).map { Int32($0) }))
        let tail = x[(n - pad - 1) ..< (n - 1)].take(MLXArray(Array((0 ..< pad).reversed()).map { Int32($0) }))
        return MLX.concatenated([head, x, tail])
    }

    /// |STFT| frames (frames, nFft/2+1) of an already padded signal, with a
    /// periodic Hann window of nFft (torch.hann_window), no centring.
    static func spectrum(_ padded: MLXArray, nFft: Int, hop: Int) -> MLXArray {
        let n = padded.dim(0)
        let frames = 1 + (n - nFft) / hop
        let windows = MLX.asStrided(padded, [frames, nFft], strides: [hop, 1])
        let window = hanningWindow(size: nFft, periodic: true)
        return MLXFFT.rfft(windows * window, axis: -1)
    }

    /// The S3 tokenizer's input: whisper's 128-band log-mel of 16 kHz audio,
    /// (128, frames), the last STFT frame dropped as whisper does.
    static func tokenizerMel(_ audio16k: MLXArray) -> MLXArray {
        let spec = spectrum(reflectPad(audio16k, 200), nFft: 400, hop: 160)
        let power = MLX.abs(spec).square()[0 ..< (spec.dim(0) - 1)]
        let filters = melFilters(sampleRate: 16000, nFft: 400, nMels: 128, norm: "slaney", melScale: .slaney)
        var logSpec = MLX.log10(MLX.maximum(matmul(power, filters), MLXArray(Float(1e-10))))
        logSpec = MLX.maximum(logSpec, logSpec.max() - 8.0)
        return ((logSpec + 4.0) / 4.0).transposed()
    }

    /// The flow's conditioning: the 80-band log-mel of 24 kHz audio
    /// (frames, 80), n_fft 1920, hop 480, reflect-padded by 720, magnitude
    /// sqrt(|X|² + 1e-9), slaney bands up to 8 kHz; an odd frame count loses
    /// its last frame (upstream's dataloader).
    static func flowMel(_ audio24k: MLXArray) -> MLXArray {
        let spec = spectrum(reflectPad(audio24k, 720), nFft: 1920, hop: 480)
        let magnitude = MLX.sqrt(MLX.abs(spec).square() + 1e-9)
        let filters = melFilters(sampleRate: 24000, nFft: 1920, nMels: 80, fMin: 0, fMax: 8000, norm: "slaney", melScale: .slaney)
        var mel = MLX.log(MLX.maximum(matmul(magnitude, filters), MLXArray(Float(1e-5))))
        if mel.dim(0) % 2 != 0 { mel = mel[0 ..< (mel.dim(0) - 1)] }
        return mel
    }
}
