import Foundation

let engineVersion = "0.1.0"

/// Runs one job (load or synthesize) at a time and lets "cancel" stop it.
final class JobRunner: @unchecked Sendable {
    private let lock = NSLock()
    private var current: (id: String?, task: Task<Void, Never>)?
    private let events: EventWriter
    private let synth: Synthesizer

    init(events: EventWriter, synth: Synthesizer) {
        self.events = events
        self.synth = synth
    }

    var busy: Bool {
        lock.lock()
        defer { lock.unlock() }
        return current != nil
    }

    func start(id: String?, _ work: @escaping @Sendable () async throws -> Void) {
        lock.lock()
        defer { lock.unlock() }
        if current != nil {
            events.error("The engine is busy with another job", id: id)
            return
        }
        let task = Task { [weak self] in
            do {
                try await work()
            } catch is CancellationError {
                self?.events.send(Event("cancelled", id: id))
            } catch {
                if Task.isCancelled {
                    self?.events.send(Event("cancelled", id: id))
                } else {
                    self?.events.error(error.localizedDescription, id: id)
                }
            }
            self?.finish()
        }
        current = (id, task)
    }

    func cancel() {
        lock.lock()
        let task = current?.task
        lock.unlock()
        task?.cancel()
    }

    private func finish() {
        lock.lock()
        current = nil
        lock.unlock()
    }
}

func handle(_ req: Request, events: EventWriter, synth: Synthesizer, jobs: JobRunner) {
    switch req.cmd {
    case "ping":
        var e = Event("pong", id: req.id)
        e.model = synth.modelID
        events.send(e)
    case "load":
        guard let model = req.model, !model.isEmpty else {
            events.error("Missing model", id: req.id)
            return
        }
        jobs.start(id: req.id) {
            try await synth.load(model, requestID: req.id)
        }
    case "synthesize":
        jobs.start(id: req.id) {
            let r = try await synth.synthesize(req)
            var e = Event("done", id: req.id)
            e.output = r.output.path
            e.model = req.model
            e.sampleRate = r.sampleRate
            e.audioSeconds = r.audioSeconds
            e.seconds = r.seconds
            e.rtf = r.seconds > 0 ? r.audioSeconds / r.seconds : nil
            events.send(e)
        }
    case "cancel":
        jobs.cancel()
    case "unload":
        if jobs.busy {
            events.error("Cancel the running job before unloading", id: req.id)
            return
        }
        synth.unload()
        events.send(Event("unloaded", id: req.id))
    case "shutdown":
        exit(0)
    default:
        events.error("Unknown command \(req.cmd)", id: req.id)
    }
}

/// `audiosmith-engine --text "…" [--model repo] [--voice v] [--output out.wav]`
/// runs one synthesis and exits: a smoke test without the app.
func runOnce(_ args: [String], events: EventWriter, synth: Synthesizer) async -> Int32 {
    var req = Request(cmd: "synthesize")
    req.id = "once"
    req.model = "mlx-community/Kokoro-82M-bf16"
    req.output = "output.wav"
    var it = args.makeIterator()
    while let arg = it.next() {
        let value = it.next()
        switch arg {
        case "--text": req.text = value
        case "--model": req.model = value
        case "--voice": req.voice = value
        case "--language": req.language = value
        case "--output": req.output = value
        case "--speed": req.speed = value.flatMap(Double.init)
        case "--ref-audio": req.refAudio = value
        case "--ref-text": req.refText = value
        default:
            log("Unknown option \(arg)")
            return 2
        }
    }
    if let out = req.output, !out.hasPrefix("/") {
        req.output = FileManager.default.currentDirectoryPath + "/" + out
    }
    do {
        let r = try await synth.synthesize(req)
        var e = Event("done", id: req.id)
        e.output = r.output.path
        e.model = req.model
        e.sampleRate = r.sampleRate
        e.audioSeconds = r.audioSeconds
        e.seconds = r.seconds
        e.rtf = r.seconds > 0 ? r.audioSeconds / r.seconds : nil
        events.send(e)
        return 0
    } catch {
        events.error(error.localizedDescription, id: req.id)
        return 1
    }
}

@main
enum Main {
    static func main() async {
        let args = Array(CommandLine.arguments.dropFirst())
        if args.first == "--version" {
            print(engineVersion)
            return
        }

        signal(SIGPIPE, SIG_IGN)
        let events = EventWriter()
        let synth = Synthesizer(events: events)

        if !args.isEmpty {
            exit(await runOnce(args, events: events, synth: synth))
        }

        let jobs = JobRunner(events: events, synth: synth)
        var ready = Event("ready")
        ready.version = engineVersion
        ready.protocolVersion = engineProtocolVersion
        ready.pid = getpid()
        events.send(ready)

        let decoder = JSONDecoder()
        do {
            for try await line in FileHandle.standardInput.bytes.lines {
                let trimmed = line.trimmingCharacters(in: .whitespaces)
                if trimmed.isEmpty { continue }
                do {
                    let req = try decoder.decode(Request.self, from: Data(trimmed.utf8))
                    handle(req, events: events, synth: synth, jobs: jobs)
                } catch {
                    events.error("Bad request: \(error.localizedDescription)", id: nil)
                }
            }
        } catch {
            log("stdin failed: \(error)")
        }
        // The app closed our stdin: it quit or crashed. Don't outlive it.
        exit(0)
    }
}
