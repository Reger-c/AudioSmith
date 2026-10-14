import Foundation

// The invisible watermark (docs/protocol-v2.md §5.5): the Swift mirror of
// internal/engine/proto/watermark.go. Optional here = omitempty there.

public struct WatermarkEmbedParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var input: String
    public var output: Output
    public var message: Int
    public var strength: Double?
}

public struct WatermarkEmbedResult: Codable, Sendable {
    public var output: String
    public var sampleRate: Int
    public var channels: Int
    public var frames: Int64
    public var audioSeconds: Double
    public var model: String
    public var message: Int
    public var snr: Double
    public var seconds: Double

    public init(output: String, sampleRate: Int, channels: Int, frames: Int64, audioSeconds: Double, model: String,
                message: Int, snr: Double, seconds: Double) {
        self.output = output
        self.sampleRate = sampleRate
        self.channels = channels
        self.frames = frames
        self.audioSeconds = audioSeconds
        self.model = model
        self.message = message
        self.snr = snr
        self.seconds = seconds
    }
}

public struct WatermarkDetectParams: Codable, Sendable {
    public var model: String
    public var modelPath: String?
    public var input: String
    public var threshold: Double?
}

public struct WatermarkFrame: Codable, Sendable {
    public var start: Double
    public var end: Double
    public var probability: Double

    public init(start: Double, end: Double, probability: Double) {
        self.start = start
        self.end = end
        self.probability = probability
    }
}

public struct WatermarkDetectResult: Codable, Sendable {
    public var probability: Double
    public var detected: Bool
    public var message: Int
    public var bits: [Double]
    public var confidence: Double
    public var frames: [WatermarkFrame]?
    public var audioSeconds: Double
    public var model: String
    public var seconds: Double

    public init(probability: Double, detected: Bool, message: Int, bits: [Double], confidence: Double,
                frames: [WatermarkFrame]?, audioSeconds: Double, model: String, seconds: Double) {
        self.probability = probability
        self.detected = detected
        self.message = message
        self.bits = bits
        self.confidence = confidence
        self.frames = frames
        self.audioSeconds = audioSeconds
        self.model = model
        self.seconds = seconds
    }
}
