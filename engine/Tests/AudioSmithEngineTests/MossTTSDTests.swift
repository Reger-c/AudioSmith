import Foundation
import XCTest
@testable import AudioSmithDialogue

/// MOSS-TTSD's text, prompt rows, generation state and sampler (thread #82)
/// against the official code: mossttsd-text.json comes from the TTSD Space's
/// own functions (clis/moss_ttsd_app.py @ 934d682), mossttsd-sampler.json
/// from the model repo's inference_utils.py (scripts/mossttsd/reference.py cases).
final class MossTTSDTests: XCTestCase {
    static func fixture(_ name: String) throws -> Data {
        try Data(contentsOf: URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Fixtures/\(name)"))
    }

    struct TextCases: Decodable {
        struct Normalize: Decodable { var `in`: String; var out: String }
        struct Recipe: Decodable { var prompts: [String]; var dialogue: String; var out: String }
        var normalize: [Normalize]
        var recipe: [Recipe]
    }

    func testNormalizeMatchesTheSpace() throws {
        let cases = try JSONDecoder().decode(TextCases.self, from: Self.fixture("mossttsd-text.json"))
        XCTAssertGreaterThan(cases.normalize.count, 5)
        for c in cases.normalize {
            XCTAssertEqual(MossTTSDText.normalize(c.in), c.out, c.in)
        }
        for c in cases.recipe {
            // The dialogue as turns: split at its tags.
            var turns: [(speaker: Int, text: String)] = []
            let re = try NSRegularExpression(pattern: #"\[S(\d+)\]([^\[]*)"#)
            let ns = c.dialogue as NSString
            for m in re.matches(in: c.dialogue, range: NSRange(location: 0, length: ns.length)) {
                turns.append((Int(ns.substring(with: m.range(at: 1)))!, ns.substring(with: m.range(at: 2))))
            }
            XCTAssertEqual(MossTTSDText.prompt(references: c.prompts, turns: turns), c.out)
        }
        XCTAssertEqual(MossTTSDText.turnText("你好……世界"), "你好，世界")
        XCTAssertEqual(MossTTSDText.promptText("[S2]好的", speaker: 2), "[S2]好的")
        XCTAssertEqual(MossTTSDText.promptText(" 好的 ", speaker: 1), "[S1] 好的")
    }

    func testDelayPattern() {
        let codes = (0 ..< 3).map { t in (0 ..< 16).map { t * 100 + $0 } }
        let d = MossTTSDPrompt.delay(codes)
        XCTAssertEqual(d.count, 18)
        XCTAssertEqual(d[0][0], 0)
        XCTAssertEqual(d[0][1], 1024)
        XCTAssertEqual(d[2][2], 2)
        XCTAssertEqual(d[17][15], 215)
        XCTAssertEqual(MossTTSDPrompt.undelay(d), codes)
        // The first clip of an assistant part: audio start (all 1024), the clip, then a gap.
        var rows = [[MossTTSDTokens.audioStart] + Array(repeating: 1024, count: 16)]
        rows += d.map { [MossTTSDTokens.genSlot] + $0 }
        rows += Array(repeating: [MossTTSDTokens.textPad] + Array(repeating: 1024, count: 16), count: 16)
        XCTAssertEqual(MossTTSDPrompt.firstClip(rows), codes)
    }

    func testPromptRows() {
        // One id per character (a stand-in tokenizer).
        let encode: (String) -> [Int] = { $0.unicodeScalars.map { Int($0.value) } }
        let ref = [[Int]](repeating: Array(0 ..< 16), count: 4)
        let prompt = [[Int]](repeating: Array(100 ..< 116), count: 5)
        let rows = MossTTSDPrompt.rows(text: "T", references: [ref, ref], prompt: prompt, encode: encode)
        XCTAssertTrue(rows.allSatisfy { $0.count == 17 })
        XCTAssertEqual(rows[0][0], MossTTSDTokens.imStart)
        // Two user clips of 4 frames: audio start, 4 + 15 slots, audio end each.
        XCTAssertEqual(rows.filter { $0[0] == MossTTSDTokens.userSlot }.count, 2 * 19)
        XCTAssertEqual(rows.filter { $0[0] == MossTTSDTokens.audioEnd }.count, 2)
        // The assistant: im_start, "assistant\n", audio start, 5 gen slots (truncated: no delay slots).
        let tail = Array(rows.suffix(6))
        XCTAssertEqual(tail[0][0], MossTTSDTokens.audioStart)
        XCTAssertEqual(tail.dropFirst().map { $0[0] }, Array(repeating: MossTTSDTokens.genSlot, count: 5))
        XCTAssertEqual(rows.suffix(5).map { $0[1] }, Array(repeating: 100, count: 5))
        // Codebook 15 of the last gen slot row is still 1024 (the cut delay tail).
        XCTAssertEqual(rows.last![16], 1024)
        let text = String(String.UnicodeScalarView(rows.map { $0[0] }.filter { $0 < 128 }.compactMap { Unicode.Scalar($0) }))
        XCTAssertTrue(text.contains("user\n<user_inst>\n- Reference(s):\n[S1]:\n"))
        XCTAssertTrue(text.contains("\n[S2]:\n"))
        XCTAssertTrue(text.contains("- Scene:\nNone\n- Text:\nT\n</user_inst>"))
        XCTAssertTrue(text.hasSuffix("assistant\n"))
    }

    func testStateMachine() {
        // Continuation: the prompt ends inside audio.
        var s = MossTTSDState(promptText: [1, 2, MossTTSDTokens.audioStart] + Array(repeating: MossTTSDTokens.genSlot, count: 20))
        XCTAssertEqual(s.text, .sample(inAudio: true, noDelay: true, noEnd: true))
        XCTAssertEqual(s.channels, Array(repeating: true, count: 16))
        s.advance(text: MossTTSDTokens.genSlot)
        XCTAssertEqual(s.text, .sample(inAudio: true, noDelay: false, noEnd: true))
        s.advance(text: MossTTSDTokens.delaySlot) // the model ends the audio
        for k in 1 ..< 16 {
            XCTAssertEqual(s.text, .forced(MossTTSDTokens.delaySlot))
            XCTAssertEqual(s.channels.filter { $0 }.count, 16 - k, "delay \(k)")
            XCTAssertFalse(s.channels[k - 1])
            s.advance(text: MossTTSDTokens.delaySlot)
        }
        XCTAssertEqual(s.text, .forced(MossTTSDTokens.audioEnd))
        XCTAssertEqual(s.channels, Array(repeating: false, count: 16))
        s.advance(text: MossTTSDTokens.audioEnd)
        XCTAssertFalse(s.inAudio)
        XCTAssertEqual(s.text, .sample(inAudio: false, noDelay: false, noEnd: false))
        XCTAssertEqual(s.channels, Array(repeating: false, count: 16))
        s.advance(text: MossTTSDTokens.imEnd)
        XCTAssertTrue(s.stopping)
        XCTAssertEqual(s.text, .forced(MossTTSDTokens.textPad))
        // A fresh audio start: codebook i starts i rows later.
        var g = MossTTSDState(promptText: [1, 2, 3])
        XCTAssertEqual(g.channels.filter { $0 }.count, 0)
        g.advance(text: MossTTSDTokens.audioStart)
        XCTAssertTrue(g.inAudio)
        XCTAssertEqual(g.channels.filter { $0 }.count, 1)
        g.advance(text: MossTTSDTokens.genSlot)
        XCTAssertEqual(g.channels.filter { $0 }.count, 2)
    }

    struct SamplerCases: Decodable {
        struct Case: Decodable {
            var kind: String
            var logits: [Float]
            var rows: Int
            var temperature: Float
            var seen: [Int]?
            var penalty: Float?
            var exclude: [Int]?
            var keep: [Int]?
            var topK: Int
            var topP: Float
            var expected: [[String: Double]]
        }

        var cases: [Case]
    }

    func testSamplerMatchesUpstream() throws {
        let cases = try JSONDecoder().decode(SamplerCases.self, from: Self.fixture("mossttsd-sampler.json")).cases
        XCTAssertEqual(cases.count, 6)
        for (n, c) in cases.enumerated() {
            var cfg = MossTTSDSampling()
            cfg.audioTemperature = c.temperature
            cfg.textTemperature = c.temperature
            cfg.audioTopK = c.topK
            cfg.textTopK = c.topK
            cfg.audioTopP = c.topP
            cfg.textTopP = c.topP
            cfg.audioRepetitionPenalty = c.penalty ?? 1
            let sampler = MossTTSDSampler(config: cfg, seed: 1)
            let cols = c.logits.count / c.rows
            var seen = [Bool](repeating: false, count: cols)
            for t in c.seen ?? [] where t < cols { seen[t] = true }
            for r in 0 ..< c.rows {
                let logits = Array(c.logits[(r * cols) ..< ((r + 1) * cols)])
                let processed: [Float]
                if c.kind == "audio" {
                    processed = sampler.processAudio(logits, seen: seen)
                } else if let keep = c.keep {
                    processed = sampler.processText(logits) { keep.contains($0) }
                } else {
                    let ex = Set(c.exclude ?? [])
                    processed = sampler.processText(logits) { !ex.contains($0) }
                }
                let probs = softmax(processed)
                let want = c.expected[r]
                XCTAssertEqual(probs.filter { $0 > 0 }.count, want.count, "case \(n) row \(r): kept")
                for (k, v) in want {
                    XCTAssertEqual(probs[Int(k)!], v, accuracy: 2e-5, "case \(n) row \(r) id \(k)")
                }
            }
        }
    }

    func testGreedyAndDraws() {
        var cfg = MossTTSDSampling()
        cfg.audioTemperature = 0
        var s = MossTTSDSampler(config: cfg, seed: 3)
        var logits = [Float](repeating: 0, count: 1025)
        logits[7] = 2
        logits[9] = 1.9
        logits[1024] = 50 // never: the empty code
        XCTAssertEqual(s.sampleAudio(logits, seen: [Bool](repeating: false, count: 1025)), 7)
        var seen = [Bool](repeating: false, count: 1025)
        seen[7] = true // 2 / 1.1 < 1.9
        XCTAssertEqual(s.sampleAudio(logits, seen: seen), 9)
        var a = MossTTSDSampler(seed: 5), b = MossTTSDSampler(seed: 5)
        let flat = (0 ..< 1025).map { Float($0 % 17) * 0.2 }
        for _ in 0 ..< 20 {
            XCTAssertEqual(a.sampleAudio(flat, seen: seen), b.sampleAudio(flat, seen: seen))
        }
    }
}
