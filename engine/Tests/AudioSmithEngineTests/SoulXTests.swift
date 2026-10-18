import Foundation
import XCTest
@testable import AudioSmithDialogue

/// SoulX-Podcast's prompts, history window and sampler (thread #80) against
/// upstream's code: the sampler cases come from transformers 4.57.1's own
/// processors (scripts/soulx/reference.py --sampler-json).
final class SoulXTests: XCTestCase {
    func testNormalize() {
        XCTAssertEqual(SoulXPrompt.normalize("你好 世界"), "你好世界。")
        XCTAssertEqual(SoulXPrompt.normalize("我 love 你"), "我love你。")
        XCTAssertEqual(SoulXPrompt.normalize("Hello world"), "Hello world.")
        XCTAssertEqual(SoulXPrompt.normalize("好的！"), "好的！")
        XCTAssertEqual(SoulXPrompt.normalize("真的吗?"), "真的吗?")
        // re.sub replaces non-overlapping matches once, as upstream does.
        XCTAssertEqual(SoulXPrompt.normalize("我 你 他"), "我你 他。")
        // Tags stay (the community port dropped them).
        XCTAssertEqual(SoulXPrompt.normalize("<|laughter|>真好笑"), "<|laughter|>真好笑。")
    }

    func testPromptTexts() {
        XCTAssertEqual(SoulXPrompt.reference(speaker: 0, text: "你好"),
                       "<|task_podcast|><|SPEAKER_0|><|text_start|>你好。<|text_end|><|semantic_token_start|>")
        XCTAssertEqual(SoulXPrompt.reference(speaker: 1, text: "嗯"),
                       "<|SPEAKER_1|><|text_start|>嗯。<|text_end|><|semantic_token_start|>")
        XCTAssertEqual(SoulXPrompt.turn(speaker: 3, text: "好"),
                       "<|SPEAKER_3|><|text_start|>好。<|text_end|><|semantic_token_start|>")
    }

    func testVocabMapping() {
        XCTAssertEqual(SoulXVocab.global(0), 152_927)
        XCTAssertEqual(SoulXVocab.global(SoulXVocab.localEOS), 151_675)
        XCTAssertEqual(SoulXVocab.local(152_927 + 6560), 6560)
        XCTAssertEqual(SoulXVocab.local(151_675), SoulXVocab.localEOS)
        XCTAssertNil(SoulXVocab.local(1000))
        XCTAssertNil(SoulXVocab.local(152_927 + 6561))
    }

    /// forward_longform's window: 2 references, maxTurnSize 10. Before turn
    /// 10 the context holds 11 blocks: it is rebuilt from the references,
    /// the texts of turns 6 and 7 and turns 8 and 9 with their speech.
    func testHistoryWindow() {
        let refs = [[1, 2, 99], [3, 4, 99]]
        var c = SoulXContext(references: refs)
        func text(_ k: Int) -> [Int] { [1000 * k, 1000 * k + 1, 7] } // the last id stands for <|semantic_token_start|>
        func speech(_ k: Int) -> [Int] { [500 + k, 99] }
        for k in 1 ... 9 {
            XCTAssertFalse(c.begin(text: text(k)), "turn \(k)")
            c.end(text: text(k), generated: speech(k))
        }
        XCTAssertEqual(c.inputs.count, 6 + 9 * 5)
        XCTAssertTrue(c.begin(text: text(10)))
        let want = refs[0] + refs[1] + [6000, 6001] + [7000, 7001] + text(8) + speech(8) + text(9) + speech(9) + text(10)
        XCTAssertEqual(c.inputs, want)
        c.end(text: text(10), generated: speech(10))
        XCTAssertEqual(c.rebuilds, 1)
        // valid_turn_size is now 2 + 11 - 7 + 1 = 7: four more turns before the next rebuild.
        for k in 11 ... 14 {
            XCTAssertFalse(c.begin(text: text(k)), "turn \(k)")
            c.end(text: text(k), generated: speech(k))
        }
        XCTAssertTrue(c.begin(text: text(15)))
    }

    func testTokenThresholdRebuilds() {
        var c = SoulXContext(references: [[1, 99], [2, 99]])
        c.turnTokensThreshold = 10
        XCTAssertFalse(c.begin(text: [10, 7]))
        c.end(text: [10, 7], generated: Array(repeating: 500, count: 8) + [99])
        XCTAssertTrue(c.begin(text: [11, 7]))
        // Four references+turn blocks: refs, then the last two prompt blocks (ref 1's and turn 1 with speech).
        XCTAssertEqual(c.inputs, [1, 99, 2, 99, 2, 99, 10, 7] + Array(repeating: 500, count: 8) + [99, 11, 7])
    }

    struct SamplerCases: Decodable {
        struct Case: Decodable {
            var logits: [Float]
            var generated: [Int]
            var expected: [Float?]
        }

        var cases: [Case]
    }

    func testSamplerMatchesTransformers() throws {
        let url = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Fixtures/soulx-sampler.json")
        let cases = try JSONDecoder().decode(SamplerCases.self, from: Data(contentsOf: url)).cases
        XCTAssertEqual(cases.count, 4)
        let sampler = SoulXSampler(seed: 1)
        for (n, c) in cases.enumerated() {
            let got = sampler.process(c.logits, generated: c.generated)
            XCTAssertEqual(got.count, c.expected.count)
            for (i, (g, w)) in zip(got, c.expected).enumerated() {
                if let w {
                    XCTAssertEqual(g, w, accuracy: 1e-4, "case \(n) token \(i)")
                } else {
                    XCTAssertEqual(g, -.infinity, "case \(n) token \(i) should be removed")
                }
            }
        }
    }

    func testSamplerDrawsAndRAS() {
        var logits = [Float](repeating: -5, count: SoulXVocab.localCount)
        logits[5] = 20
        var s = SoulXSampler(seed: 42)
        let speech5 = SoulXVocab.global(5)
        // 5 already 4 times in the last 25 ids: the candidate makes it 5 ≥ 25 × 0.2.
        let t = s.next(logits, window: [1, 2, speech5, speech5, 3, speech5, speech5], generated: [5, 5, 5, 5, 1, 2, 3, 4, 6, 7])
        XCTAssertEqual(t, 5)
        XCTAssertEqual(s.rasFallbacks, 1)
        _ = s.next(logits, window: [speech5], generated: [1, 2, 3, 4, 6, 7, 8, 9])
        XCTAssertEqual(s.rasFallbacks, 1)
        // EOS is masked until 8 tokens are generated.
        var eos = [Float](repeating: -10, count: SoulXVocab.localCount)
        eos[SoulXVocab.localEOS] = 30
        eos[9] = 0
        var e = SoulXSampler(seed: 7)
        XCTAssertEqual(e.next(eos, window: [], generated: [1, 2]), 9)
        XCTAssertEqual(e.next(eos, window: [], generated: Array(0 ..< 8)), SoulXVocab.localEOS)
        // The same seed draws the same tokens.
        var a = SoulXSampler(seed: 9), b = SoulXSampler(seed: 9)
        let flat = (0 ..< SoulXVocab.localCount).map { Float($0 % 13) * 0.3 }
        for _ in 0 ..< 20 {
            XCTAssertEqual(a.next(flat, window: [], generated: [1]), b.next(flat, window: [], generated: [1]))
        }
    }
}
