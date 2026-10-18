import Foundation

// SoulX-Podcast's prompt and its history window (thread #80), as upstream
// builds them (soulxpodcast/utils/dataloader.py, utils/text.py and
// models/soulxpodcast.py forward_longform at 5ac9c0e).
//
// The language model reads one sequence:
//
//   <|task_podcast|>
//   <|SPEAKER_0|><|text_start|>reference text<|text_end|><|semantic_token_start|>reference speech tokens EOS
//   <|SPEAKER_1|>… (one block per speaker)
//   <|SPEAKER_i|><|text_start|>turn text<|text_end|><|semantic_token_start|> → the model continues
//   with the turn's speech tokens until EOS; the next turn follows.
//
// The sequence grows turn by turn (the KV cache is kept). When it holds
// more than maxTurnSize blocks or turnTokensThreshold tokens, it is rebuilt:
// the speakers' reference blocks, the texts (without speech) of a few
// earlier turns, and the last historyContext turns with their speech.

public enum SoulXPrompt {
    public static let speakerTags = ["<|SPEAKER_0|>", "<|SPEAKER_1|>", "<|SPEAKER_2|>", "<|SPEAKER_3|>"]
    public static let maxSpeakers = 4
    static let textStart = "<|text_start|>"
    static let textEnd = "<|text_end|>"
    static let audioStart = "<|semantic_token_start|>"
    static let task = "<|task_podcast|>"

    /// Upstream's normalize_text: no spaces between Chinese characters, or
    /// between Chinese and Latin letters; a full stop after a final Chinese
    /// character or Latin letter. (Tags such as <|laughter|> stay.)
    public static func normalize(_ text: String) -> String {
        var t = text.trimmingCharacters(in: .whitespacesAndNewlines)
        // Each rule runs once over the text, as re.sub does (non-overlapping).
        t = replaceOnce(t, #"([\x{4e00}-\x{9fff}])\s+([\x{4e00}-\x{9fff}])"#)
        t = replaceOnce(t, #"([a-zA-Z])\s+([\x{4e00}-\x{9fff}])"#)
        t = replaceOnce(t, #"([\x{4e00}-\x{9fff}])\s+([a-zA-Z])"#)
        guard let last = t.unicodeScalars.last else { return t }
        if (0x4E00 ... 0x9FFF).contains(last.value) {
            t += "。"
        } else if ("a" ... "z").contains(Character(last)) || ("A" ... "Z").contains(Character(last)) {
            t += "."
        }
        return t
    }

    private static func replaceOnce(_ s: String, _ pattern: String) -> String {
        guard let re = try? NSRegularExpression(pattern: pattern) else { return s }
        return re.stringByReplacingMatches(in: s, range: NSRange(s.startIndex..., in: s), withTemplate: "$1$2")
    }

    /// A speaker's reference block text (the speech tokens follow it).
    public static func reference(speaker: Int, text: String) -> String {
        let block = "\(speakerTags[speaker])\(textStart)\(normalize(text))\(textEnd)\(audioStart)"
        return speaker == 0 ? task + block : block
    }

    /// A turn's text; the model continues with its speech tokens.
    public static func turn(speaker: Int, text: String) -> String {
        "\(speakerTags[speaker])\(textStart)\(normalize(text))\(textEnd)\(audioStart)"
    }
}

/// The token sequence across turns, with upstream's history window.
public struct SoulXContext: Sendable {
    public var maxTurnSize = 10
    public var turnTokensThreshold = 6192
    /// Reference blocks kept when the context is rebuilt. Upstream fixes it
    /// at 2 (two-speaker podcasts), which would drop speakers 3 and 4: here
    /// it is the number of speakers.
    public var promptContext: Int
    public var historyContext = 2
    public var historyTextContext = 2

    /// Blocks with speech: the references, then each turn's text + speech.
    private(set) var promptInputs: [[Int]]
    /// Blocks for the rebuilt context: the references, then each turn's text only.
    private(set) var historyInputs: [[Int]]
    /// The sequence the model has read, plus the current turn's text.
    public private(set) var inputs: [Int]
    private var validTurnSize: Int
    /// Times the context was rebuilt.
    public private(set) var rebuilds = 0

    /// references: per speaker, the reference text's tokens + speech tokens + EOS.
    public init(references: [[Int]]) {
        promptInputs = references
        historyInputs = references
        inputs = references.flatMap { $0 }
        validTurnSize = references.count
        promptContext = references.count
    }

    /// Starts a turn whose text tokens (ending with <|semantic_token_start|>)
    /// are text. Returns true when the context was rebuilt: the caller drops
    /// its KV cache and reads `inputs` from the start.
    public mutating func begin(text: [Int]) -> Bool {
        var rebuilt = false
        if validTurnSize > maxTurnSize || inputs.count > turnTokensThreshold {
            let n = historyInputs.count
            let bound = max(promptContext, n - historyTextContext - historyContext)
            var blocks = Array(historyInputs[..<min(promptContext, n)])
            if bound < n - historyContext { blocks += historyInputs[bound ..< (n - historyContext)] }
            blocks += promptInputs.suffix(historyContext)
            inputs = blocks.flatMap { $0 }
            validTurnSize = promptContext + n - bound
            rebuilt = true
            rebuilds += 1
        }
        validTurnSize += 1
        inputs += text
        return rebuilt
    }

    /// Ends the turn begun with text: generated are its speech tokens as
    /// vocabulary ids, with the final EOS when the model wrote one.
    public mutating func end(text: [Int], generated: [Int]) {
        inputs += generated
        promptInputs.append(text + generated)
        historyInputs.append(Array(text.dropLast()))
    }
}
