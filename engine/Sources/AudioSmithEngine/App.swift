import AudioSmithProtocol
import Foundation

let engineVersion = "0.2.0"
/// The MLX the engine is built with (Package.resolved pins mlx-swift).
let mlxVersion = "0.31.6"

/// `audiosmith-engine --text "…" [--model repo] [--voice v] [--output out.wav] …`
/// runs one synthesis and exits: a smoke test without the app. It prints
/// the job's notifications and the tts.synthesize response (protocol 2) to stdout.
func runOnce(_ args: [String], out: Writer, synth: Synthesizer) async -> Int32 {
    var p = SynthesizeParams(model: "mlx-community/Kokoro-82M-bf16", text: "", output: Output(path: "output.wav"))
    var it = args.makeIterator()
    while let arg = it.next() {
        if arg == "--stream" {
            p.stream = true
            continue
        }
        let value = it.next()
        switch arg {
        case "--text": p.text = value ?? ""
        case "--model": p.model = value ?? ""
        case "--voice": p.voice = value
        case "--language": p.language = value
        case "--output": p.output.path = value ?? ""
        case "--speed": p.speed = value.flatMap(Double.init)
        case "--seed": p.seed = value.flatMap(Int64.init)
        case "--bits": p.output.bits = value.flatMap(Int.init)
        case "--ref-audio": p.refAudio = value
        case "--ref-text": p.refText = value
        default:
            log("Unknown option \(arg)")
            return 2
        }
    }
    if !p.output.path.hasPrefix("/") {
        p.output.path = FileManager.default.currentDirectoryPath + "/" + p.output.path
    }
    do {
        let r = try await synth.synthesize(p, job: "once", offline: false)
        out.result("once", r)
        return 0
    } catch {
        out.fail("once", rpcError(error))
        return 1
    }
}

@main
enum Main {
    static func main() async {
        var args = Array(CommandLine.arguments.dropFirst())
        if args.first == "--version" {
            print(engineVersion)
            return
        }
        var role = Role.gpu
        if let i = args.firstIndex(of: "--role") {
            guard i + 1 < args.count, let r = Role(rawValue: args[i + 1]) else {
                log("--role takes gpu, listen or media")
                exit(2)
            }
            role = r
            args.removeSubrange(i ... i + 1)
        }

        signal(SIGPIPE, SIG_IGN)
        let out = Writer()
        let server = Server(role: role, out: out)
        if !args.isEmpty {
            guard let synth = server.synth else {
                log("one-shot synthesis runs only as --role gpu")
                exit(2)
            }
            exit(await runOnce(args, out: out, synth: synth))
        }
        log("audiosmith-engine \(engineVersion) (protocol \(protocolVersion)) as \(role.rawValue), pid \(getpid())")
        await server.run()
    }
}
