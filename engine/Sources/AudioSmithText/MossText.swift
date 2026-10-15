import Foundation

// The text MOSS-Transcribe-Diarize writes, and how the engine reads it
// (docs/protocol-v2.md, stt.transcribe "Speakers"). Pure functions without
// MLX, so the engine's tests (scripts/test-engine.sh) check them.
//
// The model writes timestamped speaker turns:
//
//     [0.48][S01]Welcome everyone[1.66][1.80][S02]Thanks.[2.50]
//
// and sometimes acoustic events inside the text ("[laughter]"). It may
// leave out an end time, or write plain text without any tag.

/// One speaker turn of MOSS's output: times in seconds (nil when the model
/// left them out), its speaker label ("S01"; nil without one) and its text
/// without tags or events.
public struct MossTurn: Equatable, Sendable {
    public var start: Double?
    public var end: Double?
    public var speaker: String?
    public var text: String

    public init(start: Double?, end: Double?, speaker: String?, text: String) {
        self.start = start
        self.end = end
        self.speaker = speaker
        self.text = text
    }
}

private let mossTagPattern = try! NSRegularExpression(pattern: #"\[(\d+(?:[.,]\d+)?|[Ss]\d{1,3})\]"#)
private let mossEventPattern = try! NSRegularExpression(pattern: #"\[([A-Za-z][A-Za-z0-9 _-]{0,31})\]"#)

/// Reads MOSS's output into turns, in order. A timestamp opens a turn (its
/// start) or, after text, closes it (its end); a speaker label names the
/// open turn, or starts a new one after text. Turns without text are
/// dropped; events are removed from the text.
public func parseMossTurns(_ output: String) -> [MossTurn] {
    let ns = output as NSString
    var turns: [MossTurn] = []
    var cur = MossTurn(start: nil, end: nil, speaker: nil, text: "")
    func close(end: Double?) {
        let text = cleanMossText(cur.text)
        if !text.isEmpty {
            turns.append(MossTurn(start: cur.start, end: end, speaker: cur.speaker, text: text))
        }
        cur = MossTurn(start: nil, end: nil, speaker: nil, text: "")
    }
    var cursor = 0
    for m in mossTagPattern.matches(in: output, range: NSRange(location: 0, length: ns.length)) {
        cur.text += ns.substring(with: NSRange(location: cursor, length: m.range.location - cursor))
        cursor = m.range.location + m.range.length
        let tag = ns.substring(with: m.range(at: 1))
        let hasText = !cleanMossText(cur.text).isEmpty
        if let first = tag.first, first == "S" || first == "s" {
            let label = "S" + String(format: "%02d", Int(tag.dropFirst()) ?? 0)
            if hasText { close(end: nil) }
            cur.speaker = label
        } else if let t = Double(tag.replacingOccurrences(of: ",", with: ".")) {
            if hasText {
                close(end: t)
            } else {
                cur.start = t
                cur.text = "" // whitespace between tags
            }
        }
    }
    cur.text += ns.substring(from: cursor)
    close(end: nil)
    return turns
}

/// Fills the times MOSS left out and keeps them in order within
/// 0…duration: a turn without a start starts where the one before ended
/// (0 for the first); one without an end ends where the next starts (the
/// duration for the last).
public func timeMossTurns(_ turns: [MossTurn], duration: Double) -> [MossTurn] {
    var out = turns
    for i in out.indices {
        if out[i].start == nil { out[i].start = i > 0 ? out[i - 1].end ?? out[i - 1].start : 0 }
    }
    for i in out.indices {
        if out[i].end == nil {
            out[i].end = i + 1 < out.count ? out[i + 1].start : duration
        }
        let s = min(max(out[i].start ?? 0, 0), duration)
        out[i].start = s
        out[i].end = min(max(out[i].end ?? s, s), max(duration, s))
    }
    return out
}

/// A turn's text without events ("[laughter]") and stray tags, with runs
/// of spaces collapsed: "我们[laughter]走吧" → "我们走吧".
public func cleanMossText(_ text: String) -> String {
    // Each removed tag leaves a mark: nothing next to Chinese or Japanese
    // characters and their punctuation, a space elsewhere.
    let mark: Character = "\u{1}"
    var s = text
    for p in [mossEventPattern, mossTagPattern] {
        let ns = s as NSString
        s = p.stringByReplacingMatches(in: s, range: NSRange(location: 0, length: ns.length), withTemplate: String(mark))
    }
    let chars = Array(s)
    var spaced = ""
    for (i, c) in chars.enumerated() {
        guard c == mark else {
            spaced.append(c)
            continue
        }
        let before = chars[..<i].last { $0 != mark && !$0.isWhitespace }
        let after = chars[(i + 1)...].first { $0 != mark && !$0.isWhitespace }
        if let before, let after, !isUnspaced(before), !isUnspaced(after) {
            spaced.append(" ")
        }
    }
    return spaced.split(whereSeparator: { $0 == " " || $0 == "\t" || $0 == "\n" || $0 == "\r" }).joined(separator: " ")
}

/// The model card's instruction (Chinese: timestamped segments that start
/// with a speaker label); a prompt (vocabulary: terms separated by commas,
/// 、, semicolons or new lines) is added as its hotword hint.
public let mossInstruction = "请将音频转写为文本，每一段需以起始时间戳和说话人编号（[S01]、[S02]、[S03]…）开头，正文为对应的语音内容，并在段末标注结束时间戳，以清晰标明该段语音范围。"

public func mossInstruction(prompt: String?) -> String {
    let terms = mossHotwords(prompt)
    guard !terms.isEmpty else { return mossInstruction }
    return mossInstruction + "热词提示：" + terms.joined(separator: ", ")
}

/// The terms of a vocabulary prompt, in order, without repeats.
public func mossHotwords(_ prompt: String?) -> [String] {
    guard let prompt else { return [] }
    var out: [String] = []
    for t in prompt.components(separatedBy: CharacterSet(charactersIn: "\n,，、;；")) {
        let term = t.trimmingCharacters(in: .whitespaces)
        if !term.isEmpty, !out.contains(term) { out.append(term) }
    }
    return out
}

/// Han, kana, CJK and full-width punctuation: written without spaces.
func isUnspaced(_ c: Character) -> Bool {
    c.unicodeScalars.contains { (0x3000 ... 0x9FFF).contains($0.value) || (0xF900 ... 0xFAFF).contains($0.value) || (0xFF00 ... 0xFFEF).contains($0.value) }
}

/// Turn texts as one transcript: no space next to Chinese or Japanese
/// characters and their punctuation, one between others (Korean too).
public func joinMossTexts(_ texts: [String]) -> String {
    var out = ""
    for t in texts where !t.isEmpty {
        if let last = out.last, let first = t.first, !(isUnspaced(last) || isUnspaced(first)) {
            out += " "
        }
        out += t
    }
    return out
}

/// Chinese, Japanese or Korean by script: Han and kana, or Hangul, make up
/// at least 30 % of the text (a run of other letters counting once, so
/// Chinese with English terms stays Chinese); nil otherwise (the caller
/// asks a language recogniser).
public func cjkLanguage(_ text: String) -> String? {
    var han = 0, kana = 0, hangul = 0, words = 0, inWord = false
    for u in text.unicodeScalars {
        switch u.value {
        case 0x3400 ... 0x9FFF, 0xF900 ... 0xFAFF: han += 1; inWord = false
        case 0x3040 ... 0x30FF: kana += 1; inWord = false
        case 0xAC00 ... 0xD7AF: hangul += 1; inWord = false
        default:
            let letter = CharacterSet.letters.contains(u)
            if letter, !inWord { words += 1 }
            inWord = letter
        }
    }
    let units = Double(max(han + kana + hangul + words, 1))
    if Double(han + kana) / units >= 0.3 { return kana > 0 && Double(kana) / Double(han + kana) >= 0.1 ? "ja" : "zh" }
    if Double(hangul) / units >= 0.3 { return "ko" }
    return nil
}
