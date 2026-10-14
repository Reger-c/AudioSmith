import AudioSmithProtocol
import AVFoundation
import Foundation
@preconcurrency import MLX

// watermark.embed / watermark.detect (docs/protocol-v2.md §5.5) on the
// media worker: AudioSeal at 16 kHz mono; the watermark is computed on a
// 16 kHz mix of the input, resampled to the input's rate and added to each
// channel. Long inputs go through in 10 s windows (1 s crossfaded
// overlaps), up to 8 at a time as one batch. MLX runs on the GPU unless AUDIOSMITH_WATERMARK_DEVICE=cpu;
// one watermark computation at a time.

private let sealLock = NSLock()
nonisolated(unsafe) private var sealModel: (path: String, model: AudioSeal)?

/// The AudioSeal model in directory, loaded once (stage "loading").
private func audioSeal(_ directory: URL, out: Writer, job: String) throws -> AudioSeal {
    if let m = sealModel, m.path == directory.path { return m.model }
    out.notify(Method.jobProgress, ProgressParams(job: job, stage: "loading", message: "Loading AudioSeal"))
    let m = try AudioSeal(directory: directory)
    sealModel = (directory.path, m)
    return m
}

private var sealDevice: MLX.Device {
    ProcessInfo.processInfo.environment["AUDIOSMITH_WATERMARK_DEVICE"] == "cpu" ? .cpu : .gpu
}

/// Batches of windows: the windows of a signal (10 s, 1 s overlaps), each
/// padded with zeros to the window's length, in groups of at most 8 that
/// the model runs as one batch.
private func batches(_ x: [Float], size: Int, overlap: Int) -> [[(start: Int, end: Int, samples: [Float])]] {
    let wins = windows(x.count, size: size, overlap: overlap)
    let length = min(size, x.count)
    let all = wins.map { (s, e) -> (start: Int, end: Int, samples: [Float]) in
        var w = Array(x[s ..< e])
        if w.count < length { w.append(contentsOf: [Float](repeating: 0, count: length - w.count)) }
        return (s, e, w)
    }
    return stride(from: 0, to: all.count, by: 8).map { Array(all[$0 ..< min($0 + 8, all.count)]) }
}

/// Windows of a long signal: (start, end) with overlap samples shared.
private func windows(_ count: Int, size: Int, overlap: Int) -> [(Int, Int)] {
    guard count > size else { return [(0, count)] }
    var out: [(Int, Int)] = []
    var start = 0
    while start < count {
        let end = min(start + size, count)
        out.append((start, end))
        if end == count { break }
        start = end - overlap
    }
    return out
}

/// Resamples mono samples with AVAudioConverter (best quality).
func resampleMono(_ x: [Float], from: Int, to: Int) throws -> [Float] {
    if from == to || x.isEmpty { return x }
    guard let fin = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(from), channels: 1, interleaved: false),
          let fout = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(to), channels: 1, interleaved: false),
          let conv = AVAudioConverter(from: fin, to: fout),
          let inBuf = AVAudioPCMBuffer(pcmFormat: fin, frameCapacity: AVAudioFrameCount(x.count)) else {
        throw RPCError(Code.internal, "Cannot resample \(from) Hz to \(to) Hz")
    }
    conv.sampleRateConverterQuality = AVAudioQuality.max.rawValue
    inBuf.frameLength = AVAudioFrameCount(x.count)
    x.withUnsafeBufferPointer { inBuf.floatChannelData![0].update(from: $0.baseAddress!, count: x.count) }
    let expected = Int((Double(x.count) * Double(to) / Double(from)).rounded())
    var result: [Float] = []
    result.reserveCapacity(expected + 1024)
    var fed = false
    while true {
        guard let outBuf = AVAudioPCMBuffer(pcmFormat: fout, frameCapacity: 65536) else { break }
        var error: NSError?
        let status = conv.convert(to: outBuf, error: &error) { _, inputStatus in
            if fed {
                inputStatus.pointee = .endOfStream
                return nil
            }
            fed = true
            inputStatus.pointee = .haveData
            return inBuf
        }
        if let error { throw RPCError(Code.internal, "Resampling failed: \(error.localizedDescription)") }
        let n = Int(outBuf.frameLength)
        if n > 0 { result.append(contentsOf: UnsafeBufferPointer(start: outBuf.floatChannelData![0], count: n)) }
        if status == .endOfStream || status == .error || (n == 0 && fed) { break }
    }
    if result.count > expected { result.removeLast(result.count - expected) }
    if result.count < expected { result.append(contentsOf: [Float](repeating: 0, count: expected - result.count)) }
    return result
}

extension MediaWorker {
    static let watermarkFamilies = ["audioseal"]

    private func sealDirectory(_ model: String, _ path: String?) throws -> URL {
        guard let path, !path.isEmpty else {
            throw EngineError.badRequest("watermark jobs need modelPath: the converted AudioSeal folder")
        }
        return URL(fileURLWithPath: path)
    }

    func watermarkEmbed(_ p: WatermarkEmbedParams, job: String) async throws -> WatermarkEmbedResult {
        guard (0 ... 0xFFFF).contains(p.message) else {
            throw EngineError.badRequest("message must be 0–65535 (16 bits), not \(p.message)")
        }
        let alpha = Float(p.strength ?? 1) == 0 ? 1 : Float(p.strength ?? 1)
        let (url, bits) = try outputURL(p.output)
        let dir = try sealDirectory(p.model, p.modelPath)
        let media = try await openMedia(p.input)
        var native: [Float] = []
        let (rate, channels) = try readAudio(media, sampleRate: nil, channels: nil) { native.append(contentsOf: $0) }
        var x16: [Float] = []
        _ = try readAudio(media, sampleRate: AudioSeal.sampleRate, channels: 1) { x16.append(contentsOf: $0) }
        guard !native.isEmpty, !x16.isEmpty else { throw EngineError.unsupportedMedia(media.url.path, "it has no audio samples") }
        let frames = native.count / channels
        let progress = JobProgress(out: out, job: job)
        progress.audioSeconds = Double(frames) / Double(rate)
        let started = Date()

        // The watermark at 16 kHz, window by window (in batches).
        let size = 10 * AudioSeal.sampleRate, overlap = AudioSeal.sampleRate
        var wm16 = [Float](repeating: 0, count: x16.count)
        sealLock.lock()
        defer { sealLock.unlock() }
        let model = try audioSeal(dir, out: out, job: job)
        try using(device: sealDevice) {
            var first = true
            for group in batches(x16, size: size, overlap: overlap) {
                try Task.checkCancellation()
                let parts = model.watermark(group.map(\.samples), message: p.message)
                for (w, part) in zip(group, parts) {
                    for j in 0 ..< (w.end - w.start) {
                        let k = w.start + j
                        // Crossfade the overlap with the previous window.
                        if !first, j < overlap {
                            let t = Float(j) / Float(overlap)
                            wm16[k] = wm16[k] * (1 - t) + part[j] * t
                        } else {
                            wm16[k] = part[j]
                        }
                    }
                    first = false
                    progress.fraction = Double(w.end) / Double(x16.count)
                }
                progress.send("watermarking")
            }
        }
        MLX.Memory.clearCache()
        var wm = try resampleMono(wm16, from: AudioSeal.sampleRate, to: rate)
        if wm.count < frames { wm.append(contentsOf: [Float](repeating: 0, count: frames - wm.count)) }

        // Added to every channel; the SNR is the mix's power over the watermark's.
        var signal = 0.0, noise = 0.0
        var outSamples = native
        for f in 0 ..< frames {
            let d = alpha * wm[f]
            noise += Double(d * d)
            for c in 0 ..< channels {
                let i = f * channels + c
                signal += Double(native[i] * native[i]) / Double(channels)
                outSamples[i] = max(-1, min(1, native[i] + d))
            }
        }
        progress.send("writing", force: true)
        let writer = try WAVWriter(url: url, sampleRate: rate, channels: channels, bits: bits)
        do {
            try writer.append(outSamples[...])
            try writer.finish()
        } catch {
            writer.cancel()
            throw error
        }
        let snr = noise > 0 ? 10 * log10(max(signal, 1e-12) / noise) : 99
        return WatermarkEmbedResult(output: url.path, sampleRate: rate, channels: channels, frames: Int64(frames),
                                    audioSeconds: Double(frames) / Double(rate), model: p.model, message: p.message,
                                    snr: (snr * 10).rounded() / 10, seconds: Date().timeIntervalSince(started))
    }

    func watermarkDetect(_ p: WatermarkDetectParams, job: String) async throws -> WatermarkDetectResult {
        let dir = try sealDirectory(p.model, p.modelPath)
        let x = try await decodeMono(p.input, sampleRate: AudioSeal.sampleRate)
        let threshold = Float(p.threshold ?? 0.5) == 0 ? 0.5 : Float(p.threshold ?? 0.5)
        let progress = JobProgress(out: out, job: job)
        progress.audioSeconds = Double(x.count) / Double(AudioSeal.sampleRate)
        let started = Date()
        let size = 10 * AudioSeal.sampleRate, overlap = AudioSeal.sampleRate
        var prob = [Float](repeating: 0, count: x.count)
        var sums = [Double](repeating: 0, count: AudioSeal.bits)
        var counted = 0
        sealLock.lock()
        defer { sealLock.unlock() }
        let model = try audioSeal(dir, out: out, job: job)
        try using(device: sealDevice) {
            var first = true
            for group in batches(x, size: size, overlap: overlap) {
                try Task.checkCancellation()
                let results = model.detect(group.map(\.samples), valid: group.map { $0.end - $0.start })
                for (w, r) in zip(group, results) {
                    let n = w.end - w.start
                    // Each window owns its samples after the overlap's middle.
                    let from = first ? 0 : overlap / 2
                    for j in from ..< n { prob[w.start + j] = r.probabilities[j] }
                    for b in 0 ..< AudioSeal.bits { sums[b] += Double(r.logitSums[b]) }
                    counted += n
                    first = false
                    progress.fraction = Double(w.end) / Double(x.count)
                }
                progress.send("detecting")
            }
        }
        MLX.Memory.clearCache()
        let detected = prob.reduce(0) { $0 + ($1 > threshold ? 1 : 0) }
        let probability = x.isEmpty ? 0 : Double(detected) / Double(x.count)
        let bits = sums.map { 1 / (1 + exp(-$0 / Double(max(counted, 1)))) }
        var message = 0
        for b in bits { message = message << 1 | (b > 0.5 ? 1 : 0) }
        let confidence = bits.map { abs(2 * $0 - 1) }.reduce(0, +) / Double(bits.count)
        var frames: [WatermarkFrame] = []
        let second = AudioSeal.sampleRate
        var s = 0
        while s < x.count {
            let e = min(s + second, x.count)
            let n = prob[s ..< e].reduce(0) { $0 + ($1 > threshold ? 1 : 0) }
            frames.append(WatermarkFrame(start: Double(s) / Double(second), end: Double(e) / Double(second),
                                         probability: (Double(n) / Double(e - s) * 1000).rounded() / 1000))
            s = e
        }
        func r3(_ v: Double) -> Double { (v * 1000).rounded() / 1000 }
        return WatermarkDetectResult(probability: r3(probability), detected: probability >= 0.5, message: message,
                                     bits: bits.map(r3), confidence: r3(confidence), frames: frames,
                                     audioSeconds: Double(x.count) / Double(AudioSeal.sampleRate), model: p.model,
                                     seconds: Date().timeIntervalSince(started))
    }
}
