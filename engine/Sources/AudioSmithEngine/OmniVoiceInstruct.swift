import Foundation

// OmniVoice's voice-design instruct, normalised the way its Python
// reference does (k2-fsa/OmniVoice, Apache-2.0: `_resolve_instruct` and
// the attribute table of omnivoice/utils/voice_design.py) before the model
// sees it — the Swift port passes the string through unchanged, but the
// model was trained on the normalised forms:
//   - items are separated by "," or "，" and matched case-insensitively;
//   - a Chinese dialect makes every item Chinese, an English accent every
//     item English; otherwise Chinese synthesis text makes them Chinese;
//   - Chinese items are joined with "，", English ones with ", ".
// Python raises on unknown items and on two items of one category; the
// engine drops them instead (and logs it): the app's voicedesign package
// only emits valid items, and a typo should not fail a take.

private let omniCategories: [[(en: String?, zh: String?)]] = [
    [("male", "男"), ("female", "女")],
    [("child", "儿童"), ("teenager", "少年"), ("young adult", "青年"), ("middle-aged", "中年"), ("elderly", "老年")],
    [("very low pitch", "极低音调"), ("low pitch", "低音调"), ("moderate pitch", "中音调"), ("high pitch", "高音调"), ("very high pitch", "极高音调")],
    [("whisper", "耳语")],
    ["american", "british", "australian", "chinese", "canadian", "indian", "korean", "portuguese", "russian", "japanese"]
        .map { ("\($0) accent", nil) },
    ["河南话", "陕西话", "四川话", "贵州话", "云南话", "桂林话", "济南话", "石家庄话", "甘肃话", "宁夏话", "青岛话", "东北话"]
        .map { (nil, $0) },
]

/// The normalised instruct (nil when nothing is left) and the items dropped.
func omniVoiceInstruct(_ raw: String?, text: String) -> (instruct: String?, dropped: [String]) {
    guard let raw = nonEmpty(raw) else { return (nil, []) }
    let items = raw.split(whereSeparator: { $0 == "," || $0 == "，" })
        .map { $0.trimmingCharacters(in: .whitespaces).lowercased() }
        .filter { !$0.isEmpty }
    var picked: [(cat: Int, entry: (en: String?, zh: String?))] = []
    var dropped: [String] = []
    for item in items {
        var found: (Int, (en: String?, zh: String?))?
        for (c, cat) in omniCategories.enumerated() {
            if let e = cat.first(where: { $0.en == item || $0.zh == item }) {
                found = (c, e)
                break
            }
        }
        guard let (c, e) = found, !picked.contains(where: { $0.cat == c }) else {
            dropped.append(item)
            continue
        }
        picked.append((c, e))
    }
    let hasDialect = picked.contains { $0.cat == 5 }
    let hasAccent = picked.contains { $0.cat == 4 }
    if hasDialect && hasAccent {
        // Dialects are for Chinese speech, accents for English speech.
        let accentFirst = picked.firstIndex { $0.cat == 4 }! < picked.firstIndex { $0.cat == 5 }!
        let drop = accentFirst ? 5 : 4
        dropped += picked.filter { $0.cat == drop }.compactMap { $0.entry.en ?? $0.entry.zh }
        picked.removeAll { $0.cat == drop }
    }
    var zh = containsHan(text)
    if picked.contains(where: { $0.cat == 5 }) { zh = true } else if picked.contains(where: { $0.cat == 4 }) { zh = false }
    let words = picked.map { zh ? ($0.entry.zh ?? $0.entry.en!) : ($0.entry.en ?? $0.entry.zh!) }
    guard !words.isEmpty else { return (nil, dropped) }
    let anyHan = words.contains { containsHan($0) }
    return (words.joined(separator: anyHan ? "，" : ", "), dropped)
}

private func containsHan(_ s: String) -> Bool {
    s.unicodeScalars.contains { (0x4E00 ... 0x9FFF).contains($0.value) }
}
