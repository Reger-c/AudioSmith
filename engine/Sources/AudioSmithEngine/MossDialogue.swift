import AudioSmithDialogue
import AudioSmithProtocol
import Foundation
@preconcurrency import MLX

// tts.dialogue on MOSS-TTSD (thread #82): the whole conversation in one
// track, as the official TTSD Space's voice-clone-and-continuation mode
// does it:
//
//   - each speaker's reference clip is encoded on its own (the user
//     message's Reference(s)), and all clips back to back are encoded once
//     more as the assistant's start, which the model continues;
//   - the text: every reference transcript as [Sn], then the turns, through
//     the Space's normalize_text (MossTTSDText);
//   - the new frames are decoded with the prompt's as the codec's left
//     context and written to <outputDir>/dialogue.wav.
//
// Context turns (with codes: frames from an earlier result's mixed.codes)
// go after the prompt clip in the assistant's start, their text in the
// text: the model goes on from there, so one turn can be made again after
// the turns before it. The result: the mixed track (and its frames), and
// each spoken turn's index, speaker and text as the model read it.

extension Synthesizer {
    func mossDialogue(_ p: DialogueParams, index: [String: Int], bits: Int, job: String, offline: Bool) async throws -> DialogueResult {
        guard let first = p.turns.firstIndex(where: { $0.codes == nil }),
              p.turns[first...].allSatisfy({ $0.codes == nil }) else {
            throw EngineError.badRequest("MOSS-TTSD takes context turns (with codes) only before the turns to speak")
        }
        let n = MossTTSDTokens.nVQ
        var context: [[Int]] = []
        for (i, t) in p.turns[..<first].enumerated() {
            let c = t.codes ?? []
            guard c.count % n == 0, c.allSatisfy({ $0 >= 0 && $0 < MossTTSDTokens.audioPad }) else {
                throw EngineError.badRequest("Turn \(i) has codes that are not frames of \(n) codes in 0..<\(MossTTSDTokens.audioPad)")
            }
            for f in stride(from: 0, to: c.count, by: n) { context.append(Array(c[f ..< f + n])) }
        }

        let model = try await acquireMoss(p.model, path: p.modelPath, job: job, offline: offline)
        defer { store.release(p.model) }
        guard let codec = model.codec else { throw EngineError.loadFailed("MOSS-TTSD: no codec") }

        let seed = p.seed ?? Int64.random(in: 0 ... Int64(Int32.max))
        MLXRandom.seed(UInt64(bitPattern: seed))
        var sampling = MossTTSDSampling()
        if let t = p.temperature, t >= 0 { sampling.audioTemperature = Float(t) }
        if let v = p.topP, v > 0, v <= 1 { sampling.audioTopP = Float(v) }
        if let k = p.topK, k > 0 { sampling.audioTopK = k }
        if let r = p.repetitionPenalty, r > 0 { sampling.audioRepetitionPenalty = Float(r) }

        let progress = JobProgress(out: out, job: job)
        progress.send("encoding", force: true)
        let started = Date()
        var refs: [MossReference] = []
        for s in p.speakers {
            refs.append(try await model.reference(s.refAudio))
            try Task.checkCancellation()
        }
        let promptCodes = codec.encode(MossCodec.loudnessNormalize(refs.flatMap(\.samples)))
        let turns = p.turns.map { (speaker: index[$0.speaker]! + 1, text: $0.text) }
        let text = MossTTSDText.prompt(references: p.speakers.map(\.refText), turns: turns)
        let rows = MossTTSDPrompt.rows(text: text, references: refs.map(\.codes), prompt: promptCodes + context, encode: model.encode)

        // About 4 Chinese characters or 2.5 words a second: the progress's
        // scale, and at most twice that (plus 10 s) before giving up.
        var units = 0.0
        for t in p.turns[first...] {
            for s in t.text.unicodeScalars where s.properties.isIdeographic { units += 0.25 }
            units += Double(t.text.split(whereSeparator: { $0.isWhitespace }).filter { $0.unicodeScalars.contains { !$0.properties.isIdeographic && $0.properties.isAlphabetic } }.count) * 0.4
        }
        let expected = max(units, 2) * MossTTSDModel.frameRate
        sampling.maxTokens = min(p.maxTokens.flatMap { $0 > 0 ? $0 : nil } ?? Int((units * 2 + 10) * MossTTSDModel.frameRate), 2000)
        var sampler = MossTTSDSampler(config: sampling, seed: UInt64(bitPattern: seed) &+ 0x5EED)

        progress.fraction = 0
        progress.send("generating", force: true)
        let genStart = Date()
        let generated = try model.generate(rows, sampler: &sampler) { k in
            progress.fraction = min(Double(k) / expected, 0.95)
            progress.audioSeconds = Double(k) / MossTTSDModel.frameRate
            progress.send("generating")
        }
        let genSeconds = Date().timeIntervalSince(genStart)
        let ended = generated.last?[0] == MossTTSDTokens.imEnd
        out.logLine("info", String(format: "MOSS-TTSD: %d prompt rows, %d rows in %.1f s (%.1f rows/s)%@", rows.count, generated.count,
                                   genSeconds, Double(generated.count) / max(genSeconds, 1e-6), ended ? "" : ", stopped at the limit"), job: job)
        progress.send("decoding", force: true)
        let (samples, frames) = try model.speech(prompt: rows, generated: generated) { Task.isCancelled }
        try Task.checkCancellation()
        guard !samples.isEmpty else { throw RPCError(Code.internal, "MOSS-TTSD produced no audio") }

        let outDir = URL(fileURLWithPath: p.outputDir)
        try FileManager.default.createDirectory(at: outDir, withIntermediateDirectories: true)
        let url = outDir.appendingPathComponent("dialogue.wav")
        do {
            try writeWAV(samples, sampleRate: MossTTSDModel.sampleRate, bits: bits, to: url)
        } catch {
            throw RPCError(Code.internal, "Could not write \(url.path): \(error.localizedDescription)")
        }
        let seconds = Double(samples.count) / Double(MossTTSDModel.sampleRate)
        var outTurns: [DialogueTurnOut] = []
        for (i, t) in p.turns.enumerated() where i >= first {
            var o = DialogueTurnOut(index: i, speaker: t.speaker)
            o.text = MossTTSDText.turnText(t.text)
            outTurns.append(o)
        }
        MLX.Memory.clearCache()
        let total = Date().timeIntervalSince(started)
        let mixed = DialogueMixed(output: url.path, frames: Int64(samples.count), audioSeconds: seconds,
                                  codes: frames.flatMap { $0 }, codebooks: n)
        return DialogueResult(model: p.model, sampleRate: MossTTSDModel.sampleRate, channels: 1, turns: outTurns, mixed: mixed,
                              audioSeconds: seconds, seconds: total, rtf: total > 0 ? seconds / total : 0, seed: seed)
    }

    /// MOSS-TTSD, loaded (stage "loading") when it is not resident. Call
    /// store.release(model) when done.
    func acquireMoss(_ model: String, path: String?, job: String, offline: Bool) async throws -> MossTTSDModel {
        guard let dir = try modelDirectory(model, path: path, offline: offline) else {
            throw EngineError.notDownloaded(model: model, missing: ["config.json"])
        }
        let (m, _) = try await store.acquire(model, task: "dialogue", job: job, as: MossTTSDModel.self,
                                             sampleRate: { _ in MossTTSDModel.sampleRate }, estimate: weightBytes(dir)) {
            try await MossTTSDModel.load(dir)
        }
        return m
    }
}
