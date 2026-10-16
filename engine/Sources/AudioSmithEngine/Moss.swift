import AudioSmithProtocol
import AudioSmithText
import Foundation
@preconcurrency import MLX
import MLXAudioSTT
import NaturalLanguage

// stt.transcribe with MOSS-Transcribe-Diarize (family
// moss_transcribe_diarize, thread #48): one pass gives the text, segment
// times and anonymous speaker labels ("[0.48][S01]Welcome everyone[1.66]").
// It takes no language parameter. The app sends at most 10 minutes per
// call, skips windows without speech and links the speakers of longer
// audio's windows (services.TranscribeSpeakers); the engine decodes what
// it gets in one pass. Patch 0005 makes the decode cancellable and takes
// the prompt.

extension Analyzer {
    /// The family's model_type in config.json.
    static let mossModelType = "moss_transcribe_diarize"

    /// Memory a MOSS load and a 10-minute pass add (bytes): its 1.3 GB of
    /// weights, the encoder's activations and a KV cache of about 13k
    /// tokens (peaks of 2.6–3.2 GB measured, threads #44 and Dicta's).
    /// ModelStore drops idle models (TTS, the LLM) first when the budget is
    /// short.
    static let mossEstimate: Int64 = 3_500_000_000

    /// Whether a repo id names a MOSS-Transcribe-Diarize conversion.
    static func isMoss(_ model: String) -> Bool {
        model.lowercased().contains("moss-transcribe-diarize")
    }

    /// Loads MOSS from its snapshot, which its loader only reads. Never by
    /// repo id: mlx-audio-swift's resolver deletes a cache it finds
    /// incomplete (the repo's models--… folder included), and the app's
    /// snapshot is that folder.
    func loadMoss(_ model: String, dir: URL?) async throws -> MossTranscribeDiarizeModel {
        guard let dir else {
            throw EngineError.notDownloaded(model: model, missing: ["config.json"])
        }
        return try await MossTranscribeDiarizeModel.fromModelDirectory(dir)
    }

    /// The token budget of a pass over seconds of audio: speech is at most
    /// about 8 tokens a second, and each turn adds about 12 for its times
    /// and label; the decoder stops at its end token long before.
    static func mossMaxTokens(seconds: Double) -> Int {
        max(256, Int((seconds * 20).rounded(.up)) + 128)
    }

    /// One pass of MOSS over samples (16 kHz mono) in the job's task;
    /// cancelling stops it at the next token. Progress follows the
    /// timestamps it writes.
    func transcribeMoss(_ model: MossTranscribeDiarizeModel, samples: [Float], prompt: String?,
                        progress: JobProgress) throws -> (String, [Segment], String?) {
        let seconds = Double(samples.count) / 16000
        let params = STTGenerateParameters(
            maxTokens: Self.mossMaxTokens(seconds: seconds), temperature: 0, topP: 1, topK: 0,
            language: nil, chunkDuration: Float(seconds + 60), minChunkDuration: 0,
            repetitionPenalty: 1, repetitionContextSize: 100)
        var tail = ""
        let output = try model.transcribe(audio: MLXArray(samples), generationParameters: params,
                                          prompt: mossInstruction(prompt: prompt)) { text in
            tail = String((tail + text).suffix(48))
            if let t = lastTimestamp(tail), seconds > 0 {
                let f = min(t / seconds, 0.99)
                if f > (progress.fraction ?? 0) + 0.005 {
                    progress.fraction = f
                    progress.send("transcribing")
                }
            }
        }
        MLX.Memory.clearCache()
        let turns = timeMossTurns(parseMossTurns(output.text), duration: seconds)
        let segments = turns.map {
            Segment(start: round3($0.start ?? 0), end: round3($0.end ?? 0), text: $0.text, speaker: $0.speaker)
        }
        let text = joinMossTexts(segments.map(\.text))
        progress.fraction = 1
        progress.send("transcribing", force: true)
        return (text, segments, detectLanguage(text))
    }
}

/// The last "[12.34]" timestamp in s.
private func lastTimestamp(_ s: String) -> Double? {
    guard let close = s.lastIndex(of: "]") else { return nil }
    var i = close
    while i > s.startIndex {
        i = s.index(before: i)
        if s[i] == "[" { return Double(s[s.index(after: i) ..< close]) }
        if !(s[i].isNumber || s[i] == ".") { return nil }
    }
    return nil
}

/// The dominant language of text as a BCP-47 code; nil when it cannot
/// tell. Chinese, Japanese and Korean by script first (Apple's recogniser
/// calls Chinese with English terms English), then Apple's on-device
/// recogniser.
func detectLanguage(_ text: String) -> String? {
    guard !text.isEmpty else { return nil }
    if let l = cjkLanguage(text) { return l }
    let r = NLLanguageRecognizer()
    r.processString(text)
    guard let l = r.dominantLanguage, l != .undetermined else { return nil }
    switch l {
    case .simplifiedChinese, .traditionalChinese: return "zh"
    default: return baseLanguage(l.rawValue)
    }
}
