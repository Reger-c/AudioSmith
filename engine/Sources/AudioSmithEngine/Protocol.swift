import Foundation

// The wire protocol between AudioSmith (Go) and audiosmith-engine (Swift).
//
// One JSON object per line. The app writes requests to the engine's stdin;
// the engine writes events to its stdout. Everything else the engine or its
// libraries print goes to stderr, so stdout stays machine-readable.
//
// Requests:
//   {"id":"1","cmd":"ping"}
//   {"id":"2","cmd":"load","model":"mlx-community/Kokoro-82M-bf16"}
//   {"id":"3","cmd":"synthesize","model":"…","text":"…","voice":"af_heart",
//    "language":"en","speed":1.0,"temperature":0.7,"topP":0.9,"maxTokens":1200,
//    "refAudio":"/path/ref.wav","refText":"…","output":"/path/out.wav"}
//   {"id":"4","cmd":"cancel"}            cancels the running job
//   {"id":"5","cmd":"unload"}            frees the loaded model
//   {"id":"6","cmd":"shutdown"}
//
// Events (always carry the id of the request they answer, except "ready"):
//   ready     {version, pid}
//   pong      {model?}
//   status    {message}                 human-readable progress
//   loaded    {model, sampleRate, seconds}
//   progress  {fraction?, audioSeconds?}
//   done      {output, model, sampleRate, audioSeconds, seconds, rtf}
//   cancelled {}
//   unloaded  {}
//   error     {message}

let engineProtocolVersion = 1

struct Request: Decodable {
    var id: String?
    var cmd: String
    var model: String?
    var text: String?
    var voice: String?
    var language: String?
    var speed: Double?
    var temperature: Double?
    var topP: Double?
    var maxTokens: Int?
    var refAudio: String?
    var refText: String?
    var output: String?
}

struct Event: Encodable {
    var id: String?
    var event: String
    var message: String?
    var model: String?
    var version: String?
    var protocolVersion: Int?
    var pid: Int32?
    var sampleRate: Int?
    var seconds: Double?
    var fraction: Double?
    var audioSeconds: Double?
    var rtf: Double?
    var output: String?

    init(_ event: String, id: String? = nil) {
        self.event = event
        self.id = id
    }
}

/// Writes events to the original stdout, which it takes over at startup.
final class EventWriter: @unchecked Sendable {
    private let handle: FileHandle
    private let lock = NSLock()
    private let encoder: JSONEncoder = {
        let e = JSONEncoder()
        e.outputFormatting = [.sortedKeys, .withoutEscapingSlashes]
        return e
    }()

    /// Duplicates stdout for events, then points stdout at stderr so stray
    /// prints of the engine's libraries cannot corrupt the protocol.
    init() {
        let fd = dup(STDOUT_FILENO)
        dup2(STDERR_FILENO, STDOUT_FILENO)
        handle = FileHandle(fileDescriptor: fd, closeOnDealloc: true)
    }

    func send(_ event: Event) {
        guard var data = try? encoder.encode(event) else { return }
        data.append(0x0A)
        lock.lock()
        defer { lock.unlock() }
        try? handle.write(contentsOf: data)
    }

    func error(_ message: String, id: String?) {
        var e = Event("error", id: id)
        e.message = message
        send(e)
    }

    func status(_ message: String, id: String?) {
        var e = Event("status", id: id)
        e.message = message
        send(e)
    }
}

func log(_ message: String) {
    FileHandle.standardError.write(Data("[engine] \(message)\n".utf8))
}
