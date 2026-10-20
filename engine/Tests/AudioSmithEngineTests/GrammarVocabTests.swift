import Foundation
import AudioSmithProtocol
import AudioSmithText
import XCTest

/// What the JSON grammar is told about the vocabulary (AudioSmithText/GrammarVocab.swift).
final class GrammarVocabTests: XCTestCase {
    func testSpecialTokenIDs() {
        // A tokenizer.json's added tokens, as Gemma 4's: special ones and a plain one.
        let json = #"{"added_tokens":[{"id":1,"content":"<eos>","special":true},{"id":106,"content":"<turn|>","special":true},{"id":50,"content":"<|tool_response>","special":true},{"id":7,"content":"<b>","special":false}],"model":{}}"#
        XCTAssertEqual(GrammarVocab.specialTokenIDs(tokenizerJSON: Data(json.utf8)), [1, 50, 106])
        XCTAssertNil(GrammarVocab.specialTokenIDs(tokenizerJSON: Data("[]".utf8)))
    }

    func testBlankedKeepsTheGrammarsStopToken() {
        let vocab = ["<pad>", "<eos>", "a", "<turn|>", "\""]
        XCTAssertEqual(GrammarVocab.blanked(vocab, [0, 1, 3, 99], keep: 1), ["", "<eos>", "a", "", "\""])
    }

    /// The stop tokens follow the grammar's own: allowed where it may end,
    /// never elsewhere, whatever the bitmask says of them.
    func testAdditiveMask() {
        // 40 tokens; the grammar allows 2, 5, 33 and 3 (a stop token it
        // took for text before it was blanked).
        var words: [Int32] = [0, 0]
        for i in [2, 3, 5, 33] { words[i / 32] |= Int32(bitPattern: UInt32(1) << UInt32(i % 32)) }
        let midway = words.withUnsafeBufferPointer {
            GrammarVocab.additiveMask($0, grammarVocab: 40, logits: 42, eos: 1, stops: [1, 3])
        }
        XCTAssertEqual(midway.count, 42)
        XCTAssertEqual(midway.indices.filter { midway[$0] == 0 }, [2, 5, 33])
        // Where it may end (eos 1 allowed): every stop token is.
        words[0] |= 1 << 1
        let end = words.withUnsafeBufferPointer {
            GrammarVocab.additiveMask($0, grammarVocab: 40, logits: 42, eos: 1, stops: [1, 3])
        }
        XCTAssertEqual(end.indices.filter { end[$0] == 0 }, [1, 2, 3, 5, 33])
        // No exclusions: everything but the padding past the vocabulary.
        let open = GrammarVocab.additiveMask(nil, grammarVocab: 40, logits: 42, eos: 1, stops: [1])
        XCTAssertEqual(open.indices.filter { open[$0] != 0 }, [40, 41])
    }

    /// The schema the grammar compiles keeps one key order, whatever the
    /// process (xgrammar writes the reply's keys in it): the required
    /// properties as "required" lists them, then the others by name.
    func testGrammarSchemaOrder() throws {
        let json = #"{"model":"m","messages":[],"json":true,"schema":{"type":"object","required":["scene","lines"],"properties":{"scene":{"type":"string","maxLength":40},"lines":{"type":"array","items":{"type":"object","required":["n","emotion"],"properties":{"note":{"type":"string"},"emotion":{"type":"string"},"n":{"type":"integer","minimum":-0.5}}}},"cast":{"type":"string","description":"a/b \"x\""}},"additionalProperties":false}}"#
        let p = try JSONDecoder().decode(LLMGenerateParams.self, from: Data(json.utf8))
        XCTAssertEqual(p.grammarSchema,
                       #"{"additionalProperties":false,"properties":{"scene":{"maxLength":40,"type":"string"},"lines":{"items":{"properties":{"n":{"minimum":-0.5,"type":"integer"},"emotion":{"type":"string"},"note":{"type":"string"}},"required":["n","emotion"],"type":"object"},"type":"array"},"cast":{"description":"a/b \"x\"","type":"string"}},"required":["scene","lines"],"type":"object"}"#)
        let none = try JSONDecoder().decode(LLMGenerateParams.self, from: Data(#"{"model":"m","messages":[],"json":true}"#.utf8))
        XCTAssertEqual(none.grammarSchema, #"{"type":["object","array"]}"#)
    }
}
