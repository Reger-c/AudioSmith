import AudioSmithProtocol
import Foundation
@preconcurrency import MLX

let engineVersion = "0.3.0"
/// The MLX the engine is built with (Package.resolved pins mlx-swift).
let mlxVersion = "0.32.3"

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
        // Developer checks of the SoulX-Podcast port (SoulXCheck.swift).
        if args.first == "--soulx-dump-params", args.count > 1 {
            do { try SoulXCheck.dumpParams(args[1]) } catch { log("\(error)"); exit(1) }
            return
        }
        if args.first == "--soulx-check" {
            do { exit(try await SoulXCheck.run(Array(args.dropFirst()))) } catch { log("\(errorMessage(error))"); exit(1) }
        }
        // Developer checks of the MOSS-TTSD port (MossTTSDCheck.swift).
        if args.first == "--moss-check" {
            do { exit(try await MossTTSDCheck.run(Array(args.dropFirst()))) } catch { log("\(errorMessage(error))"); exit(1) }
        }
        let out = Writer()
        let server = Server(role: role, out: out)
        // `--dialogue request.json`: one tts.dialogue (its params as JSON), printed like a job.
        if args.first == "--dialogue", args.count > 1, let synth = server.synth {
            do {
                let p = try JSONDecoder().decode(DialogueParams.self, from: Data(contentsOf: URL(fileURLWithPath: args[1])))
                let aux = try? JSONDecoder().decode(AuxParams.self, from: Data(contentsOf: URL(fileURLWithPath: args[1])))
                out.result("once", try await synth.dialogue(p, job: "once", offline: false, auxPaths: aux?.auxPaths))
                log(String(format: "MLX peak memory %.2f GB", Double(MLX.Memory.peakMemory) / 1e9))
                exit(0)
            } catch {
                out.fail("once", rpcError(error))
                exit(1)
            }
        }
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
