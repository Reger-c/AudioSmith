import AVFoundation
import Foundation
import XCTest
@testable import AudioSmithStream

/// watermark.embed's streaming pieces (review #102) against the whole-file
/// code they replace, kept here as the reference: the same windows, in the
/// same batches, crossfaded the same, and the same resampled samples, for
/// any way the input arrives in pieces.
final class WatermarkStreamTests: XCTestCase {
    // The reference: Watermark.swift before streaming.
    private func refWindows(_ count: Int, size: Int, overlap: Int) -> [(Int, Int)] {
        guard count > size else { return [(0, count)] }
        var out: [(Int, Int)] = []
        var start = 0
        while start < count {
            let end = min(start + size, count)
            out.append((start, end))
            if end == count { break }
            start = end - overlap
        }
        return out
    }

    private func refBatches(_ x: [Float], size: Int, overlap: Int) -> [[(start: Int, end: Int, samples: [Float])]] {
        let wins = refWindows(x.count, size: size, overlap: overlap)
        let length = min(size, x.count)
        let all = wins.map { (s, e) -> (start: Int, end: Int, samples: [Float]) in
            var w = Array(x[s ..< e])
            if w.count < length { w.append(contentsOf: [Float](repeating: 0, count: length - w.count)) }
            return (s, e, w)
        }
        return stride(from: 0, to: all.count, by: 8).map { Array(all[$0 ..< min($0 + 8, all.count)]) }
    }

    private func refWatermark(_ x: [Float], size: Int, overlap: Int, compute: ([[Float]]) -> [[Float]]) -> [Float] {
        var wm16 = [Float](repeating: 0, count: x.count)
        var first = true
        for group in refBatches(x, size: size, overlap: overlap) {
            let parts = compute(group.map(\.samples))
            for (w, part) in zip(group, parts) {
                for j in 0 ..< (w.end - w.start) {
                    let k = w.start + j
                    if !first, j < overlap {
                        let t = Float(j) / Float(overlap)
                        wm16[k] = wm16[k] * (1 - t) + part[j] * t
                    } else {
                        wm16[k] = part[j]
                    }
                }
                first = false
            }
        }
        return wm16
    }

    private func refResample(_ x: [Float], from: Int, to: Int) -> [Float] {
        if from == to || x.isEmpty { return x }
        let fin = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(from), channels: 1, interleaved: false)!
        let fout = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(to), channels: 1, interleaved: false)!
        let conv = AVAudioConverter(from: fin, to: fout)!
        let inBuf = AVAudioPCMBuffer(pcmFormat: fin, frameCapacity: AVAudioFrameCount(x.count))!
        conv.sampleRateConverterQuality = AVAudioQuality.max.rawValue
        inBuf.frameLength = AVAudioFrameCount(x.count)
        x.withUnsafeBufferPointer { inBuf.floatChannelData![0].update(from: $0.baseAddress!, count: x.count) }
        let expected = Int((Double(x.count) * Double(to) / Double(from)).rounded())
        var result: [Float] = []
        var fed = false
        while true {
            let outBuf = AVAudioPCMBuffer(pcmFormat: fout, frameCapacity: 65536)!
            var error: NSError?
            let status = conv.convert(to: outBuf, error: &error) { _, inputStatus in
                if fed {
                    inputStatus.pointee = .endOfStream
                    return nil
                }
                fed = true
                inputStatus.pointee = .haveData
                return inBuf
            }
            let n = Int(outBuf.frameLength)
            if n > 0 { result.append(contentsOf: UnsafeBufferPointer(start: outBuf.floatChannelData![0], count: n)) }
            if status == .endOfStream || status == .error || (n == 0 && fed) { break }
        }
        if result.count > expected { result.removeLast(result.count - expected) }
        if result.count < expected { result.append(contentsOf: [Float](repeating: 0, count: expected - result.count)) }
        return result
    }

    private func signal(_ n: Int, seed: UInt64) -> [Float] {
        var state = seed
        return (0 ..< n).map { i in
            state = state &* 6364136223846793005 &+ 1442695040888963407
            let noise = Float(Int64(bitPattern: state >> 11) % 1000) / 4000
            return 0.5 * sinf(Float(i) * 0.031) + noise
        }
    }

    /// A stand-in for AudioSeal: depends on the batch (its size, its
    /// windows' order) and on each window's samples, as a model's output may.
    private func fakeModel(_ xs: [[Float]]) -> [[Float]] {
        xs.enumerated().map { b, x in x.enumerated().map { j, v in v * 0.25 + Float(b) * 0.001 + Float(xs.count) * 0.0001 + Float(j % 7) * 0.00001 } }
    }

    func testWindowsMatchTheWholeFile() throws {
        let size = 1000, overlap = 100
        // Shorter than a window, exactly one, just over, many (several
        // batches), a window's end on the last sample.
        for count in [1, 37, 999, 1000, 1001, 1900, 1901, 8 * 900 + 100, 23_456, 900 * 20 + 100] {
            let x = signal(count, seed: UInt64(count))
            let want = refWatermark(x, size: size, overlap: overlap, compute: fakeModel)
            for piece in [1, 7, 333, 1000, 4096, count] {
                var got: [Float] = []
                var groups: [[Int]] = []
                let w = WindowedWatermark(size: size, overlap: overlap, compute: { group in
                    groups.append(group.map(\.start))
                    return self.fakeModel(group.map(\.samples))
                }, sink: { got.append(contentsOf: $0) }, done: { _ in })
                var at = 0
                while at < count {
                    let n = min(piece, count - at)
                    try w.feed(Array(x[at ..< at + n]))
                    at += n
                }
                try w.finish()
                let wantGroups = refBatches(x, size: size, overlap: overlap).map { $0.map(\.start) }
                XCTAssertEqual(groups, wantGroups, "count \(count) piece \(piece): batches")
                XCTAssertEqual(got, want, "count \(count) piece \(piece): samples")
                XCTAssertEqual(w.count, count)
            }
        }
    }

    func testResamplerMatchesTheWholeFile() throws {
        for (from, to) in [(16000, 24000), (16000, 44100), (16000, 48000), (16000, 22050), (16000, 16000)] {
            for count in [5, 16000, 160_001] {
                let x = signal(count, seed: UInt64(count + to))
                let want = refResample(x, from: from, to: to)
                for (piece, take) in [(512, 1000), (65536, 4410), (count, 7)] {
                    var at = 0
                    let r = try StreamResampler(count: count, from: from, to: to) { n in
                        let k = min(n, piece, count - at)
                        defer { at += k }
                        return Array(x[at ..< at + k])
                    }
                    var got: [Float] = []
                    while got.count < want.count + 10 {
                        got.append(contentsOf: try r.next(take))
                    }
                    XCTAssertEqual(Array(got.prefix(want.count)), want, "\(from)→\(to) count \(count) piece \(piece)")
                    XCTAssertTrue(got.dropFirst(want.count).allSatisfy { $0 == 0 }, "zeros after the end")
                }
            }
        }
    }

    /// 81 s of 16 kHz: the converter fed in pieces still gives the samples
    /// it gives fed at once.
    func testResamplerMatchesOverLongInputs() throws {
        let count = 1_296_000
        let x = signal(count, seed: 9)
        let want = refResample(x, from: 16000, to: 44100)
        for piece in [4096, 1 << 20] {
            var at = 0
            let r = try StreamResampler(count: count, from: 16000, to: 44100) { n in
                let k = min(n, piece, count - at)
                defer { at += k }
                return Array(x[at ..< at + k])
            }
            XCTAssertEqual(try r.next(want.count), want, "piece \(piece)")
        }
    }

    func testSpillReadsBackWhatItWrote() throws {
        let s = try FloatSpill()
        defer { s.remove() }
        let x = signal(100_003, seed: 3)
        try s.append(x[..<50_000])
        try s.append(x[50_000...])
        var back: [Float] = []
        while true {
            let p = try s.read(4097)
            if p.isEmpty { break }
            back.append(contentsOf: p)
        }
        XCTAssertEqual(back, x)
        XCTAssertEqual(s.count, x.count)
    }
}
