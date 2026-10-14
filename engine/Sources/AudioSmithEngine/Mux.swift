import AudioSmithProtocol
import AVFoundation
import CoreMedia
import Foundation

// media.mux (M4): an MP4 (or MOV) with the source's video track copied as
// it is, the source's audio (keepAudio), N dubbed audio tracks encoded AAC
// 48 kHz stereo, and soft subtitle tracks (3GPP timed text) from SRT or
// WebVTT — each with its language and title; the audio tracks in one
// alternate group (one enabled), the subtitles in another. Audio and
// subtitles are cut or padded to the video's duration.

/// ISO 639-2/T of a BCP-47 tag ("es" → "spa"), as QuickTime/MP4 store it.
func iso639_2(_ tag: String) -> String? {
    guard !tag.isEmpty else { return nil }
    let lang = Locale.Language(identifier: tag)
    return lang.languageCode?.identifier(.alpha3)
}

/// One subtitle cue.
struct Cue {
    var start: Double
    var end: Double
    var text: String
}

/// Parses SRT or WebVTT (timestamps "hh:mm:ss,mmm" or "mm:ss.mmm").
func parseSubtitles(_ text: String) -> [Cue] {
    func seconds(_ s: Substring) -> Double? {
        let parts = s.trimmingCharacters(in: .whitespaces).replacingOccurrences(of: ",", with: ".").split(separator: ":")
        guard (2 ... 3).contains(parts.count) else { return nil }
        var t = 0.0
        for p in parts {
            guard let v = Double(p) else { return nil }
            t = t * 60 + v
        }
        return t
    }
    var cues: [Cue] = []
    let blocks = text.replacingOccurrences(of: "\r\n", with: "\n").components(separatedBy: "\n\n")
    for block in blocks {
        let lines = block.split(separator: "\n", omittingEmptySubsequences: false).map(String.init)
        guard let i = lines.firstIndex(where: { $0.contains("-->") }) else { continue }
        let times = lines[i].components(separatedBy: "-->")
        guard times.count == 2, let a = seconds(Substring(times[0])),
              let b = seconds(Substring(times[1].split(separator: " ", omittingEmptySubsequences: true).first ?? "")) else { continue }
        let body = lines[(i + 1)...].joined(separator: "\n").trimmingCharacters(in: .whitespacesAndNewlines)
        // WebVTT/SRT markup (<i>, <b>, {\an8}…) is dropped.
        let plain = body.replacingOccurrences(of: "<[^>]+>", with: "", options: .regularExpression)
            .replacingOccurrences(of: "\\{[^}]*\\}", with: "", options: .regularExpression)
        if b > a, !plain.isEmpty { cues.append(Cue(start: a, end: b, text: plain)) }
    }
    return cues.sorted { $0.start < $1.start }
}

/// A 3GPP timed-text ('tx3g') sample description, laid out big-endian as
/// TS 26.245 defines it: white sans-serif text at the bottom.
func tx3gFormat() throws -> CMFormatDescription {
    var d: [UInt8] = []
    func u32(_ v: UInt32) { d += [UInt8(v >> 24), UInt8((v >> 16) & 0xFF), UInt8((v >> 8) & 0xFF), UInt8(v & 0xFF)] }
    func u16(_ v: UInt16) { d += [UInt8(v >> 8), UInt8(v & 0xFF)] }
    u32(0); d += Array("tx3g".utf8); d += [0, 0, 0, 0, 0, 0]; u16(1) // size (set below), type, reserved, data reference
    u32(0) // display flags
    d += [1, 0xFF] // horizontal: centre; vertical: bottom (-1)
    d += [0, 0, 0, 0] // background RGBA
    u16(0); u16(0); u16(0); u16(0) // default text box
    u16(0); u16(0); u16(1); d += [0, 18]; d += [0xFF, 0xFF, 0xFF, 0xFF] // style: chars 0–0, font 1, plain, 18 pt, white
    u32(18); d += Array("ftab".utf8); u16(1); u16(1); d += [5]; d += Array("Serif".utf8) // font table: 1 = "Serif"
    let n = UInt32(d.count)
    d[0] = UInt8(n >> 24); d[1] = UInt8((n >> 16) & 0xFF); d[2] = UInt8((n >> 8) & 0xFF); d[3] = UInt8(n & 0xFF)
    var fd: CMFormatDescription?
    let status = d.withUnsafeBufferPointer {
        CMTextFormatDescriptionCreateFromBigEndianTextDescriptionData(allocator: kCFAllocatorDefault, bigEndianTextDescriptionData: $0.baseAddress!,
                                                                     size: $0.count, flavor: nil, mediaType: kCMMediaType_Subtitle, formatDescriptionOut: &fd)
    }
    guard status == noErr, let fd else { throw RPCError(Code.internal, "Could not describe the subtitle track (\(status))") }
    return fd
}

/// A timed-text sample: 16-bit big-endian length, then UTF-8.
func textSample(_ text: String, start: Double, end: Double, format: CMFormatDescription) throws -> CMSampleBuffer {
    let utf8 = Array(text.utf8.prefix(65_000))
    let bytes: [UInt8] = [UInt8(utf8.count >> 8), UInt8(utf8.count & 0xFF)] + utf8
    var block: CMBlockBuffer?
    var status = CMBlockBufferCreateWithMemoryBlock(allocator: kCFAllocatorDefault, memoryBlock: nil, blockLength: bytes.count,
                                                    blockAllocator: kCFAllocatorDefault, customBlockSource: nil, offsetToData: 0,
                                                    dataLength: bytes.count, flags: kCMBlockBufferAssureMemoryNowFlag, blockBufferOut: &block)
    guard status == noErr, let block else { throw RPCError(Code.internal, "text sample (\(status))") }
    status = bytes.withUnsafeBytes { CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block, offsetIntoDestination: 0, dataLength: bytes.count) }
    guard status == noErr else { throw RPCError(Code.internal, "text sample (\(status))") }
    var timing = CMSampleTimingInfo(duration: CMTime(seconds: max(end - start, 0.001), preferredTimescale: 1000),
                                    presentationTimeStamp: CMTime(seconds: start, preferredTimescale: 1000), decodeTimeStamp: .invalid)
    var size = bytes.count
    var sample: CMSampleBuffer?
    status = CMSampleBufferCreate(allocator: kCFAllocatorDefault, dataBuffer: block, dataReady: true, makeDataReadyCallback: nil, refcon: nil,
                                  formatDescription: format, sampleCount: 1, sampleTimingEntryCount: 1, sampleTimingArray: &timing,
                                  sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &sample)
    guard status == noErr, let sample else { throw RPCError(Code.internal, "text sample (\(status))") }
    return sample
}

/// Silence: interleaved float32 PCM, frames long, at pts.
func silenceSample(frames: Int, rate: Int, channels: Int, pts: CMTime) throws -> CMSampleBuffer {
    var asbd = AudioStreamBasicDescription(mSampleRate: Float64(rate), mFormatID: kAudioFormatLinearPCM,
                                           mFormatFlags: kAudioFormatFlagIsFloat | kAudioFormatFlagIsPacked,
                                           mBytesPerPacket: UInt32(4 * channels), mFramesPerPacket: 1, mBytesPerFrame: UInt32(4 * channels),
                                           mChannelsPerFrame: UInt32(channels), mBitsPerChannel: 32, mReserved: 0)
    var fmt: CMAudioFormatDescription?
    var status = CMAudioFormatDescriptionCreate(allocator: kCFAllocatorDefault, asbd: &asbd, layoutSize: 0, layout: nil,
                                                magicCookieSize: 0, magicCookie: nil, extensions: nil, formatDescriptionOut: &fmt)
    guard status == noErr, let fmt else { throw RPCError(Code.internal, "silence format (\(status))") }
    let length = frames * 4 * channels
    var block: CMBlockBuffer?
    status = CMBlockBufferCreateWithMemoryBlock(allocator: kCFAllocatorDefault, memoryBlock: nil, blockLength: length, blockAllocator: kCFAllocatorDefault,
                                                customBlockSource: nil, offsetToData: 0, dataLength: length, flags: kCMBlockBufferAssureMemoryNowFlag,
                                                blockBufferOut: &block)
    guard status == noErr, let block else { throw RPCError(Code.internal, "silence block (\(status))") }
    CMBlockBufferFillDataBytes(with: 0, blockBuffer: block, offsetIntoDestination: 0, dataLength: length)
    var sample: CMSampleBuffer?
    status = CMAudioSampleBufferCreateReadyWithPacketDescriptions(allocator: kCFAllocatorDefault, dataBuffer: block, formatDescription: fmt,
                                                                   sampleCount: frames, presentationTimeStamp: pts, packetDescriptions: nil,
                                                                   sampleBufferOut: &sample)
    guard status == noErr, let sample else { throw RPCError(Code.internal, "silence sample (\(status))") }
    return sample
}

/// Feeds one writer input from a source of sample buffers on its own
/// queue; done is called once it is finished (or failed).
final class Feed: @unchecked Sendable {
    let input: AVAssetWriterInput
    let next: () throws -> CMSampleBuffer?
    let queue: DispatchQueue
    var error: Error?
    var onTime: ((Double) -> Void)?
    private var finished = false

    init(_ input: AVAssetWriterInput, label: String, next: @escaping () throws -> CMSampleBuffer?) {
        self.input = input
        self.next = next
        queue = DispatchQueue(label: "audiosmith.mux.\(label)")
    }

    func start(cancelled: @escaping () -> Bool, group: DispatchGroup) {
        group.enter()
        input.requestMediaDataWhenReady(on: queue) { [self] in
            guard !finished else { return }
            while input.isReadyForMoreMediaData {
                do {
                    if cancelled() { throw CancellationError() }
                    guard let buffer = try next() else {
                        finish(group)
                        return
                    }
                    onTime?(CMSampleBufferGetPresentationTimeStamp(buffer).seconds)
                    if !input.append(buffer) {
                        throw RPCError(Code.internal, "The writer refused a sample")
                    }
                } catch {
                    self.error = error
                    finish(group)
                    return
                }
            }
        }
    }

    private func finish(_ group: DispatchGroup) {
        guard !finished else { return }
        finished = true
        input.markAsFinished()
        group.leave()
    }
}

extension MediaWorker {
    func mux(_ p: MuxParams, job: String) async throws -> MuxResult {
        if !(p.retime ?? []).isEmpty {
            throw EngineError.unsupported("Retiming the video is not supported yet", capability: "retime")
        }
        let format = (p.output.format ?? "mp4").lowercased()
        guard format == "mp4" || format == "mov" else { throw EngineError.badRequest("Unknown output format \(format)") }
        guard p.output.path.hasPrefix("/") else { throw EngineError.badRequest("output.path must be an absolute path") }
        let media = try await openMedia(p.video)
        let videoTracks = try await media.asset.loadTracks(withMediaType: .video)
        guard let videoTrack = videoTracks.first else { throw EngineError.unsupportedMedia(media.url.path, "it has no video track") }
        let duration = try await media.asset.load(.duration)
        let end = duration.seconds
        var dubbed: [Media] = []
        for t in p.audio { dubbed.append(try await openMedia(t.path)) }
        var subtitleCues: [[Cue]] = []
        for s in p.subtitles ?? [] {
            let url = try inputURL(s.path)
            guard let text = try? String(contentsOf: url, encoding: .utf8) else { throw EngineError.unsupportedMedia(url.path, "it is not UTF-8 text") }
            subtitleCues.append(parseSubtitles(text))
        }

        let dest = URL(fileURLWithPath: p.output.path)
        let dir = dest.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let tmp = dir.appendingPathComponent(".\(dest.deletingPathExtension().lastPathComponent).\(getpid()).\(format)")
        try? FileManager.default.removeItem(at: tmp)
        let writer = try AVAssetWriter(outputURL: tmp, fileType: format == "mov" ? .mov : .mp4)
        var feeds: [Feed] = []

        // Video: copied.
        let videoFormat = try await videoTrack.load(.formatDescriptions).first
        let vin = AVAssetWriterInput(mediaType: .video, outputSettings: nil, sourceFormatHint: videoFormat)
        vin.transform = try await videoTrack.load(.preferredTransform)
        vin.expectsMediaDataInRealTime = false
        guard writer.canAdd(vin) else { throw EngineError.unsupportedMedia(media.url.path, "its video cannot go in \(format.uppercased())") }
        writer.add(vin)
        let vreader = try AVAssetReader(asset: media.asset)
        let vout = AVAssetReaderTrackOutput(track: videoTrack, outputSettings: nil)
        vout.alwaysCopiesSampleData = false
        vreader.add(vout)
        let videoFeed = Feed(vin, label: "video") { vout.copyNextSampleBuffer() }
        feeds.append(videoFeed)
        var readers = [vreader]

        func named(_ input: AVAssetWriterInput, language: String?, title: String?) {
            if let tag = nonEmpty(language) {
                input.extendedLanguageTag = tag
                if let code = iso639_2(tag) { input.languageCode = code }
            }
            if let title = nonEmpty(title) {
                let item = AVMutableMetadataItem()
                item.identifier = .quickTimeUserDataTrackName
                item.value = title as NSString
                item.extendedLanguageTag = nonEmpty(language)
                input.metadata = [item]
            }
        }
        /// Audio read as PCM, cut at the video's end and padded with silence to it.
        func pcmFeed(_ m: Media, track: AVAssetTrack, input: AVAssetWriterInput, label: String) throws -> Feed {
            let reader = try AVAssetReader(asset: m.asset)
            let out = AVAssetReaderAudioMixOutput(audioTracks: [track], audioSettings: [
                AVFormatIDKey: kAudioFormatLinearPCM, AVLinearPCMBitDepthKey: 32, AVLinearPCMIsFloatKey: true,
                AVLinearPCMIsBigEndianKey: false, AVLinearPCMIsNonInterleaved: false, AVSampleRateKey: 48000, AVNumberOfChannelsKey: 2,
            ])
            reader.add(out)
            readers.append(reader)
            let endFrame = Int64((end * 48000).rounded())
            var written: Int64 = 0
            var sourceDone = false
            return Feed(input, label: label) {
                if !sourceDone, let b = out.copyNextSampleBuffer() {
                    let n = Int64(CMSampleBufferGetNumSamples(b))
                    if written >= endFrame { sourceDone = true; return nil }
                    if written + n > endFrame {
                        var cut: CMSampleBuffer?
                        CMSampleBufferCopySampleBufferForRange(allocator: kCFAllocatorDefault, sampleBuffer: b,
                                                               sampleRange: CFRange(location: 0, length: Int(endFrame - written)), sampleBufferOut: &cut)
                        written = endFrame
                        sourceDone = true
                        return cut
                    }
                    written += n
                    return b
                }
                sourceDone = true
                guard written < endFrame else { return nil }
                let n = Int(min(endFrame - written, 48000))
                let s = try silenceSample(frames: n, rate: 48000, channels: 2, pts: CMTime(value: written, timescale: 48000))
                written += Int64(n)
                return s
            }
        }
        func aacInput(bitrate: Int) -> AVAssetWriterInput {
            let input = AVAssetWriterInput(mediaType: .audio, outputSettings: [
                AVFormatIDKey: kAudioFormatMPEG4AAC, AVSampleRateKey: 48000, AVNumberOfChannelsKey: 2, AVEncoderBitRateKey: bitrate,
            ])
            input.expectsMediaDataInRealTime = false
            return input
        }

        // The source's own audio, first.
        var audioInputs: [AVAssetWriterInput] = []
        var originals: [AVAssetWriterInput] = []
        if p.keepAudio == true {
            for (i, track) in media.audioTracks.enumerated() {
                let fmt = try await track.load(.formatDescriptions).first
                let codec = fmt.map { CMFormatDescriptionGetMediaSubType($0) } ?? 0
                var lang = (try? await track.load(.extendedLanguageTag)) ?? nil
                if lang == nil { lang = (try? await track.load(.languageCode)) ?? nil }
                let input: AVAssetWriterInput
                let feed: Feed
                if [kAudioFormatMPEG4AAC, kAudioFormatAppleLossless, kAudioFormatAC3, kAudioFormatEnhancedAC3].contains(codec) {
                    input = AVAssetWriterInput(mediaType: .audio, outputSettings: nil, sourceFormatHint: fmt)
                    let reader = try AVAssetReader(asset: media.asset)
                    let out = AVAssetReaderTrackOutput(track: track, outputSettings: nil)
                    reader.add(out)
                    readers.append(reader)
                    feed = Feed(input, label: "original\(i)") {
                        guard let b = out.copyNextSampleBuffer() else { return nil }
                        return CMSampleBufferGetPresentationTimeStamp(b).seconds < end ? b : nil
                    }
                } else {
                    input = aacInput(bitrate: 192000)
                    feed = try pcmFeed(media, track: track, input: input, label: "original\(i)")
                }
                named(input, language: lang, title: nil)
                guard writer.canAdd(input) else { throw EngineError.unsupportedMedia(media.url.path, "its audio cannot go in \(format.uppercased())") }
                writer.add(input)
                audioInputs.append(input)
                originals.append(input)
                feeds.append(feed)
            }
        }
        // The dubbed tracks.
        var defaultAudio: AVAssetWriterInput?
        for (i, t) in p.audio.enumerated() {
            let m = dubbed[i]
            guard let track = m.audioTracks.first else { throw EngineError.unsupportedMedia(m.url.path, "it has no audio") }
            let input = aacInput(bitrate: (t.bitrate ?? 0) > 0 ? t.bitrate! : 192000)
            named(input, language: t.language, title: t.title)
            guard writer.canAdd(input) else { throw RPCError(Code.internal, "Could not add audio track \(i + 1)") }
            writer.add(input)
            audioInputs.append(input)
            if t.default == true, defaultAudio == nil { defaultAudio = input }
            feeds.append(try pcmFeed(m, track: track, input: input, label: "dub\(i)"))
        }
        let firstDub = audioInputs.count > originals.count ? audioInputs[originals.count] : nil
        let enabledAudio = defaultAudio ?? firstDub ?? audioInputs.first
        if audioInputs.count > 1 {
            writer.add(AVAssetWriterInputGroup(inputs: audioInputs, defaultInput: enabledAudio))
        }
        for input in audioInputs { input.marksOutputTrackAsEnabled = input === enabledAudio }

        // Subtitles.
        var subInputs: [AVAssetWriterInput] = []
        var defaultSub: AVAssetWriterInput?
        if !subtitleCues.isEmpty {
            let fmt = try tx3gFormat()
            for (i, s) in (p.subtitles ?? []).enumerated() {
                let input = AVAssetWriterInput(mediaType: .subtitle, outputSettings: nil, sourceFormatHint: fmt)
                input.expectsMediaDataInRealTime = false
                named(input, language: s.language, title: s.title)
                guard writer.canAdd(input) else { throw RPCError(Code.internal, "Could not add subtitle track \(i + 1)") }
                writer.add(input)
                subInputs.append(input)
                if s.default == true, defaultSub == nil { defaultSub = input }
                // Cues, with empty samples in the gaps, up to the video's end.
                var samples: [CMSampleBuffer] = []
                var t = 0.0
                for c in subtitleCues[i] where c.start < end {
                    let a = max(c.start, t), b = min(c.end, end)
                    guard b > a else { continue }
                    if a > t { samples.append(try textSample("", start: t, end: a, format: fmt)) }
                    samples.append(try textSample(c.text, start: a, end: b, format: fmt))
                    t = b
                }
                if t < end { samples.append(try textSample("", start: t, end: end, format: fmt)) }
                var k = 0
                feeds.append(Feed(input, label: "sub\(i)") {
                    defer { k += 1 }
                    return k < samples.count ? samples[k] : nil
                })
            }
            writer.add(AVAssetWriterInputGroup(inputs: subInputs, defaultInput: defaultSub))
            for input in subInputs { input.marksOutputTrackAsEnabled = input === defaultSub }
        }

        // Write.
        let progress = JobProgress(out: out, job: job)
        progress.send("muxing", force: true)
        videoFeed.onTime = { t in
            progress.fraction = min(max(t / max(end, 0.001), 0), 1)
            progress.send("muxing")
        }
        for r in readers {
            guard r.startReading() else { throw EngineError.unsupportedMedia(media.url.path, r.error?.localizedDescription ?? "reading failed") }
        }
        guard writer.startWriting() else { throw RPCError(Code.internal, "Could not start writing: \(describeError(writer.error))") }
        writer.startSession(atSourceTime: .zero)
        let group = DispatchGroup()
        let cancelled = CancelFlag()
        for f in feeds { f.start(cancelled: { cancelled.value }, group: group) }
        await withTaskCancellationHandler {
            await withCheckedContinuation { (k: CheckedContinuation<Void, Never>) in
                group.notify(queue: .global()) { k.resume() }
            }
        } onCancel: {
            cancelled.set()
        }
        if let e = feeds.compactMap(\.error).first {
            writer.cancelWriting()
            try? FileManager.default.removeItem(at: tmp)
            if Task.isCancelled || e is CancellationError { throw CancellationError() }
            throw RPCError(Code.internal, "Could not write \(p.output.path): \(describeError(e)); \(describeError(writer.error))")
        }
        await writer.finishWriting()
        guard writer.status == .completed else {
            try? FileManager.default.removeItem(at: tmp)
            throw RPCError(Code.internal, "Could not write \(p.output.path): \(describeError(writer.error))")
        }
        guard rename(tmp.path, dest.path) == 0 else {
            let e = String(cString: strerror(errno))
            try? FileManager.default.removeItem(at: tmp)
            throw RPCError(Code.internal, "Could not write \(dest.path): \(e)")
        }
        let bytes = ((try? FileManager.default.attributesOfItem(atPath: dest.path)[.size]) as? NSNumber)?.int64Value ?? 0
        return MuxResult(output: dest.path, bytes: bytes, duration: round3(end), tracks: audioInputs.count)
    }
}

/// A thread-safe flag.
final class CancelFlag: @unchecked Sendable {
    private let lock = NSLock()
    private var flag = false
    var value: Bool { lock.withLock { flag } }
    func set() { lock.withLock { flag = true } }
}
