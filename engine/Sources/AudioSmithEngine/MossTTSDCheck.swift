import AudioSmithDialogue
import Foundation
@preconcurrency import MLX

// Developer checks of the MOSS-TTSD port (thread #82), run on the Mac by
// scripts/mossttsd/parity.sh; not used by the app. Each compares the
// engine with the official PyTorch code's outputs (scripts/mossttsd/reference.py)
// and exits 1 when a check fails.
//
//   audiosmith-engine --moss-check codec FIXTURES_TOK MODEL_DIR
//       loudness, codes of each reference and of both back to back, the
//       encoder's output, the decoded waveforms (one chunk and streamed);
//       strict against the official model with our 8-bit weights
//       dequantized ("q8."), informative against the official float32;
//   audiosmith-engine --moss-check tiny TINY_DIR
//       a tiny random model's greedy generation, row by row, against the
//       official generate() (the delay pattern's state machine, the
//       masks and the repetition penalty);
//   audiosmith-engine --moss-check lm FIXTURES_TTSD FIXTURES_TOK MODEL_DIR [--fp32]
//       the prompt's text and rows, and the 17 heads' logits at the last
//       25 rows; --fp32 runs the 8-bit model with float32 scales and
//       activations (the strict check against "q8."), else bfloat16 as the
//       app runs it.

enum MossTTSDCheck {
    typealias Report = SoulXCheck.Report

    static func run(_ args: [String]) async throws -> Int32 {
        guard let what = args.first else {
            log("usage: --moss-check codec|tiny|lm …")
            return 2
        }
        var r = Report()
        let started = Date()
        switch what {
        case "codec": try await codec(Array(args.dropFirst()), &r)
        case "tiny": try await tiny(Array(args.dropFirst()), &r)
        case "lm": try await lm(Array(args.dropFirst()), &r)
        default:
            log("unknown check \(what)")
            return 2
        }
        print(String(format: "%.1f s, MLX peak %.2f GB", Date().timeIntervalSince(started), Double(MLX.Memory.peakMemory) / 1e9))
        print(r.failed == 0 ? "all checks passed" : "\(r.failed) check(s) failed")
        return r.failed == 0 ? 0 : 1
    }

    static func ints(_ a: MLXArray) -> [[Int]] {
        let cols = a.dim(1)
        let flat = a.asType(.int32).asArray(Int32.self).map { Int($0) }
        return stride(from: 0, to: flat.count, by: cols).map { Array(flat[$0 ..< $0 + cols]) }
    }

    static func floats(_ a: MLXArray) -> [Float] { a.asType(.float32).reshaped(-1).asArray(Float.self) }

    static func equalShare(_ a: [[Int]], _ b: [[Int]]) -> (Double, String) {
        guard a.count == b.count else { return (0, "\(a.count) vs \(b.count) frames") }
        var same = 0, total = 0
        var perBook = [Int](repeating: 0, count: a.first?.count ?? 0)
        for (x, y) in zip(a, b) {
            for i in x.indices {
                total += 1
                if x[i] == y[i] { same += 1 } else { perBook[i] += 1 }
            }
        }
        let first = perBook.firstIndex { $0 > 0 }.map { ", first differing codebook \($0)" } ?? ""
        return (Double(same) / Double(max(total, 1)), String(format: "%d/%d codes equal (codebook 0: %d differ)%@", same, total, perBook.first ?? 0, first))
    }

    // MARK: codec

    static func codec(_ args: [String], _ r: inout Report) async throws {
        guard args.count >= 2 else { throw EngineError.badRequest("codec FIXTURES_TOK MODEL_DIR") }
        let fx = try MLX.loadArrays(url: URL(fileURLWithPath: args[0]))
        let t0 = Date()
        let codec = try MossCodec.load(URL(fileURLWithPath: args[1]))
        print(String(format: "codec loaded in %.1f s", Date().timeIntervalSince(t0)))
        var wavs: [[Float]] = []
        for i in 0 ..< 2 {
            guard let w = fx["wav\(i)"] else { break }
            let wav = floats(w)
            wavs.append(wav)
            let norm = MossCodec.loudnessNormalize(wav)
            let want = floats(fx["norm\(i)"]!)
            let d = zip(norm, want).map { abs($0 - $1) }.max() ?? 1
            r.check("ref\(i) loudness", d < 1e-5, String(format: "max |Δ| %.1e", d))
            let t = Date()
            let codes = codec.encode(norm)
            let dt = Date().timeIntervalSince(t)
            let (q8, q8s) = equalShare(codes, ints(fx["q8.codes\(i)"]!))
            r.check("ref\(i) codes vs q8", q8 >= 0.99, q8s + String(format: ", %.2f s", dt))
            let (f32, f32s) = equalShare(codes, ints(fx["codes\(i)"]!))
            r.check("ref\(i) codes vs fp32 (info)", true, String(format: "%.1f %%: ", f32 * 100) + f32s)
            if i == 0, let wantH = fx["q8.hidden0"] {
                let h = codec.encoderHidden(norm)[0] // (T, 768)
                let w = wantH.transposed() // (T, 768)
                if h.shape == w.shape {
                    let d = MLX.abs(h - w).max().item(Float.self), scale = MLX.abs(w).max().item(Float.self)
                    r.check("ref0 encoder output vs q8", d < 1e-3 * max(scale, 1), String(format: "max |Δ| %.2e (max |x| %.2f)", d, scale))
                } else {
                    r.check("ref0 encoder output shape", false, "\(h.shape) vs \(w.shape)")
                }
            }
        }
        if wavs.count == 2 {
            let cat = codec.encode(MossCodec.loudnessNormalize(wavs[0] + wavs[1]))
            let (q8, q8s) = equalShare(cat, ints(fx["q8.codesCat"]!))
            r.check("both clips' codes vs q8", q8 >= 0.99, q8s)
        }
        for (name, codesKey, want) in [("decode one chunk", "q8.codes0", "q8.decoded0"), ("decode streamed", "q8.codesCat", "q8.decodedCat"),
                                       ("decode streamed, fp32 codec (info)", "codesCat", "decodedCat")] {
            let t = Date()
            let wav = try codec.decode(ints(fx[codesKey]!))
            let dt = Date().timeIntervalSince(t)
            let ref = floats(fx[want]!)
            let snr = SoulXCheck.snr(ref, wav), corr = SoulXCheck.correlation(ref, wav)
            let info = name.hasSuffix("(info)")
            r.check(name, info || (wav.count == ref.count && snr > 30),
                    String(format: "%d vs %d samples, SNR %.1f dB, correlation %.5f, %.2f s", wav.count, ref.count, snr, corr, dt))
        }
    }

    // MARK: tiny

    static func tiny(_ args: [String], _ r: inout Report) async throws {
        guard let dirArg = args.first else { throw EngineError.badRequest("tiny TINY_DIR") }
        let dir = URL(fileURLWithPath: dirArg)
        let run = try JSONSerialization.jsonObject(with: Data(contentsOf: dir.appendingPathComponent("run.json"))) as! [String: Any]
        let input = run["inputIds"] as! [[Int]]
        let gen = run["generation"] as! [[Int]]
        let startLength = run["startLength"] as! Int
        let model = try await MossTTSDModel.load(dir, withCodec: false)
        var cfg = MossTTSDSampling()
        cfg.audioTemperature = 0
        cfg.textTemperature = 0
        cfg.audioRepetitionPenalty = Float(run["repetitionPenalty"] as? Double ?? 1.1)
        cfg.maxTokens = run["maxNewTokens"] as? Int ?? 80
        var sampler = MossTTSDSampler(config: cfg, seed: 0)
        let rows = try model.generate(input, sampler: &sampler)
        let want = Array(gen[startLength...])
        let firstDiff = zip(rows, want).enumerated().first { $0.element.0 != $0.element.1 }?.offset
        var detail = "\(rows.count) vs \(want.count) rows"
        if let k = firstDiff { detail += ", first difference at row \(k): \(rows[k]) vs \(want[k])" }
        let texts = Set(want.map { $0[0] })
        detail += "; text tokens seen: \(texts.sorted().prefix(8))"
        r.check("tiny greedy generation", rows == want, detail)
    }

    // MARK: lm

    static func lm(_ args: [String], _ r: inout Report) async throws {
        guard args.count >= 3 else { throw EngineError.badRequest("lm FIXTURES_TTSD FIXTURES_TOK MODEL_DIR [--fp32]") }
        let fx = try MLX.loadArrays(url: URL(fileURLWithPath: args[0]))
        let tok = try MLX.loadArrays(url: URL(fileURLWithPath: args[1]))
        let fp32 = args.contains("--fp32")
        let t0 = Date()
        let model = try await MossTTSDModel.load(URL(fileURLWithPath: args[2]), fp32: fp32, withCodec: false)
        print(String(format: "language model loaded in %.1f s (%@)", Date().timeIntervalSince(t0), fp32 ? "float32 activations" : "bfloat16"))

        // 1. The text and the prompt rows.
        let wantText = String(decoding: fx["text"]!.asArray(UInt8.self), as: UTF8.self)
        let refTexts = String(decoding: tok["refText"]!.asArray(UInt8.self), as: UTF8.self).components(separatedBy: "\u{0}")
        if let tj = fx["turns"] {
            let turns = try JSONSerialization.jsonObject(with: Data(tj.asArray(UInt8.self))) as! [[String]]
            let text = MossTTSDText.prompt(references: refTexts, turns: turns.map { (Int($0[0])!, $0[1]) })
            r.check("prompt text", text == wantText, text == wantText ? "\(text.count) characters" : "\(text) vs \(wantText)")
        }
        let all = ints(fx["inputIds"]!)
        let promptRows = fx["promptRows"]!.asArray(Int32.self).map { Int($0) }[0]
        let rows = MossTTSDPrompt.rows(text: wantText, references: [ints(tok["codes0"]!), ints(tok["codes1"]!)],
                                       prompt: ints(tok["codesCat"]!), encode: model.encode)
        let wantRows = Array(all[..<promptRows])
        let diff = zip(rows, wantRows).enumerated().first { $0.element.0 != $0.element.1 }?.offset
        r.check("prompt rows", rows == wantRows, "\(rows.count) vs \(wantRows.count) rows" + (diff.map { ", first difference at row \($0): \(rows[$0]) vs \(wantRows[$0])" } ?? ""))

        // 2. The last row after the first layer, and after the final norm.
        let l0 = model.hiddenAfter(all, layers: 1).asType(.float32)
        for (prefix, label) in [("q8.", "vs q8"), ("", "vs fp32 (info)")] {
            guard let w = fx[prefix + "layer0Last"] else { continue }
            let d = MLX.abs(l0 - w).max().item(Float.self), sc = MLX.abs(w).max().item(Float.self)
            r.check("layer 0, last row \(label)\(fp32 ? "" : " (info)")", prefix.isEmpty || !fp32 || d < 2e-3 * max(sc, 1), String(format: "max |Δ| %.2e (max |x| %.2f)", d, sc))
        }

        // 3. Logits of the 17 heads at the last 25 rows.
        let t1 = Date()
        let h = model.hiddenAll(all)
        let keep = fx["audioLogits"]!.dim(0)
        let last = h[(h.dim(0) - keep)...]
        let audio = model.audioLogits(last) // (keep, 16, 1025)
        let text = model.textLogits(last)
        eval(audio, text)
        print(String(format: "forward of %d rows in %.1f s", all.count, Date().timeIntervalSince(t1)))
        for (prefix, label) in [("q8.", "vs q8"), ("", "vs fp32 (info)")] {
            guard let w = fx[prefix + "normLast"] else { continue }
            let g = last[keep - 1].asType(.float32)
            let d = MLX.abs(g - w).max().item(Float.self), sc = MLX.abs(w).max().item(Float.self)
            r.check("final norm, last row \(label)", true, String(format: "max |Δ| %.2e (max |x| %.2f)", d, sc))
        }
        for (prefix, label, strict) in [("q8.", "vs official with our 8-bit weights", fp32), ("", "vs official bf16 weights in fp32", false)] {
            guard let wa = fx[prefix + "audioLogits"], let wt = fx[prefix + "textLogits"] else { continue }
            let s = compare(floats(audio), floats(wa), rows: keep * 16, cols: 1025)
            r.check("audio logits \(label)\(strict ? "" : " (info)")", !strict || (s.top1 >= 0.99 && s.kl < 1e-3),
                    String(format: "max |Δ| %.2e, top-1 %.1f %%, top-50 overlap %.1f %%, KL %.2e", s.maxAbs, s.top1 * 100, s.top50 * 100, s.kl))
            let st = compare(floats(text), floats(wt), rows: keep, cols: wt.dim(1))
            r.check("text logits \(label)\(strict ? "" : " (info)")", !strict || (st.top1 >= 0.99 && st.kl < 1e-3),
                    String(format: "max |Δ| %.2e, top-1 %.1f %%, top-50 overlap %.1f %%, KL %.2e", st.maxAbs, st.top1 * 100, st.top50 * 100, st.kl))
        }
    }

    struct Stats { var maxAbs: Float = 0, top1 = 0.0, top50 = 0.0, kl = 0.0 }

    /// got vs want, row by row (rows × cols): -infinity entries skipped.
    static func compare(_ got: [Float], _ want: [Float], rows: Int, cols: Int) -> Stats {
        var s = Stats()
        for r in 0 ..< rows {
            let g = Array(got[(r * cols) ..< ((r + 1) * cols)]), w = Array(want[(r * cols) ..< ((r + 1) * cols)])
            for (a, b) in zip(g, w) where a.isFinite && b.isFinite { s.maxAbs = max(s.maxAbs, abs(a - b)) }
            let ig = g.indices.max { g[$0] < g[$1] }!, iw = w.indices.max { w[$0] < w[$1] }!
            if ig == iw { s.top1 += 1 }
            let tw = Set(w.indices.sorted { w[$0] > w[$1] }.prefix(50))
            s.top50 += Double(g.indices.sorted { g[$0] > g[$1] }.prefix(50).filter { tw.contains($0) }.count) / 50
            let pw = softmax(w), pg = softmax(g)
            var kl = 0.0
            for i in pw.indices where pw[i] > 0 { kl += pw[i] * log(pw[i] / max(pg[i], 1e-30)) }
            s.kl += kl
        }
        let n = Double(rows)
        s.top1 /= n
        s.top50 /= n
        s.kl /= n
        return s
    }
}
