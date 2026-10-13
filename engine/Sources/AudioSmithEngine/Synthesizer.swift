import Foundation
@preconcurrency import MLX
import MLXAudioCore
import MLXAudioTTS
import MLXLMCommon

enum EngineError: Error, LocalizedError {
    case missing(String)
    case badAudioShape([Int])
    case emptyAudio

    var errorDescription: String? {
        switch self {
        case .missing(let field): "Missing \(field)"
        case .badAudioShape(let shape): "Unexpected audio shape \(shape)"
        case .emptyAudio: "The model produced no audio"
        }
    }
}

struct SynthesisResult {
    var output: URL
    var sampleRate: Int
    var audioSeconds: Double
    var seconds: Double
}

/// Owns the loaded model. Calls are serialized by JobRunner: MLX models
/// must not be used from two generations at once.
final class Synthesizer: @unchecked Sendable {
    private(set) var model: SpeechGenerationModel?
    private(set) var modelID: String?
    private let events: EventWriter

    init(events: EventWriter) {
        self.events = events
        // Keep MLX's buffer cache small: this process lives as long as the app.
        Memory.cacheLimit = 256 * 1024 * 1024
    }

    /// Loads modelID unless it is already loaded; returns the model.
    @discardableResult
    func load(_ modelID: String, requestID: String?) async throws -> SpeechGenerationModel {
        if let model, self.modelID == modelID {
            return model
        }
        unload()
        events.status("Loading \(modelID)… (first use downloads it from Hugging Face)", id: requestID)
        let started = Date()
        let hfToken = ProcessInfo.processInfo.environment["HF_TOKEN"]
        let loaded = try await TTS.loadModel(modelRepo: modelID, hfToken: hfToken)
        try Task.checkCancellation()
        model = loaded
        self.modelID = modelID

        var e = Event("loaded", id: requestID)
        e.model = modelID
        e.sampleRate = loaded.sampleRate
        e.seconds = Date().timeIntervalSince(started)
        events.send(e)
        return loaded
    }

    func unload() {
        model = nil
        modelID = nil
        Memory.clearCache()
    }

    func synthesize(_ req: Request) async throws -> SynthesisResult {
        guard let modelID = req.model, !modelID.isEmpty else { throw EngineError.missing("model") }
        guard let text = req.text?.trimmingCharacters(in: .whitespacesAndNewlines), !text.isEmpty else {
            throw EngineError.missing("text")
        }
        guard let outputPath = req.output, !outputPath.isEmpty else { throw EngineError.missing("output") }

        let model = try await load(modelID, requestID: req.id)

        if let kokoro = model as? KokoroModel {
            kokoro.speed = Float(req.speed ?? 1.0)
        }

        var params = model.defaultGenerationParameters
        if let maxTokens = req.maxTokens, maxTokens > 0 { params.maxTokens = maxTokens }
        if let temperature = req.temperature { params.temperature = Float(temperature) }
        if let topP = req.topP { params.topP = Float(topP) }

        var refAudio: MLXArray?
        if let refPath = nonEmpty(req.refAudio) {
            (_, refAudio) = try loadAudioArray(from: URL(fileURLWithPath: refPath), sampleRate: model.sampleRate)
        }

        events.status("Generating…", id: req.id)
        let started = Date()
        let sampleRate = model.sampleRate
        let stream = model.generateStream(
            text: text,
            voice: nonEmpty(req.voice),
            refAudio: refAudio,
            refText: nonEmpty(req.refText),
            language: nonEmpty(req.language),
            generationParameters: params,
            streamingInterval: 1.0
        )

        var samples: [Float] = []
        var lastReport = Date.distantPast
        for try await event in stream {
            try Task.checkCancellation()
            switch event {
            case .audio(let chunk):
                samples.append(contentsOf: try monoSamples(chunk))
                var e = Event("progress", id: req.id)
                e.audioSeconds = Double(samples.count) / Double(sampleRate)
                events.send(e)
            case .progress(let fraction):
                var e = Event("progress", id: req.id)
                e.fraction = fraction
                events.send(e)
            case .token:
                // Token models emit many tokens per second; report a heartbeat now and then.
                if Date().timeIntervalSince(lastReport) > 0.5 {
                    lastReport = Date()
                    events.send(Event("progress", id: req.id))
                }
            case .info(let info):
                log(info.summary)
            }
        }
        try Task.checkCancellation()
        guard !samples.isEmpty else { throw EngineError.emptyAudio }

        let outputURL = URL(fileURLWithPath: outputPath)
        try FileManager.default.createDirectory(
            at: outputURL.deletingLastPathComponent(), withIntermediateDirectories: true
        )
        try AudioUtils.writeWavFile(samples: samples, sampleRate: sampleRate, fileURL: outputURL)
        Memory.clearCache()

        return SynthesisResult(
            output: outputURL,
            sampleRate: sampleRate,
            audioSeconds: Double(samples.count) / Double(sampleRate),
            seconds: Date().timeIntervalSince(started)
        )
    }
}

private func nonEmpty(_ s: String?) -> String? {
    guard let s = s?.trimmingCharacters(in: .whitespacesAndNewlines), !s.isEmpty else { return nil }
    return s
}

/// Flattens a generated chunk to mono samples. Models return [N], [1, N],
/// [1, 1, N], [C, N] or [N, C] depending on their vocoder.
func monoSamples(_ audio: MLXArray) throws -> [Float] {
    var a = audio.asType(.float32)
    while a.ndim > 2, a.dim(0) == 1 {
        a = a[0]
    }
    switch a.ndim {
    case 0:
        return []
    case 1:
        return a.asArray(Float.self)
    case 2:
        let (rows, cols) = (a.dim(0), a.dim(1))
        if rows == 1 { return a.reshaped([cols]).asArray(Float.self) }
        if cols == 1 { return a.reshaped([rows]).asArray(Float.self) }
        // Channels are the short axis.
        let mono = rows <= 8 ? a.mean(axis: 0) : a.mean(axis: 1)
        return mono.asArray(Float.self)
    default:
        throw EngineError.badAudioShape(a.shape)
    }
}
