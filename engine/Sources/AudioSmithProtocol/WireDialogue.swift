import Foundation

// tts.dialogue (docs/protocol-v2.md §5.6): a whole conversation in one job,
// each turn hearing the ones before it. The Swift mirror of
// internal/engine/proto/dialogue.go. Optional here = omitempty there.

extension Method {
    public static let ttsDialogue = "tts.dialogue"
    /// Notification: one turn is written (per-turn models).
    public static let ttsTurn = "tts.turn"
}

/// A speaker of the dialogue: a reference clip and its exact transcript.
public struct DialogueSpeaker: Codable, Sendable {
    public var id: String
    public var refAudio: String
    public var refText: String

    public init(id: String, refAudio: String, refText: String) {
        self.id = id
        self.refAudio = refAudio
        self.refText = refText
    }
}

/// One turn. With codes (a turn's codes from an earlier result) the turn is
/// context only: it is not generated again and gets no audio.
public struct DialogueTurn: Codable, Sendable {
    public var speaker: String
    public var text: String
    public var codes: [Int]?

    public init(speaker: String, text: String, codes: [Int]? = nil) {
        self.speaker = speaker
        self.text = text
        self.codes = codes
    }
}

public struct DialogueParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var speakers: [DialogueSpeaker]
    public var turns: [DialogueTurn]
    /// Per-turn audio goes to <outputDir>/turn-<index, 3 digits>.wav, a
    /// mixed track to <outputDir>/dialogue.wav.
    public var outputDir: String
    public var bits: Int?
    public var language: String?
    public var seed: Int64?
    public var temperature: Double?
    public var topP: Double?
    public var topK: Int?
    public var repetitionPenalty: Double?
    /// Most speech tokens per turn.
    public var maxTokens: Int?
    /// Flow-matching steps.
    public var steps: Int?

    public init(model: String, speakers: [DialogueSpeaker], turns: [DialogueTurn], outputDir: String) {
        self.model = model
        self.speakers = speakers
        self.turns = turns
        self.outputDir = outputDir
    }
}

/// A turn of the result. Per-turn models fill output…frames and codes;
/// one-track models give only index, speaker and text (the app cuts the
/// mixed track at the turns, e.g. with stt.align).
public struct DialogueTurnOut: Codable, Sendable {
    public var index: Int
    public var speaker: String
    public var text: String?
    public var output: String?
    public var frames: Int64?
    public var audioSeconds: Double?
    public var codes: [Int]?
    public var seconds: Double?

    public init(index: Int, speaker: String) {
        self.index = index
        self.speaker = speaker
    }
}

public struct DialogueMixed: Codable, Sendable {
    public var output: String
    public var frames: Int64
    public var audioSeconds: Double

    public init(output: String, frames: Int64, audioSeconds: Double) {
        self.output = output
        self.frames = frames
        self.audioSeconds = audioSeconds
    }
}

public struct DialogueResult: Codable, Sendable {
    public var model: String
    public var sampleRate: Int
    public var channels: Int
    /// The generated turns, in order (context-only turns are left out).
    public var turns: [DialogueTurnOut]
    /// One-track models: the whole dialogue in one file.
    public var mixed: DialogueMixed?
    public var audioSeconds: Double
    public var seconds: Double
    public var rtf: Double
    public var seed: Int64?

    public init(model: String, sampleRate: Int, channels: Int, turns: [DialogueTurnOut], mixed: DialogueMixed?,
                audioSeconds: Double, seconds: Double, rtf: Double, seed: Int64?) {
        self.model = model
        self.sampleRate = sampleRate
        self.channels = channels
        self.turns = turns
        self.mixed = mixed
        self.audioSeconds = audioSeconds
        self.seconds = seconds
        self.rtf = rtf
        self.seed = seed
    }
}

/// tts.turn: a turn's audio is written (sent before the job's response).
public struct TurnParams: Codable, Sendable {
    public var job: String
    public var index: Int
    public var speaker: String
    public var output: String
    public var frames: Int64
    public var audioSeconds: Double
    public var sampleRate: Int

    public init(job: String, index: Int, speaker: String, output: String, frames: Int64, audioSeconds: Double, sampleRate: Int) {
        self.job = job
        self.index = index
        self.speaker = speaker
        self.output = output
        self.frames = frames
        self.audioSeconds = audioSeconds
        self.sampleRate = sampleRate
    }
}
