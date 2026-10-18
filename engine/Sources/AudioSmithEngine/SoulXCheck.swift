import AudioSmithDialogue
import Foundation
@preconcurrency import MLX
import MLXAudioCodecs
import MLXLLM
import MLXLMCommon

// Developer checks of the SoulX-Podcast port (thread #80), run on the Mac by
// scripts/soulx/parity.sh; not used by the app.
//
//   audiosmith-engine --soulx-dump-params out.json
//       the S3Gen module's parameter names and shapes (scripts/soulx/convert.py
//       checks its keys against them);
//   audiosmith-engine --soulx-check fixtures.safetensors MODEL_DIR S3TOKENIZER_DIR [--llm-dir DIR] [--fp32]
//       compares every stage with the official PyTorch reference's outputs
//       (scripts/soulx/reference.py) and exits 1 when one is off. --llm-dir
//       loads another language model (the upstream bfloat16 one, with --fp32
//       cast to float32) for the logits check.

enum SoulXCheck {
    static func dumpParams(_ path: String) throws {
        let flow = CausalMaskedDiffWithXvec(meanflow: false, hiftUseInterpolation: true)
        var shapes: [String: [Int]] = [:]
        for (k, v) in flow.parameters().flattened() { shapes[k] = v.shape }
        let data = try JSONSerialization.data(withJSONObject: shapes, options: [.sortedKeys, .prettyPrinted])
        try data.write(to: URL(fileURLWithPath: path))
        print("wrote \(shapes.count) parameters to \(path)")
    }

    struct Report {
        var failed = 0
        mutating func check(_ name: String, _ ok: Bool, _ detail: String) {
            print(String(format: "%@  %-28@ %@", ok ? "ok  " : "FAIL", name, detail))
            if !ok { failed += 1 }
        }
    }

    static func maxAbs(_ a: MLXArray, _ b: MLXArray) -> Float {
        MLX.abs(a.asType(.float32) - b.asType(.float32)).max().item(Float.self)
    }

    static func correlation(_ a: [Float], _ b: [Float]) -> Double {
        let n = min(a.count, b.count)
        guard n > 1 else { return 0 }
        let x = a.prefix(n).map(Double.init), y = b.prefix(n).map(Double.init)
        let mx = x.reduce(0, +) / Double(n), my = y.reduce(0, +) / Double(n)
        var sxy = 0.0, sxx = 0.0, syy = 0.0
        for i in 0 ..< n {
            sxy += (x[i] - mx) * (y[i] - my)
            sxx += (x[i] - mx) * (x[i] - mx)
            syy += (y[i] - my) * (y[i] - my)
        }
        return sxy / max(sqrt(sxx * syy), 1e-30)
    }

    static func snr(_ ref: [Float], _ got: [Float]) -> Double {
        let n = min(ref.count, got.count)
        var s = 0.0, e = 0.0
        for i in 0 ..< n {
            s += Double(ref[i]) * Double(ref[i])
            e += Double(ref[i] - got[i]) * Double(ref[i] - got[i])
        }
        return 10 * log10(s / max(e, 1e-30))
    }

    static func run(_ args: [String]) async throws -> Int32 {
        guard args.count >= 3 else {
            log("usage: --soulx-check fixtures.safetensors MODEL_DIR S3TOKENIZER_DIR [--llm-dir DIR] [--fp32]")
            return 2
        }
        let (fx, meta) = try MLX.loadArraysAndMetadata(url: URL(fileURLWithPath: args[0]))
        let info = try JSONSerialization.jsonObject(with: Data((meta["info"] ?? "{}").utf8)) as? [String: Any] ?? [:]
        let modelDir = URL(fileURLWithPath: args[1])
        let s3Dir = URL(fileURLWithPath: args[2])
        var llmDir: URL?
        if let i = args.firstIndex(of: "--llm-dir"), i + 1 < args.count { llmDir = URL(fileURLWithPath: args[i + 1]) }
        let fp32 = args.contains("--fp32")
        var r = Report()

        let started = Date()
        let model = try await SoulXModel.load(modelDir, s3Tokenizer: s3Dir, llmDir: llmDir, fp32: fp32)
        print(String(format: "loaded in %.1f s", Date().timeIntervalSince(started)))

        // 1. References: log-mel, S3 tokens, CAM++, the flow's mel.
        var refTokens: [[Int]] = []
        for i in 0 ..< 2 {
            let p = "ref\(i)_"
            guard let a16 = fx[p + "a16"], let a24 = fx[p + "a24"] else { break }
            let logMel = SoulXAudio.tokenizerMel(a16)
            let want = fx[p + "logmel"]!
            r.check("ref\(i) log-mel shape", logMel.shape == want.shape, "\(logMel.shape) vs \(want.shape)")
            if logMel.shape == want.shape {
                r.check("ref\(i) log-mel", maxAbs(logMel, want) < 2e-3, String(format: "max |Δ| %.2e", maxAbs(logMel, want)))
            }
            let wantTokens = fx[p + "tokens"]!.asArray(Int32.self).map { Int($0) }
            func tokens(_ mel: MLXArray) -> [Int] {
                let (c, l) = model.s3Tokenizer.quantize(mel.expandedDimensions(axis: 0), melLen: MLXArray([Int32(mel.dim(1))]))
                return c[0, 0 ..< l.item(Int.self)].asArray(Int32.self).map { Int($0) }
            }
            for (label, t) in [("tokens (ref mel)", tokens(want)), ("tokens (own mel)", tokens(logMel))] {
                let same = zip(t, wantTokens).filter { $0 == $1 }.count
                let frac = Double(same) / Double(max(wantTokens.count, 1))
                r.check("ref\(i) \(label)", t.count == wantTokens.count && frac >= 0.97,
                        String(format: "%d/%d equal (%d vs %d tokens)", same, wantTokens.count, t.count, wantTokens.count))
            }
            refTokens.append(wantTokens)
            let x = model.flow.speakerEncoder.inference([a16]).reshaped(-1)
            let wx = fx[p + "xvector"]!.reshaped(-1)
            let cos = (x * wx).sum().item(Float.self) / (sqrt((x * x).sum().item(Float.self)) * sqrt((wx * wx).sum().item(Float.self)))
            r.check("ref\(i) CAM++ x-vector", cos > 0.999, String(format: "cosine %.6f", cos))
            let mel = SoulXAudio.flowMel(a24)
            let wm = fx[p + "mel24"]!
            r.check("ref\(i) flow mel shape", mel.shape == wm.shape, "\(mel.shape) vs \(wm.shape)")
            if mel.shape == wm.shape {
                r.check("ref\(i) flow mel", maxAbs(mel, wm) < 2e-3, String(format: "max |Δ| %.2e", maxAbs(mel, wm)))
            }
        }

        // 2. The prompt's token ids.
        if let wantIDs = fx["prompt_ids"]?.asArray(Int32.self).map({ Int($0) }),
           let texts = info["ref_texts"] as? [String], let turnText = info["turn_text"] as? String,
           let turnSpeaker = info["turn_speaker"] as? Int {
            var ids: [Int] = []
            for (i, t) in texts.enumerated() {
                ids += model.encode(SoulXPrompt.reference(speaker: i, text: t))
                    + refTokens[i].map { $0 + SoulXVocab.speechOffset } + [SoulXVocab.eos]
            }
            ids += model.encode(SoulXPrompt.turn(speaker: turnSpeaker, text: turnText))
            let firstDiff = zip(ids, wantIDs).enumerated().first { $0.element.0 != $0.element.1 }?.offset
            r.check("prompt token ids", ids == wantIDs, "\(ids.count) vs \(wantIDs.count) ids" + (firstDiff.map { ", first difference at \($0)" } ?? ""))

            // 3. The language model's logits over speech tokens + EOS.
            let lm = model
            let cache = try lm.model.newCache(parameters: nil)
            let got = lm.logits(lm.read(wantIDs, cache: cache))
            let full = fx["logits"]!.asArray(Float.self)
            let want = (0 ..< SoulXVocab.localCount).map { full[SoulXVocab.global($0)] }
            let d = zip(got, want).map { abs($0 - $1) }.max() ?? 0
            let argGot = got.indices.max { got[$0] < got[$1] }!, argWant = want.indices.max { want[$0] < want[$1] }!
            let top = Set(want.indices.sorted { want[$0] > want[$1] }.prefix(100))
            let overlap = got.indices.sorted { got[$0] > got[$1] }.prefix(100).filter { top.contains($0) }.count
            let pw = softmax(want), pg = softmax(got)
            var kl = 0.0
            for i in pw.indices where pw[i] > 0 { kl += pw[i] * log(pw[i] / max(pg[i], 1e-30)) }
            let strict = llmDir != nil && fp32
            r.check(strict ? "logits (fp32)" : "logits (8-bit, informative)", strict ? (d < 0.05 && argGot == argWant) : kl < 0.1,
                    String(format: "max |Δ| %.2e, argmax %@, top-100 overlap %d, KL %.2e", d, argGot == argWant ? "same" : "differs", overlap, kl))
            if let mx = info["text_logit_max"] as? Double, let ms = info["speech_logit_max"] as? Double {
                print(String(format: "      reference: best text-token logit %.2f, best speech/EOS logit %.2f", mx, ms))
            }

            // 4. The sampler's processed scores on the reference logits.
            if let hist = fx["proc_hist"]?.asArray(Int32.self).map({ Int($0) }), let procFull = fx["proc_scores"]?.asArray(Float.self) {
                let sampler = SoulXSampler(seed: 0)
                let generated = hist.compactMap(SoulXVocab.local)
                let mine = sampler.process(want, generated: generated)
                let ref = (0 ..< SoulXVocab.localCount).map { procFull[SoulXVocab.global($0)] }
                let maskSame = zip(mine, ref).allSatisfy { $0.isFinite == $1.isFinite }
                let dv = zip(mine, ref).filter { $0.isFinite && $1.isFinite }.map { abs($0 - $1) }.max() ?? 0
                r.check("sampler processed scores", maskSame && dv < 1e-4,
                        String(format: "kept %d vs %d, max |Δ| %.2e", mine.filter(\.isFinite).count, ref.filter(\.isFinite).count, dv))
            }
        }

        // 5. Flow matching with the reference's noise.
        if let toks = fx["flow_tokens"], let noise = fx["flow_noise"], let wantMel = fx["flow_mel"] {
            let ref = SoulXReference(tokens: fx["flow_prompt_tokens"]!.asArray(Int32.self).map { Int($0) },
                                     mel: fx["flow_prompt_mel"]!, xvector: fx["flow_xvector"]!, seconds: 0)
            let steps = info["flow_steps"] as? Int ?? 15
            let t0 = Date()
            let mel = model.flowMel(toks.asArray(Int32.self).map { Int($0) }, reference: ref, steps: steps, noise: noise)
            let dt = Date().timeIntervalSince(t0)
            r.check("flow mel shape", mel.shape == wantMel.shape, "\(mel.shape) vs \(wantMel.shape)")
            if mel.shape == wantMel.shape {
                let a = mel.reshaped(-1).asArray(Float.self), b = wantMel.reshaped(-1).asArray(Float.self)
                r.check("flow mel", correlation(a, b) > 0.999,
                        String(format: "correlation %.6f, max |Δ| %.2e, %.2f s", correlation(a, b), maxAbs(mel, wantMel), dt))
            }
            // 6. HiFT on the reference mel, without random phase or noise.
            if let wantWav = fx["hift_wav"] {
                model.flow.deterministicVocoderSource = true
                let (wav, _) = model.flow.vocoder(wantMel)
                model.flow.deterministicVocoderSource = false
                let a = wav.reshaped(-1).asArray(Float.self), b = wantWav.reshaped(-1).asArray(Float.self)
                r.check("HiFT waveform", a.count == b.count && snr(b, a) > 25,
                        String(format: "%d vs %d samples, SNR %.1f dB, correlation %.5f", a.count, b.count, snr(b, a), correlation(a, b)))
            }
        }
        print(r.failed == 0 ? "all checks passed" : "\(r.failed) check(s) failed")
        return r.failed == 0 ? 0 : 1
    }
}
