import AudioSmithProtocol
import AVFoundation
import Foundation

// watermark.embed in constant memory (Watermark.swift in the engine) (review #102: the whole input was
// held 3–4 times, 9 GB and more for 3 hours of 48 kHz stereo). The 16 kHz
// mix is read once and cut into the same windows and batches as before
// (WindowedWatermark), the watermark kept in a temporary file of floats
// (FloatSpill); then the input is read again at its own rate, the
// watermark resampled as it goes (StreamResampler), added, and written.

/// A temporary file of Float32 samples, written in order and read back
/// in order.
public final class FloatSpill {
    public let url: URL
    private var handle: FileHandle?
    private var reader: FileHandle?
    public private(set) var count = 0

    public init() throws {
        url = FileManager.default.temporaryDirectory.appendingPathComponent("audiosmith-wm-\(getpid())-\(UUID().uuidString).f32")
        guard FileManager.default.createFile(atPath: url.path, contents: nil) else {
            throw CocoaError(.fileWriteUnknown, userInfo: [NSFilePathErrorKey: url.path])
        }
        handle = try FileHandle(forWritingTo: url)
    }

    deinit { remove() }

    public func append(_ x: ArraySlice<Float>) throws {
        guard !x.isEmpty, let handle else { return }
        let data = x.withUnsafeBufferPointer { Data(buffer: $0) }
        try handle.write(contentsOf: data)
        count += x.count
    }

    /// Up to n samples after those read so far; empty at the end.
    public func read(_ n: Int) throws -> [Float] {
        if reader == nil {
            try handle?.close()
            handle = nil
            reader = try FileHandle(forReadingFrom: url)
        }
        guard n > 0, let data = try reader?.read(upToCount: n * 4), !data.isEmpty else { return [] }
        return data.withUnsafeBytes { Array($0.bindMemory(to: Float.self)) }
    }

    public func remove() {
        try? handle?.close()
        try? reader?.close()
        handle = nil
        reader = nil
        try? FileManager.default.removeItem(at: url)
    }
}

/// The windows of a signal fed in pieces, as `windows` cuts the whole
/// signal (size samples, overlap shared, the last up to the end; one
/// window for a signal no longer than size), padded with zeros as
/// `batches` pads them, run through compute 8 at a time in the same
/// groups, and the results crossfaded over the overlaps into sink, in
/// order, as they are final.
public final class WindowedWatermark {
    public typealias Window = (start: Int, end: Int, samples: [Float])
    public let size: Int, overlap: Int
    private let compute: ([Window]) throws -> [[Float]]
    private let sink: (ArraySlice<Float>) throws -> Void
    private let done: (Int) -> Void
    private var buf: [Float] = []
    private var bufStart = 0 // the sample buf[0] is
    private var next = 0 // where the next window starts
    private var pending: [Window] = []
    private var acc: [Float] = [] // results not final yet, from accStart
    private var accStart = 0
    private var first = true
    public private(set) var count = 0

    /// done is told the end of each window computed (progress).
    public init(size: Int, overlap: Int, compute: @escaping ([Window]) throws -> [[Float]],
         sink: @escaping (ArraySlice<Float>) throws -> Void, done: @escaping (Int) -> Void) {
        self.size = size
        self.overlap = overlap
        self.compute = compute
        self.sink = sink
        self.done = done
    }

    public func feed(_ x: [Float]) throws {
        buf.append(contentsOf: x)
        count += x.count
        // A window that ends before the samples read is not the last.
        while next + size < count {
            let from = next - bufStart
            pending.append((next, next + size, Array(buf[from ..< from + size])))
            next += size - overlap
            if pending.count == 8 { try run() }
        }
        if next > bufStart {
            buf.removeFirst(next - bufStart)
            bufStart = next
        }
    }

    /// Runs the last window and what is pending.
    public func finish() throws {
        guard count > 0 else { return }
        let length = min(size, count)
        var w = Array(buf[(next - bufStart)...])
        if w.count < length { w.append(contentsOf: [Float](repeating: 0, count: length - w.count)) }
        pending.append((next, count, w))
        buf = []
        try run()
        try sink(acc[...])
        acc = []
    }

    private func run() throws {
        let parts = try compute(pending)
        for (w, part) in zip(pending, parts) {
            let have = accStart + acc.count
            if w.end > have { acc.append(contentsOf: [Float](repeating: 0, count: w.end - have)) }
            for j in 0 ..< (w.end - w.start) {
                let k = w.start + j - accStart
                // Crossfade the overlap with the previous window.
                if !first, j < overlap {
                    let t = Float(j) / Float(overlap)
                    acc[k] = acc[k] * (1 - t) + part[j] * t
                } else {
                    acc[k] = part[j]
                }
            }
            first = false
            done(w.end)
            // Before the next window's overlap, the results are final.
            let final = w.end - overlap - accStart
            if final > 0 {
                try sink(acc[..<final])
                acc.removeFirst(final)
                accStart += final
            }
        }
        pending = []
    }
}

/// Resamples a mono signal read in pieces from source with
/// AVAudioConverter (best quality), as resampleMono does the whole: count
/// samples in, round(count × to / from) out, the last ones zeros when the
/// converter makes fewer. next(n) returns the next n, zeros past the end.
public final class StreamResampler {
    private let source: (Int) throws -> [Float]
    private let conv: AVAudioConverter?
    private let fin: AVAudioFormat?
    private let fout: AVAudioFormat?
    private let expected: Int
    private var queue: [Float] = []
    private var produced = 0
    private var sourceEnded = false
    private var ended = false
    private var failure: Error?

    public init(count: Int, from: Int, to: Int, source: @escaping (Int) throws -> [Float]) throws {
        self.source = source
        if from == to || count == 0 {
            conv = nil
            fin = nil
            fout = nil
            expected = count
            return
        }
        guard let fin = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(from), channels: 1, interleaved: false),
              let fout = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(to), channels: 1, interleaved: false),
              let conv = AVAudioConverter(from: fin, to: fout) else {
            throw RPCError(Code.internal, "Cannot resample \(from) Hz to \(to) Hz")
        }
        conv.sampleRateConverterQuality = AVAudioQuality.max.rawValue
        self.conv = conv
        self.fin = fin
        self.fout = fout
        expected = Int((Double(count) * Double(to) / Double(from)).rounded())
    }

    public func next(_ n: Int) throws -> [Float] {
        while queue.count < n, !ended {
            try pull()
        }
        var out = Array(queue.prefix(n))
        queue.removeFirst(out.count)
        if out.count < n { out.append(contentsOf: [Float](repeating: 0, count: n - out.count)) }
        return out
    }

    private func take(_ x: [Float]) {
        let room = expected - produced
        guard room > 0 else { return }
        let part = x.prefix(room)
        queue.append(contentsOf: part)
        produced += part.count
    }

    private func pull() throws {
        guard let conv, let fin, let fout else {
            // The same rate: the samples as they are.
            let x = try source(65536)
            if x.isEmpty { ended = true } else { take(x) }
            if ended, produced < expected { take([Float](repeating: 0, count: expected - produced)) }
            return
        }
        guard let outBuf = AVAudioPCMBuffer(pcmFormat: fout, frameCapacity: 65536) else {
            ended = true
            return
        }
        var error: NSError?
        let status = conv.convert(to: outBuf, error: &error) { packets, inputStatus in
            if self.sourceEnded || self.failure != nil {
                inputStatus.pointee = .endOfStream
                return nil
            }
            let x: [Float]
            do {
                x = try self.source(max(Int(packets), 1))
            } catch {
                self.failure = error
                inputStatus.pointee = .endOfStream
                return nil
            }
            guard !x.isEmpty, let inBuf = AVAudioPCMBuffer(pcmFormat: fin, frameCapacity: AVAudioFrameCount(x.count)) else {
                self.sourceEnded = true
                inputStatus.pointee = .endOfStream
                return nil
            }
            inBuf.frameLength = AVAudioFrameCount(x.count)
            x.withUnsafeBufferPointer { inBuf.floatChannelData![0].update(from: $0.baseAddress!, count: x.count) }
            inputStatus.pointee = .haveData
            return inBuf
        }
        if let failure { throw failure }
        if let error { throw RPCError(Code.internal, "Resampling failed: \(error.localizedDescription)") }
        let n = Int(outBuf.frameLength)
        if n > 0 { take(Array(UnsafeBufferPointer(start: outBuf.floatChannelData![0], count: n))) }
        if status == .endOfStream || status == .error || (n == 0 && sourceEnded) {
            ended = true
            if produced < expected { take([Float](repeating: 0, count: expected - produced)) }
        }
    }
}
