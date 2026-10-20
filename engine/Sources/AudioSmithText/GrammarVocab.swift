import Foundation

// What the JSON grammar (xgrammar) is told about a model's vocabulary, as
// pure logic the tests check without MLX (LLMGuided.swift uses it).
//
// xgrammar treats only empty token strings as special: every other token
// is text it may allow inside a JSON string. A chat model's control
// tokens are not text — Gemma 4's "<turn|>" (one of its end-of-reply
// tokens), "<|tool_response>", "<|\"|>"… — but their pieces are non-empty,
// so the grammar allowed them mid-string, and an end-of-turn token there
// ended the reply halfway (`{"text": "` and nothing after). Blanking them
// keeps the grammar from ever choosing one; the engine itself allows the
// stop tokens, and only where the grammar may end.
public enum GrammarVocab {
    /// The ids of a Hugging Face tokenizer.json's added tokens marked
    /// special (nil: not a tokenizer.json it can read).
    public static func specialTokenIDs(tokenizerJSON data: Data) -> Set<Int>? {
        guard let obj = try? JSONSerialization.jsonObject(with: data) as? [String: Any],
              let added = obj["added_tokens"] as? [[String: Any]] else { return nil }
        var ids = Set<Int>()
        for t in added where (t["special"] as? Bool) == true {
            if let id = t["id"] as? Int { ids.insert(id) }
        }
        return ids
    }

    /// vocab with the tokens of blank emptied, except keep (the grammar's
    /// own stop token, which it allows once the value is complete).
    public static func blanked(_ vocab: [String], _ blank: Set<Int>, keep: Int) -> [String] {
        var out = vocab
        for id in blank where id != keep && id >= 0 && id < out.count {
            out[id] = ""
        }
        return out
    }

    /// Whether bit i of an LSB-first bitmask of 32-bit words is set
    /// (xgrammar's token mask).
    public static func allows(_ mask: UnsafeBufferPointer<Int32>, _ i: Int) -> Bool {
        let w = i >> 5
        guard i >= 0, w < mask.count else { return false }
        return (UInt32(bitPattern: mask[w]) >> UInt32(i & 31)) & 1 == 1
    }

    /// The additive mask over a model's logits (0 allowed, -inf not) for
    /// xgrammar's bitmask over its first grammarVocab tokens; logits past
    /// those (padding) are not allowed. The stop tokens are allowed exactly
    /// when the grammar's own stop token (eos) is: where the value may end.
    public static func additiveMask(_ mask: UnsafeBufferPointer<Int32>?, grammarVocab: Int, logits: Int,
                                    eos: Int, stops: [Int]) -> [Float] {
        let n = min(grammarVocab, logits)
        guard let mask else {
            // No exclusions (xgrammar's needsApply false).
            var out = [Float](repeating: 0, count: logits)
            for i in n..<logits { out[i] = -.infinity }
            return out
        }
        var out = [Float](repeating: -.infinity, count: logits)
        var w = 0
        while w * 32 < n && w < mask.count {
            let bits = UInt32(bitPattern: mask[w])
            if bits != 0 {
                let base = w * 32
                for b in 0..<32 where (bits >> UInt32(b)) & 1 == 1 {
                    let i = base + b
                    if i < n { out[i] = 0 }
                }
            }
            w += 1
        }
        let end: Float = (eos >= 0 && eos < logits && out[eos] == 0) ? 0 : -.infinity
        for s in stops where s >= 0 && s < logits {
            out[s] = end
        }
        return out
    }
}
