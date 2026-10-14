import Foundation

// The methods M4 (dubbing) adds (docs/protocol-v2.md §5.3): the Swift
// mirror of internal/engine/proto/m4.go. Optional here = omitempty there.

public struct SeparateParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var audio: String
    public var vocals: Output
    public var background: Output
    public var shifts: Int?
    public var overlap: Double?
}

public struct SeparateResult: Codable, Sendable {
    public var vocals: String?
    public var background: String?
    public var sampleRate: Int
    public var channels: Int
    public var frames: Int64
    public var audioSeconds: Double
    public var model: String
    public var seconds: Double

    public init(vocals: String?, background: String?, sampleRate: Int, channels: Int, frames: Int64, audioSeconds: Double, model: String, seconds: Double) {
        self.vocals = vocals
        self.background = background
        self.sampleRate = sampleRate
        self.channels = channels
        self.frames = frames
        self.audioSeconds = audioSeconds
        self.model = model
        self.seconds = seconds
    }
}

public struct DiarizeParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var audio: String
    public var speakers: Int?
    public var minSpeakers: Int?
    public var maxSpeakers: Int?
}

public struct Turn: Codable, Sendable, Equatable {
    public var start: Double
    public var end: Double
    public var speaker: String

    public init(start: Double, end: Double, speaker: String) {
        self.start = start
        self.end = end
        self.speaker = speaker
    }
}

public struct DiarizeResult: Codable, Sendable {
    public var turns: [Turn]
    public var speakers: Int
    public var model: String
    public var audioSeconds: Double
    public var seconds: Double

    public init(turns: [Turn], speakers: Int, model: String, audioSeconds: Double, seconds: Double) {
        self.turns = turns
        self.speakers = speakers
        self.model = model
        self.audioSeconds = audioSeconds
        self.seconds = seconds
    }
}

public struct MuxTrack: Codable, Sendable {
    public var path: String
    public var language: String?
    public var title: String?
    public var `default`: Bool?
    public var bitrate: Int?
}

public struct MuxSubtitle: Codable, Sendable {
    public var path: String
    public var language: String?
    public var title: String?
    public var `default`: Bool?
    public var forced: Bool?
}

public struct Retime: Codable, Sendable {
    public var start: Double
    public var end: Double
    public var factor: Double
}

public struct MuxParams: Codable, Sendable {
    public var video: String
    public var keepAudio: Bool?
    public var audio: [MuxTrack]
    public var subtitles: [MuxSubtitle]?
    public var output: Output
    public var retime: [Retime]?
}

public struct MuxResult: Codable, Sendable {
    public var output: String
    public var bytes: Int64
    public var duration: Double
    public var tracks: Int
    public var reencoded: Bool?

    public init(output: String, bytes: Int64, duration: Double, tracks: Int, reencoded: Bool? = nil) {
        self.output = output
        self.bytes = bytes
        self.duration = duration
        self.tracks = tracks
        self.reencoded = reencoded
    }
}

public struct ThumbnailParams: Codable, Sendable {
    public var input: String
    public var times: [Double]
    public var outputs: [String]
    public var width: Int?
}

public struct Frame: Codable, Sendable {
    public var time: Double
    public var path: String
    public var width: Int
    public var height: Int

    public init(time: Double, path: String, width: Int, height: Int) {
        self.time = time
        self.path = path
        self.width = width
        self.height = height
    }
}

public struct ThumbnailResult: Codable, Sendable {
    public var frames: [Frame]
    public init(frames: [Frame]) { self.frames = frames }
}

public struct TranscodeParams: Codable, Sendable {
    public var input: String
    public var output: Output
    public var maxHeight: Int?
    public var noAudio: Bool?
}

public struct TranscodeResult: Codable, Sendable {
    public var output: String
    public var bytes: Int64
    public var duration: Double
    public var width: Int
    public var height: Int

    public init(output: String, bytes: Int64, duration: Double, width: Int, height: Int) {
        self.output = output
        self.bytes = bytes
        self.duration = duration
        self.width = width
        self.height = height
    }
}
