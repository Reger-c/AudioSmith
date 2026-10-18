import Foundation

// MOSS-TTSD's text (thread #82), without MLX so the tests can check it
// against the official code: the TTSD Space's normalize_text and its
// clone-and-continuation recipe (OpenMOSS/MOSS-TTS clis/moss_ttsd_app.py
// @ 934d682), which the model card recommends always using:
//
//   - each `[Sn]` segment (`[n]` is read as `[Sn]`; newlines are spaces):
//     brackets, quotes, `-`, `_` and tildes go; `哈哈…` becomes `[笑]`, an
//     English "haha…" `[laugh]`; dashes and ellipses become `，`; `；` `;`
//     `：` `:` `、` become commas; a run of sentence marks keeps its first;
//     inside the segment `。` becomes `，`, and a trailing comma a full stop;
//   - consecutive segments of one speaker are merged;
//   - the prompt: every speaker's reference transcript as `[Sn] text`, in
//     speaker order, then the dialogue; merged and normalized again.
//
// Python's `len`, slices and `str.strip` count code points, so this does
// too (Unicode scalars, not Characters).

public enum MossTTSDText {
    static let removed: Set<Unicode.Scalar> = Set("【】《》（）『』「」\"-_“”～~‘’".unicodeScalars)
    static let commaLike: [String: String] = ["——": "，", "……": "，", "...": "，", "⸺": "，", "―": "，", "—": "，", "…": "，"]
    static let internalPunct: [Unicode.Scalar: Unicode.Scalar] = ["；": "，", ";": ",", "：": "，", ":": ",", "、": "，"]
    static let sentenceMarks: Set<Unicode.Scalar> = Set("，。？！,.?!".unicodeScalars)

    /// normalize_text.
    public static func normalize(_ input: String) -> String {
        var text = replace(input, #"\[(\d+)\]"#, with: "[S$1]")
        text = text.replacingOccurrences(of: "\n", with: " ")
        var parts: [(tag: String, content: String)] = []
        for seg in splitBeforeTags(text) {
            let s = strip(seg)
            guard !s.isEmpty else { continue }
            var tag = "", content = s
            if let m = firstMatch(s, #"^(\[S\d+\])\s*(.*)"#, dotAll: false) {
                tag = m[1]
                content = m[2]
            }
            parts.append((tag, normalizeContent(content)))
        }
        guard !parts.isEmpty else { return "" }
        var lines: [String] = []
        var tag = parts[0].tag
        var content = [parts[0].content]
        for p in parts.dropFirst() {
            if p.tag == tag, !tag.isEmpty {
                content.append(p.content)
            } else {
                lines.append(strip(tag + content.joined()))
                tag = p.tag
                content = [p.content]
            }
        }
        lines.append(strip(tag + content.joined()))
        return lines.joined().replacingOccurrences(of: "‘", with: "'").replacingOccurrences(of: "’", with: "'")
    }

    /// One segment's content (after its tag).
    static func normalizeContent(_ input: String) -> String {
        var s = String(String.UnicodeScalarView(input.unicodeScalars.filter { !removed.contains($0) }))
        s = replace(s, "哈{2,}", with: "[笑]")
        s = replace(s, #"\b(ha(\s*ha)+)\b"#, with: "[laugh]", caseInsensitive: true)
        for k in ["——", "……", "...", "⸺", "―", "—", "…"] {
            s = s.replacingOccurrences(of: k, with: commaLike[k]!)
        }
        s = String(String.UnicodeScalarView(s.unicodeScalars.map { internalPunct[$0] ?? $0 }))
        s = strip(s)
        s = replace(s, "([，。？！,.?!])[，。？！,.?!]+", with: "$1")
        let u = Array(s.unicodeScalars)
        if u.count > 1 {
            let last = u[u.count - 1]
            let end: Unicode.Scalar = last == "，" ? "。" : (last == "," ? "." : last)
            let body = String(String.UnicodeScalarView(u.dropLast())).replacingOccurrences(of: "。", with: "，")
            s = body + String(end)
        }
        return s
    }

    /// _merge_consecutive_speaker_tags.
    public static func mergeTags(_ text: String) -> String {
        var out: [String] = []
        var current: String?
        for seg in splitBeforeTags(text) {
            let s = strip(seg)
            guard !s.isEmpty else { continue }
            guard let m = firstMatch(s, #"^(\[S\d+\])\s*(.*)"#, dotAll: true) else {
                out.append(s)
                continue
            }
            if m[1] == current {
                out.append(m[2])
            } else {
                current = m[1]
                out.append(m[1] + m[2])
            }
        }
        return out.joined()
    }

    /// _normalize_prompt_text: a reference transcript with its speaker tag.
    public static func promptText(_ text: String, speaker: Int) -> String {
        let t = strip(text)
        let tag = "[S\(speaker)]"
        return t.hasPrefix(tag) ? t : "\(tag) \(t)"
    }

    /// The prompt's text: the speakers' transcripts (speaker 1 first), then
    /// the turns (speaker numbers from 1), as the Space builds it.
    public static func prompt(references: [String], turns: [(speaker: Int, text: String)]) -> String {
        let dialogue = normalize(turns.map { "[S\($0.speaker)]\($0.text)" }.joined())
        let prefix = references.enumerated().map { promptText($0.element, speaker: $0.offset + 1) }.joined()
        return normalize(mergeTags(prefix + dialogue))
    }

    /// A turn's text as the model reads it (its segment, normalized alone).
    public static func turnText(_ text: String) -> String {
        normalizeContent(strip(text.replacingOccurrences(of: "\n", with: " ")))
    }

    // MARK: Python-like helpers

    /// str.strip(): Unicode whitespace at both ends.
    static func strip(_ s: String) -> String {
        let u = s.unicodeScalars
        guard let a = u.firstIndex(where: { !$0.properties.isWhitespace }) else { return "" }
        let b = u.lastIndex(where: { !$0.properties.isWhitespace })!
        return String(u[a ... b])
    }

    /// re.split(r"(?=\[S\d+\])", s).
    static func splitBeforeTags(_ s: String) -> [String] {
        let re = try! NSRegularExpression(pattern: #"\[S\d+\]"#)
        let ns = s as NSString
        var cuts = re.matches(in: s, range: NSRange(location: 0, length: ns.length)).map { $0.range.location }
        if cuts.first != 0 { cuts.insert(0, at: 0) }
        var out: [String] = []
        for (i, c) in cuts.enumerated() {
            let end = i + 1 < cuts.count ? cuts[i + 1] : ns.length
            out.append(ns.substring(with: NSRange(location: c, length: end - c)))
        }
        return out
    }

    static func replace(_ s: String, _ pattern: String, with template: String, caseInsensitive: Bool = false) -> String {
        let re = try! NSRegularExpression(pattern: pattern, options: caseInsensitive ? [.caseInsensitive] : [])
        return re.stringByReplacingMatches(in: s, range: NSRange(location: 0, length: (s as NSString).length), withTemplate: template)
    }

    static func firstMatch(_ s: String, _ pattern: String, dotAll: Bool) -> [String]? {
        let re = try! NSRegularExpression(pattern: pattern, options: dotAll ? [.dotMatchesLineSeparators] : [])
        let ns = s as NSString
        guard let m = re.firstMatch(in: s, range: NSRange(location: 0, length: ns.length)) else { return nil }
        return (0 ..< m.numberOfRanges).map { m.range(at: $0).location == NSNotFound ? "" : ns.substring(with: m.range(at: $0)) }
    }
}
