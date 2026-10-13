import AudioSmithProtocol
import Foundation
@preconcurrency import MLX

/// Writes protocol lines to the original stdout, which it takes over at
/// startup, one whole line at a time.
final class Writer: @unchecked Sendable {
    private let handle: FileHandle
    private let lock = NSLock()
    private let encoder = makeEncoder()

    /// Duplicates stdout for the protocol, then points stdout at stderr so
    /// stray prints of the engine's libraries cannot corrupt the stream.
    init() {
        let fd = dup(STDOUT_FILENO)
        dup2(STDERR_FILENO, STDOUT_FILENO)
        handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }

    func send<P: Encodable>(_ message: Outgoing<P>) {
        guard var data = try? encoder.encode(message) else {
            log("could not encode a \(message.method ?? "response") message")
            return
        }
        data.append(0x0A)
        lock.lock()
        defer { lock.unlock() }
        try? handle.write(contentsOf: data)
    }

    func result<P: Encodable>(_ id: String, _ value: P) { send(Outgoing.response(id, value)) }
    func fail(_ id: String, _ error: RPCError) { send(Outgoing<Empty>.failure(id, error)) }
    func notify<P: Encodable>(_ method: String, _ params: P) { send(Outgoing.notification(method, params)) }

    func logLine(_ level: String, _ message: String, job: String? = nil) {
        notify(Method.log, LogParams(level: level, message: message, job: job))
    }
}

/// Free-form diagnostics on stderr (the app keeps them in engine-<role>.log).
func log(_ message: String) {
    FileHandle.standardError.write(Data("[engine] \(message)\n".utf8))
}

/// Errors with a protocol code.
enum EngineError: Error, LocalizedError {
    case badRequest(String)
    case unsupported(String, capability: String)
    case notDownloaded(model: String, missing: [String])
    case loadFailed(String)

    var errorDescription: String? {
        switch self {
        case .badRequest(let m), .loadFailed(let m): m
        case .unsupported(let m, _): m
        case .notDownloaded(let model, _): "\(model) is not downloaded"
        }
    }

    var rpc: RPCError {
        switch self {
        case .badRequest(let m): RPCError(Code.badRequest, m)
        case .unsupported(let m, let cap): RPCError(Code.unsupported, m, data: .object(["capability": .string(cap)]))
        case .notDownloaded(let model, let missing):
            RPCError(Code.modelNotDownloaded, errorDescription!,
                     data: .object(["model": .string(model), "missing": .array(missing.map { .string($0) })]))
        case .loadFailed(let m): RPCError(Code.modelLoadFailed, m)
        }
    }
}

func rpcError(_ error: Error) -> RPCError {
    if let e = error as? RPCError { return e }
    if let e = error as? EngineError { return e.rpc }
    if error is CancellationError { return RPCError(Code.cancelled, "Cancelled") }
    return RPCError(Code.internal, error.localizedDescription)
}

/// The session: answers requests from stdin, runs jobs by id.
final class Server: @unchecked Sendable {
    let role: Role
    let out: Writer
    let synth: Synthesizer?
    private let lock = NSLock()
    private var hello: HelloParams?
    private var jobs: [String: Task<Void, Never>] = [:]
    private var cancelled: Set<String> = []

    /// Jobs it runs at once; one more gets "busy". The gpu worker runs one:
    /// MLX models must not generate from two jobs at once.
    let concurrency = 1

    init(role: Role, out: Writer) {
        self.role = role
        self.out = out
        synth = role == .gpu ? Synthesizer(out: out) : nil
    }

    var methods: [String] {
        var m = [Method.hello, Method.stats, Method.flush, Method.shutdown]
        if role == .gpu {
            m += [Method.modelsLoad, Method.modelsUnload, Method.modelsLoaded, Method.ttsSynthesize]
        }
        return m + [Method.jobCancel]
    }

    /// Reads requests until stdin closes, then cancels the jobs and exits.
    func run() async {
        do {
            for try await line in FileHandle.standardInput.bytes.lines {
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                if trimmed.isEmpty { continue }
                let msg: Incoming
                do {
                    msg = try Incoming.decode(Data(trimmed.utf8))
                } catch {
                    log("bad line: \(error.localizedDescription)")
                    continue
                }
                guard msg.kind == .request, let id = msg.id, let method = msg.method else {
                    log("ignored a message that is not a request")
                    continue
                }
                handle(id: id, method: method, msg)
            }
        } catch {
            log("stdin failed: \(error)")
        }
        // The app closed our stdin: it quit or crashed. Don't outlive it.
        cancelAll()
        exit(0)
    }

    func handle(id: String, method: String, _ msg: Incoming) {
        if method == Method.hello {
            return answerHello(id, msg)
        }
        lock.lock()
        let ready = hello != nil
        lock.unlock()
        guard ready else {
            return out.fail(id, RPCError(Code.notInitialized, "engine.hello must come first"))
        }
        guard methods.contains(method) else {
            return out.fail(id, RPCError(Code.methodNotFound, "Unknown method \(method)"))
        }
        do {
            switch method {
            case Method.stats:
                out.result(id, stats())
            case Method.flush:
                MLX.Memory.clearCache()
                out.result(id, Empty())
            case Method.shutdown:
                cancelAll()
                out.result(id, Empty())
                exit(0)
            case Method.jobCancel:
                let p = try msg.decodeParams(CancelParams.self)
                out.result(id, CancelResult(cancelled: cancel(p.job)))
            case Method.modelsLoaded:
                out.result(id, LoadedResult(models: synth?.loaded() ?? []))
            case Method.modelsUnload:
                let p = try msg.decodeParams(UnloadParams.self)
                out.result(id, UnloadResult(unloaded: synth?.unload(p.model) ?? []))
            case Method.modelsLoad:
                let p = try msg.decodeParams(LoadParams.self)
                guard !p.model.isEmpty else { throw EngineError.badRequest("Missing model") }
                guard let synth else { throw RPCError(Code.methodNotFound, "Unknown method \(method)") }
                let offline = self.offline
                startJob(id) { try await synth.load(p, job: id, offline: offline) }
            case Method.ttsSynthesize:
                let p = try msg.decodeParams(SynthesizeParams.self)
                guard let synth else { throw RPCError(Code.methodNotFound, "Unknown method \(method)") }
                let offline = self.offline
                startJob(id) { try await synth.synthesize(p, job: id, offline: offline) }
            default:
                out.fail(id, RPCError(Code.methodNotFound, "Unknown method \(method)"))
            }
        } catch let error as DecodingError {
            out.fail(id, RPCError(Code.badRequest, "Bad params for \(method): \(describe(error))"))
        } catch {
            out.fail(id, rpcError(error))
        }
    }

    private var offline: Bool {
        lock.lock()
        defer { lock.unlock() }
        return hello?.offline ?? false
    }

    private func answerHello(_ id: String, _ msg: Incoming) {
        let p: HelloParams
        do {
            p = try msg.decodeParams(HelloParams.self)
        } catch {
            // A peer of another protocol may not even match the shape.
            return out.fail(id, RPCError(Code.protocolMismatch, "This engine speaks protocol \(protocolVersion)",
                                         data: .object(["supported": .array([.int(Int64(protocolVersion))])])))
        }
        guard p.protocol == protocolVersion else {
            return out.fail(id, RPCError(Code.protocolMismatch, "This engine speaks protocol \(protocolVersion)",
                                         data: .object(["supported": .array([.int(Int64(protocolVersion))])])))
        }
        if let dir = p.cacheDir, !dir.isEmpty {
            // swift-huggingface resolves its cache from the environment on first use.
            setenv("HF_HUB_CACHE", dir, 1)
        }
        lock.lock()
        hello = p
        lock.unlock()
        if p.role != role {
            log("hello asks for role \(p.role.rawValue); this process runs as \(role.rawValue)")
        }
        out.result(id, HelloResult(
            protocol: protocolVersion, version: engineVersion, role: role, pid: Int(getpid()),
            concurrency: concurrency, methods: methods, families: role == .gpu ? Synthesizer.families : [],
            device: deviceInfo()))
    }

    private func stats() -> StatsResult {
        lock.lock()
        let running = jobs.keys.sorted()
        lock.unlock()
        return StatsResult(
            memory: AudioSmithProtocol.Memory(
                active: Int64(MLX.Memory.activeMemory), cache: Int64(MLX.Memory.cacheMemory), peak: Int64(MLX.Memory.peakMemory)),
            models: synth?.loaded() ?? [], jobs: running)
    }

    /// Runs work as job id: its response is written when it ends, after
    /// every notification it sent.
    private func startJob<R: Encodable>(_ id: String, _ work: @escaping @Sendable () async throws -> R) {
        lock.lock()
        defer { lock.unlock() }
        if jobs[id] != nil {
            out.fail(id, RPCError(Code.badRequest, "Job \(id) is already running"))
            return
        }
        if jobs.count >= concurrency {
            out.fail(id, RPCError(Code.busy, "The engine runs \(concurrency) job at a time"))
            return
        }
        let out = self.out
        jobs[id] = Task { [weak self] in
            let started = Date()
            do {
                let result = try await work()
                self?.finish(id)
                out.result(id, result)
            } catch {
                let cancelled = Task.isCancelled || error is CancellationError || (self?.wasCancelled(id) ?? false)
                self?.finish(id)
                if cancelled {
                    out.fail(id, RPCError(Code.cancelled, "Cancelled"))
                } else {
                    let e = rpcError(error)
                    log("job \(id) failed after \(String(format: "%.1f", Date().timeIntervalSince(started))) s: \(e.code): \(e.message)")
                    out.fail(id, e)
                }
            }
        }
    }

    private func wasCancelled(_ id: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        return cancelled.contains(id)
    }

    /// Forgets job id. Called before its response is written, so a
    /// job.cancel that arrives after it is answered false.
    private func finish(_ id: String) {
        lock.lock()
        jobs[id] = nil
        cancelled.remove(id)
        lock.unlock()
    }

    private func cancel(_ id: String) -> Bool {
        lock.lock()
        defer { lock.unlock() }
        guard let task = jobs[id] else { return false }
        cancelled.insert(id)
        task.cancel()
        return true
    }

    private func cancelAll() {
        lock.lock()
        let tasks = Array(jobs.values)
        cancelled.formUnion(jobs.keys)
        lock.unlock()
        tasks.forEach { $0.cancel() }
    }
}

func describe(_ error: DecodingError) -> String {
    switch error {
    case .keyNotFound(let key, _): "missing \(key.stringValue)"
    case .typeMismatch(_, let ctx), .valueNotFound(_, let ctx), .dataCorrupted(let ctx):
        "\(ctx.codingPath.map(\.stringValue).joined(separator: ".")): \(ctx.debugDescription)"
    @unknown default: "\(error)"
    }
}

func deviceInfo() -> AudioSmithProtocol.Device {
    var size = 0
    sysctlbyname("machdep.cpu.brand_string", nil, &size, nil, 0)
    var name = [CChar](repeating: 0, count: max(size, 1))
    sysctlbyname("machdep.cpu.brand_string", &name, &size, nil, 0)
    let v = ProcessInfo.processInfo.operatingSystemVersion
    return AudioSmithProtocol.Device(
        name: String(cString: name), memory: Int64(ProcessInfo.processInfo.physicalMemory),
        os: "macOS \(v.majorVersion).\(v.minorVersion).\(v.patchVersion)", mlx: mlxVersion)
}
