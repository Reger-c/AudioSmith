import AudioSmithProtocol
import Foundation
@preconcurrency import MLX
import MLXAudioCore
import MLXAudioTTS
import MLXLMCommon

/// Keeps TTS models resident (least recently used beyond a cap are
/// dropped; the app's supervisor also unloads idle ones to stay within its
/// memory budget) and runs synthesis jobs. Jobs are serialized by the
/// Server (concurrency 1): MLX models must not generate from two jobs at once.
final class Synthesizer: @unchecked Sendable {
    /// Model families this build loads (catalog.Entry.Family names).
    static let families = [
        "kokoro", "kitten", "qwen3_tts", "qwen3", "marvis", "soprano", "omnivoice", "chatterbox", "pocket_tts",
        "llama_tts", "fish_speech", "echo_tts", "irodori_tts", "moss_tts", "moss_tts_nano", "indextts", "spark", "breeze",
    ]

    struct Resident {
        var model: SpeechGenerationModel
        var sampleRate: Int
        var bytes: Int64
        var lastUsed: Date
        var inUse: Int
    }

    private let out: Writer
    private let lock = NSLock()
    private var resident: [String: Resident] = [:]
    /// Most models kept at once (AUDIOSMITH_MAX_MODELS, default 3).
    private let maxResident: Int

    init(out: Writer) {
        self.out = out
        maxResident = Int(ProcessInfo.processInfo.environment["AUDIOSMITH_MAX_MODELS"] ?? "") ?? 3
        // Keep MLX's buffer cache small: this process lives as long as the
        // app, and an uncapped cache grows to gigabytes (spike: OmniVoice 6–8 GB → 4.9 GB).
        MLX.Memory.cacheLimit = 256 * 1024 * 1024
    }

    // MARK: Residency

    func loaded() -> [LoadedModel] {
        lock.lock()
        defer { lock.unlock() }
        let now = Date()
        return resident.sorted { $0.value.lastUsed > $1.value.lastUsed }.map { id, r in
            LoadedModel(model: id, task: "tts", sampleRate: r.sampleRate, bytes: r.bytes,
                        idleSeconds: r.inUse > 0 ? 0 : max(now.timeIntervalSince(r.lastUsed), 0.001))
        }
    }

    /// Drops model (all when nil) and returns what was dropped. A model a
    /// job is using stays alive until the job ends.
    func unload(_ model: String?) -> [String] {
        lock.lock()
        let ids = resident.keys.filter { model == nil || model == "" || $0 == model }.sorted()
        for id in ids { resident[id] = nil }
        lock.unlock()
        if !ids.isEmpty { MLX.Memory.clearCache() }
        return ids
    }

    /// Returns model, loading it (stage "loading") when it is not resident.
    private func acquire(_ model: String, path: String?, job: String, offline: Bool) async throws -> (SpeechGenerationModel, Resident) {
        if let r = take(model) {
            return (r.model, r)
        }

        if offline && (path ?? "").isEmpty, let missing = missingFromCache(model) {
            throw EngineError.notDownloaded(model: model, missing: missing)
        }
        let short = model.split(separator: "/").last.map(String.init) ?? model
        out.notify(Method.jobProgress, ProgressParams(
            job: job, stage: "loading",
            message: offline ? "Loading \(short)" : "Loading \(short) (the first use downloads it from Hugging Face)"))
        let started = Date()
        let before = MLX.Memory.activeMemory
        let loaded: SpeechGenerationModel
        do {
            let source = (path?.isEmpty ?? true) ? model : path!
            loaded = try await TTS.loadModel(modelRepo: source, hfToken: ProcessInfo.processInfo.environment["HF_TOKEN"])
        } catch {
            if Task.isCancelled { throw CancellationError() }
            throw EngineError.loadFailed(error.localizedDescription)
        }
        try Task.checkCancellation()
        let r = Resident(model: loaded, sampleRate: loaded.sampleRate,
                         bytes: Int64(max(MLX.Memory.activeMemory - before, 0)), lastUsed: Date(), inUse: 1)
        insert(model, r)
        out.logLine("info", String(format: "Loaded %@ in %.1f s", model, Date().timeIntervalSince(started)), job: job)
        return (loaded, r)
    }

    /// Marks a resident model in use and returns it.
    private func take(_ model: String) -> Resident? {
        lock.withLock {
            guard var r = resident[model] else { return nil }
            r.inUse += 1
            r.lastUsed = Date()
            resident[model] = r
            return r
        }
    }

    /// Adds a loaded model; over the cap, drops the least recently used idle ones.
    private func insert(_ model: String, _ r: Resident) {
        lock.withLock {
            resident[model] = r
            while resident.count > maxResident,
                  let lru = resident.filter({ $0.value.inUse == 0 }).min(by: { $0.value.lastUsed < $1.value.lastUsed })?.key {
                resident[lru] = nil
                log("dropped \(lru): more than \(maxResident) models resident")
            }
        }
    }

    private func release(_ model: String) {
        lock.lock()
        if var r = resident[model] {
            r.inUse = max(r.inUse - 1, 0)
            r.lastUsed = Date()
            resident[model] = r
        }
        lock.unlock()
    }

    /// What is missing from the HF cache for model, or nil when a complete
    /// copy is there (the layouts mlx-audio-swift resolves offline).
    private func missingFromCache(_ model: String) -> [String]? {
        let env = ProcessInfo.processInfo.environment
        let hub = env["HF_HUB_CACHE"].map { URL(fileURLWithPath: $0) }
            ?? env["HF_HOME"].map { URL(fileURLWithPath: $0).appendingPathComponent("hub") }
            ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".cache/huggingface/hub")
        let fm = FileManager.default
        let flat = hub.appendingPathComponent("mlx-audio").appendingPathComponent(model.replacingOccurrences(of: "/", with: "_"))
        if fm.fileExists(atPath: flat.appendingPathComponent("config.json").path) {
            return nil
        }
        let snapshots = hub.appendingPathComponent("models--" + model.replacingOccurrences(of: "/", with: "--"))
            .appendingPathComponent("snapshots")
        if let shas = try? fm.contentsOfDirectory(atPath: snapshots.path),
           shas.contains(where: { !((try? fm.contentsOfDirectory(atPath: snapshots.appendingPathComponent($0).path)) ?? []).isEmpty }) {
            return nil
        }
        return ["config.json"]
    }

    // MARK: Jobs

    func load(_ p: LoadParams, job: String, offline: Bool) async throws -> LoadResult {
        let started = Date()
        let (model, r) = try await acquire(p.model, path: p.path, job: job, offline: offline)
        defer { release(p.model) }
        var caps = ["stream"]
        if model is KokoroModel { caps.append("speed") }
        return LoadResult(model: p.model, task: p.task ?? "tts", sampleRate: model.sampleRate,
                          seconds: Date().timeIntervalSince(started), bytes: r.bytes > 0 ? r.bytes : nil, capabilities: caps)
    }

    func synthesize(_ p: SynthesizeParams, job: String, offline: Bool) async throws -> SynthesizeResult {
        guard !p.model.isEmpty else { throw EngineError.badRequest("Missing model") }
        let text = p.text.trimmingCharacters(in: .whitespacesAndNewlines)
        guard !text.isEmpty else { throw EngineError.badRequest("Missing text") }
        guard p.output.path.hasPrefix("/") else { throw EngineError.badRequest("output.path must be an absolute path") }
        guard (p.output.format ?? "wav") == "wav" else { throw EngineError.badRequest("Unsupported output format \(p.output.format!)") }
        let bits = p.output.bits ?? 16
        guard [16, 24, 32].contains(bits) else { throw EngineError.badRequest("Unsupported bits \(bits)") }

        let (model, _) = try await acquire(p.model, path: nil, job: job, offline: offline)
        defer { release(p.model) }

        let refPath = nonEmpty(p.refAudio)
        if let kokoro = model as? KokoroModel {
            if refPath != nil {
                throw EngineError.unsupported("\(p.model.split(separator: "/").last ?? "") cannot clone voices", capability: "clone")
            }
            kokoro.speed = Float(p.speed ?? 1.0)
        }
        var params = model.defaultGenerationParameters
        if let maxTokens = p.maxTokens, maxTokens > 0 { params.maxTokens = maxTokens }
        if let temperature = p.temperature { params.temperature = Float(temperature) }
        if let topP = p.topP { params.topP = Float(topP) }
        // Seeded every time, so a take can be reproduced from its result.
        let seed = p.seed ?? Int64.random(in: 0 ... Int64.max)
        MLXRandom.seed(UInt64(bitPattern: seed))

        var refAudio: MLXArray?
        if let refPath {
            do {
                (_, refAudio) = try loadAudioArray(from: URL(fileURLWithPath: refPath), sampleRate: model.sampleRate)
            } catch {
                throw EngineError.badRequest("Could not read the reference audio \(refPath): \(error.localizedDescription)")
            }
        }

        let sampleRate = model.sampleRate
        let progress = JobProgress(out: out, job: job)
        progress.send("generating", force: true)
        let started = Date()
        let stream = model.generateStream(
            text: text, voice: nonEmpty(p.voice), refAudio: refAudio, refText: nonEmpty(p.refText),
            language: nonEmpty(p.language), generationParameters: params, streamingInterval: 1.0)

        var samples: [Float] = []
        var seq = 0
        for try await event in stream {
            try Task.checkCancellation()
            switch event {
            case .audio(let chunk):
                let mono = try monoSamples(chunk)
                if p.stream == true {
                    // Up to one second per tts.chunk.
                    var i = 0
                    while i < mono.count {
                        let end = min(i + sampleRate, mono.count)
                        out.notify(Method.ttsChunk, ChunkParams(job: job, seq: seq, sampleRate: sampleRate, channels: 1,
                                                                encoding: "pcm16le", data: pcm16(mono[i ..< end]), final: nil))
                        seq += 1
                        i = end
                    }
                }
                samples.append(contentsOf: mono)
                progress.audioSeconds = Double(samples.count) / Double(sampleRate)
                progress.send("generating")
            case .progress(let fraction):
                progress.fraction = min(max(fraction, 0), 1)
                progress.send("generating")
            case .token:
                progress.send("generating")
            case .info(let info):
                log(info.summary)
            }
        }
        try Task.checkCancellation()
        guard !samples.isEmpty else { throw RPCError(Code.internal, "The model produced no audio") }
        if p.stream == true {
            out.notify(Method.ttsChunk, ChunkParams(job: job, seq: seq, sampleRate: sampleRate, channels: 1,
                                                    encoding: "pcm16le", data: Data(), final: true))
        }
        let seconds = Date().timeIntervalSince(started)
        progress.fraction = 1
        progress.send("writing", force: true)
        do {
            try writeWAV(samples, sampleRate: sampleRate, bits: bits, to: URL(fileURLWithPath: p.output.path))
        } catch {
            throw RPCError(Code.internal, "Could not write \(p.output.path): \(error.localizedDescription)")
        }
        MLX.Memory.clearCache()

        let audioSeconds = Double(samples.count) / Double(sampleRate)
        return SynthesizeResult(output: p.output.path, model: p.model, sampleRate: sampleRate, channels: 1,
                                frames: Int64(samples.count), audioSeconds: audioSeconds, seconds: seconds,
                                rtf: seconds > 0 ? audioSeconds / seconds : 0, seed: seed)
    }
}

/// Sends a job's job.progress, at most 10 per second unless forced.
final class JobProgress {
    let out: Writer
    let job: String
    var fraction: Double?
    var audioSeconds: Double?
    private var last = Date.distantPast
    private var lastStage = ""

    init(out: Writer, job: String) {
        self.out = out
        self.job = job
    }

    func send(_ stage: String, force: Bool = false) {
        let now = Date()
        guard force || stage != lastStage || now.timeIntervalSince(last) >= 0.1 else { return }
        last = now
        lastStage = stage
        out.notify(Method.jobProgress, ProgressParams(job: job, stage: stage, fraction: fraction, audioSeconds: audioSeconds))
    }
}

func nonEmpty(_ s: String?) -> String? {
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
        throw RPCError(Code.internal, "Unexpected audio shape \(a.shape)")
    }
}
