import Foundation

// A constrained reply caught in a loop: greedy decoding can repeat the
// same words inside a JSON string until the budget runs out (Qwen3 4B, a
// glossary note: ".) 1 time in transcript, likely a brand or show title,
// so kept as is" over and over, 2,048 tokens and two minutes, then the
// same again on each repair turn). The grammar cannot end a string the
// model keeps writing, and the engine no longer pushes a long reply to
// close (LLMGuided.swift), so it ends one that loops: finish "length",
// which the app repairs (a sampled turn, internal/llm).
public enum ReplyLoop {
    /// Whether tokens end with the same block of minBlock…maxBlock tokens
    /// repeated `times` times in a row. Real JSON rarely does: array items
    /// differ in their ids and numbers, and runs of whitespace are single
    /// tokens (held back apart).
    public static func ends(_ tokens: [Int], minBlock: Int = 8, maxBlock: Int = 64, times: Int = 3) -> Bool {
        let n = tokens.count
        guard times >= 2, minBlock >= 1 else { return false }
        var size = minBlock
        while size <= maxBlock && size * times <= n {
            var same = true
            check: for k in 1..<times {
                let a = n - size, b = n - size * (k + 1)
                for i in 0..<size where tokens[a + i] != tokens[b + i] {
                    same = false
                    break check
                }
            }
            if same { return true }
            size += 1
        }
        return false
    }
}
