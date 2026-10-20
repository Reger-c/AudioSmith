import Foundation
@preconcurrency import MLX
import MLXGuidedGeneration
import MLXLMCommon
import AudioSmithText

// Constrained decoding for llm.generate with json/schema: xgrammar's
// matcher (MLXGuidedGeneration's GrammarConstraint, with fast-forward)
// over the model's logits, in the engine's own token loop. It is modelled
// on MLXGuidedGeneration's GuidedGenerationLoop (mlx-swift-lm 3.32.3, MIT),
// which broke long replies. (This loop was first written by the same
// author for a sibling app, on Gemma 4 E4B; e2e_llm_structured_test.go
// measured it here on Qwen3 4B.)
//   - its "closing zones" bias the last completionReserve / hardReserve
//     tokens of the budget towards `"`, `}`, `]` and the digits — the hard
//     zone allows nothing else. The reserve is the shortest valid value of
//     the schema and grows with it; the soft zone is the last quarter of
//     the budget. The story director closed its "lines" a line early
//     (15 of 16, 11 of 12); on Gemma long replies became `…}1234567891011…`;
//   - it stopped at any of the model's stop tokens, which xgrammar took for
//     text (GrammarVocab): an end-of-turn token inside a string ended the
//     reply halfway;
//   - a token sampled before a fast-forward run never went through the
//     model: only the forced tokens did, so the model read its reply with
//     those tokens missing. The emotion director's reply turned into
//     `"emotion": "surprisedn”: 2note”: "` (its closing quotes missing, it
//     wrote its own), 6 of 12 lines usable;
//   - it samples greedily only.
// Here nothing is biased but runs of whitespace; a reply that needs more
// than the budget ends with finish "length" (the app asks again), and so
// does one that repeats itself (ReplyLoop); the stop tokens are allowed
// only where the grammar may end and the special tokens never; every token
// of the reply goes through the model (the context is the reply's text, as
// in TokenIterator); the sampler is the request's (greedy unless
// sampleConstrained). Forced text (keys) comes from xgrammar's
// jump-forward string in the tokenizer's own tokens: the same tokens the
// model chose itself, a mask computation fewer each.
//
// The prompt ends with a ```json line (LLM.swift): kept from writing its
// fence by the grammar, a model writes compact JSON, and worse in it
// (Qwen3 4B: a glossary note that never closed, `.”, "` and blank lines
// inside it; Gemma 4: wrong readings, fewer grammar points).

extension LocalLLM {
    /// A constrained reply: the tokens it took ("stop": the value is
    /// complete or emit stopped it; "length": the budget ran out) and a
    /// line for AUDIOSMITH_LLM_TRACE. emit gets the text as it comes and
    /// returns false to stop.
    func guidedReply(input: LMInput, constraint: GrammarConstraint, grammar: GrammarTokenizer, maxTokens: Int,
                     sampler: LogitSampler, steps: ((String) -> Void)? = nil,
                     emit: (String) -> Bool) throws -> (tokens: Int, finish: String, note: String) {
        let model = context.model
        let began = Date()
        let ms = { Int(Date().timeIntervalSince(began) * 1000) }
        let cache = try model.newCache(parameters: nil)
        var state: LMOutput.State?
        var logits: MLXArray
        switch try model.prepare(input, cache: cache, state: nil, prefill: PrefillParameters()) {
        case .tokens(let rest):
            let r = model(rest[text: .newAxis], cache: cache.isEmpty ? nil : cache, state: nil)
            state = r.state
            logits = r.logits
        case .logits(let r):
            state = r.state
            logits = r.logits
        }

        let eos = context.tokenizer.eosTokenId ?? 0
        let stops = stopTokenIDs
        let stopList = stops.sorted()
        let dim = logits.dim(-1)
        let ws = grammarWhitespace()
        let wsBias = Self.fit(ws.bias, dim)
        var tracker = WhitespaceRunTracker(whitespaceTokenIDs: ws.ids)
        var detokenizer = NaiveStreamingDetokenizer(tokenizer: context.tokenizer)
        var count = 0, forced = 0, held = 0
        var finish = "length"
        var reply: [Int] = []
        var looped = false

        /// Appends a token of the reply; false: emit stopped it.
        func take(_ id: Int) -> Bool {
            count += 1
            reply.append(id)
            detokenizer.append(token: id)
            if let piece = detokenizer.next() { return emit(piece) }
            return true
        }

        var mask = try constraint.computeMask()
        decoding: while count < maxTokens {
            if Task.isCancelled { break }
            if mask.isTerminated {
                finish = "stop"
                break
            }
            let add: [Float] = mask.mask.withUnsafeBufferPointer { buf in
                GrammarVocab.additiveMask(mask.needsApply ? buf : nil, grammarVocab: grammar.vocabSize, logits: dim,
                                          eos: eos, stops: stopList)
            }
            var masked = logits[0..., -1, 0...] + MLXArray(add)
            if tracker.isActive {
                masked = masked + wsBias
                held += 1
            }
            let id = sampler.sample(logits: masked).item(Int.self)
            if let steps {
                let raw = argMax(logits[0..., -1, 0...], axis: -1).item(Int.self)
                let text = { (t: Int) in self.context.tokenizer.decode(tokenIds: [t], skipSpecialTokens: false).debugDescription }
                steps("\(count) +\(ms())ms: \(text(id))" + (raw == id ? "" : " (model: \(text(raw)))"))
            }
            _ = tracker.record(tokenID: id)
            if stops.contains(id) {
                // The mask allows a stop token only where the value may end.
                finish = "stop"
                break
            }
            let commit = try constraint.commitToken(Int32(id))
            var feed = [Int32(id)]
            if !take(id) {
                finish = "stop"
                break
            }
            if commit.isTerminated {
                finish = "stop"
                break
            }
            if let steps, !commit.tokens.isEmpty {
                steps("  forced " + context.tokenizer.decode(tokenIds: commit.tokens.map { Int($0) }, skipSpecialTokens: false).debugDescription)
            }
            for t in commit.tokens {
                if count >= maxTokens { break decoding }
                feed.append(t)
                forced += 1
                if !take(Int(t)) {
                    finish = "stop"
                    break decoding
                }
            }
            // A reply that repeats itself ends here, cut off (ReplyLoop).
            if ReplyLoop.ends(reply) {
                looped = true
                break
            }
            // One token at a time (as GuidedGenerationLoop: several at once
            // with a filled cache hit an MLX attention-mask bug on Gemma).
            for t in feed {
                let r = model(LMInput.Text(tokens: MLXArray([t]))[text: .newAxis], cache: cache, state: state)
                state = r.state
                logits = r.logits
            }
            asyncEval(logits)
            mask = try constraint.computeMask()
            eval(logits)
        }
        let note = "tokens \(count) (forced \(forced)) finish \(finish) whitespaceHeld \(held)" + (looped ? " looped" : "") + " in \(ms()) ms"
        return (count, finish, note)
    }

    /// bias (over the tokenizer's vocabulary) fitted to n logits.
    static func fit(_ bias: MLXArray, _ n: Int) -> MLXArray {
        let k = bias.dim(0)
        if k == n { return bias }
        if k > n { return bias[0 ..< n] }
        return concatenated([bias, MLXArray.zeros([n - k])])
    }
}
