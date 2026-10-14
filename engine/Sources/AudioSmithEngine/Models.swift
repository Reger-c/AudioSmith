import AudioSmithProtocol
import Foundation
@preconcurrency import MLX

/// The models a gpu worker keeps resident, of every task (TTS, STT,
/// alignment, VAD, enhancement). The least recently used beyond a cap are
/// dropped (AUDIOSMITH_MAX_MODELS, default 3); the app's supervisor also
/// unloads idle ones to stay within its memory budget. Jobs are serialized
/// by the Server (gpu concurrency 1): MLX models must not run from two jobs
/// at once.
final class ModelStore: @unchecked Sendable {
    struct Resident {
        var model: Any
        var task: String
        var sampleRate: Int
        var bytes: Int64
        var lastUsed: Date
        var inUse: Int
    }

    private let out: Writer
    private let lock = NSLock()
    private var resident: [String: Resident] = [:]
    private let maxResident: Int

    /// maxResident overrides AUDIOSMITH_MAX_MODELS (the listen worker keeps one).
    init(out: Writer, maxResident: Int? = nil) {
        self.out = out
        self.maxResident = maxResident ?? Int(ProcessInfo.processInfo.environment["AUDIOSMITH_MAX_MODELS"] ?? "") ?? 3
        // Keep MLX's buffer cache small: this process lives as long as the
        // app, and an uncapped cache grows to gigabytes (spike: OmniVoice 6–8 GB → 4.9 GB).
        MLX.Memory.cacheLimit = 256 * 1024 * 1024
    }

    func loaded() -> [LoadedModel] {
        lock.lock()
        defer { lock.unlock() }
        let now = Date()
        return resident.sorted { $0.value.lastUsed > $1.value.lastUsed }.map { id, r in
            LoadedModel(model: id, task: r.task, sampleRate: r.sampleRate, bytes: r.bytes,
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

    /// Returns model as T, loading it with load (stage "loading") when it
    /// is not resident. Call release(model) when the job is done with it.
    func acquire<T>(
        _ model: String, task: String, job: String, as type: T.Type,
        sampleRate: (T) -> Int, load: () async throws -> T
    ) async throws -> (T, Resident) {
        if let r = take(model) {
            guard let m = r.model as? T else {
                release(model)
                throw EngineError.badRequest("\(model) is loaded as a \(r.task) model, not for \(task)")
            }
            return (m, r)
        }
        out.notify(Method.jobProgress, ProgressParams(job: job, stage: "loading", message: "Loading \(shortName(model))"))
        let started = Date()
        let before = MLX.Memory.activeMemory
        let loaded: T
        do {
            loaded = try await load()
        } catch let error as EngineError {
            throw error
        } catch let error as RPCError {
            throw error
        } catch {
            if Task.isCancelled { throw CancellationError() }
            throw EngineError.loadFailed("\(shortName(model)): \(error.localizedDescription)")
        }
        try Task.checkCancellation()
        let r = Resident(model: loaded, task: task, sampleRate: sampleRate(loaded),
                         bytes: Int64(max(MLX.Memory.activeMemory - before, 0)), lastUsed: Date(), inUse: 1)
        insert(model, r)
        out.logLine("info", String(format: "Loaded %@ in %.1f s", model, Date().timeIntervalSince(started)), job: job)
        return (loaded, r)
    }

    private func take(_ model: String) -> Resident? {
        lock.withLock {
            guard var r = resident[model] else { return nil }
            r.inUse += 1
            r.lastUsed = Date()
            resident[model] = r
            return r
        }
    }

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

    func release(_ model: String) {
        lock.lock()
        if var r = resident[model] {
            r.inUse = max(r.inUse - 1, 0)
            r.lastUsed = Date()
            resident[model] = r
        }
        lock.unlock()
    }
}

func shortName(_ model: String) -> String {
    model.split(separator: "/").last.map(String.init) ?? model
}

/// What is missing from the HF cache for model, or nil when a complete
/// copy is there (the layouts mlx-audio-swift resolves offline).
func missingFromCache(_ model: String) -> [String]? {
    let fm = FileManager.default
    let hub = hubCacheDirectory()
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

/// The Hugging Face hub cache (HF_HUB_CACHE, set from hello.cacheDir).
func hubCacheDirectory() -> URL {
    let env = ProcessInfo.processInfo.environment
    return env["HF_HUB_CACHE"].map { URL(fileURLWithPath: $0) }
        ?? env["HF_HOME"].map { URL(fileURLWithPath: $0).appendingPathComponent("hub") }
        ?? FileManager.default.homeDirectoryForCurrentUser.appendingPathComponent(".cache/huggingface/hub")
}

/// Resolves the local directory of a model job: its modelPath when set,
/// else the snapshot of model in the HF cache; offline and missing fails
/// with model_not_downloaded.
func modelDirectory(_ model: String, path: String?, offline: Bool) throws -> URL? {
    if let path, !path.isEmpty { return URL(fileURLWithPath: path) }
    if offline, let missing = missingFromCache(model) {
        throw EngineError.notDownloaded(model: model, missing: missing)
    }
    let snapshots = hubCacheDirectory().appendingPathComponent("models--" + model.replacingOccurrences(of: "/", with: "--"))
        .appendingPathComponent("snapshots")
    let fm = FileManager.default
    if let shas = try? fm.contentsOfDirectory(atPath: snapshots.path),
       let sha = shas.first(where: { !((try? fm.contentsOfDirectory(atPath: snapshots.appendingPathComponent($0).path)) ?? []).isEmpty }) {
        return snapshots.appendingPathComponent(sha)
    }
    return nil
}
