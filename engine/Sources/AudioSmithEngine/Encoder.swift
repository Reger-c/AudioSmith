import AudioSmithProtocol
import AVFoundation
import CoreMedia
import Foundation

// media.encode (M3): a WAV into an M4B/M4A audiobook — AAC-LC through
// AVAssetWriter, a QuickTime chapter track (a text track the audio track
// refers to with a "chap" reference: what Apple Books, QuickTime and
// AVFoundation read), iTunes tags and cover art.

extension MediaWorker {
    func encode(_ p: EncodeParams, job: String) async throws -> EncodeResult {
        let format = (p.output.format ?? "m4b").lowercased()
        switch format {
        case "m4b", "m4a": break
        case "mp3": throw EngineError.unsupported("This engine cannot encode MP3", capability: "mp3")
        default: throw EngineError.badRequest("Unknown output format \(format)")
        }
        guard (p.codec ?? "aac") == "aac" else { throw EngineError.badRequest("Unknown codec \(p.codec!)") }
        guard p.output.path.hasPrefix("/") else { throw EngineError.badRequest("output.path must be an absolute path") }
        let media = try await openMedia(p.input)
        guard media.probe.hasAudio else { throw EngineError.unsupportedMedia(media.url.path, "it has no audio") }
        var cover: (Data, AVMetadataExtraAttributeKey?)?
        if let path = nonEmpty(p.cover) {
            let url = try inputURL(path)
            cover = (try Data(contentsOf: url), nil)
        }
        let channels = min(max(media.probe.channels ?? 1, 1), 2)
        var rate = (p.sampleRate ?? 0) > 0 ? p.sampleRate! : (media.probe.sampleRate ?? 44100)
        rate = min(rate, 48000)
        let bitrate = (p.bitrate ?? 0) > 0 ? p.bitrate! : (channels == 1 ? 64000 : 96000)
        let duration = media.probe.duration

        let dest = URL(fileURLWithPath: p.output.path)
        let dir = dest.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let tmp = dir.appendingPathComponent(".\(dest.deletingPathExtension().lastPathComponent).\(getpid()).m4a")
        try? FileManager.default.removeItem(at: tmp)
        let writer = try AVAssetWriter(outputURL: tmp, fileType: .m4a)
        writer.metadata = Self.tags(p.metadata, cover: cover?.0)

        let audio = AVAssetWriterInput(mediaType: .audio, outputSettings: [
            AVFormatIDKey: kAudioFormatMPEG4AAC, AVSampleRateKey: rate, AVNumberOfChannelsKey: channels, AVEncoderBitRateKey: bitrate,
        ])
        audio.expectsMediaDataInRealTime = false
        guard writer.canAdd(audio) else { throw RPCError(Code.internal, "The AAC encoder refused \(rate) Hz, \(channels) channel(s), \(bitrate) b/s") }
        writer.add(audio)
        let chapters = (p.chapters ?? []).filter { $0.end > $0.start }.sorted { $0.start < $1.start }
        var text: AVAssetWriterInput?
        var textFormat: CMFormatDescription?
        if !chapters.isEmpty {
            textFormat = try Self.chapterFormat()
            let t = AVAssetWriterInput(mediaType: .text, outputSettings: nil, sourceFormatHint: textFormat)
            t.marksOutputTrackAsEnabled = false
            t.expectsMediaDataInRealTime = false
            // AVFoundation (and Apple Books) looks chapters up by language;
            // "und" ones are not found by a language lookup.
            t.languageCode = "eng"
            guard writer.canAdd(t) else { throw RPCError(Code.internal, "Could not add the chapter track") }
            writer.add(t)
            audio.addTrackAssociation(withTrackOf: t, type: AVAssetTrack.AssociationType.chapterList.rawValue)
            text = t
        }

        let progress = JobProgress(out: out, job: job)
        progress.send("encoding", force: true)
        let started = Date()
        guard writer.startWriting() else {
            throw RPCError(Code.internal, "Could not start writing: \(writer.error?.localizedDescription ?? "unknown")")
        }
        writer.startSession(atSourceTime: .zero)

        // AVAssetWriter interleaves its tracks: past a point it stops taking
        // audio until the chapter track has a sample further on, and the
        // other way round. So chapters are appended as soon as the writer
        // takes them (never blocking on the text track while audio waits),
        // and every wait gives up when the writer makes no progress for a
        // minute instead of spinning forever (books of a few minutes with
        // chapters 45 s apart hung here: v0.2 verification).
        var next = 0
        func appendChapter() throws {
            guard let textFormat, let text else { return }
            let c = chapters[next]
            let end = next + 1 < chapters.count ? chapters[next + 1].start : max(c.end, duration)
            let sample = try Self.chapterSample(c.title, start: c.start, end: max(end, c.start + 0.001), format: textFormat)
            guard text.append(sample) else { throw RPCError(Code.internal, "Could not write chapter \(next + 1): \(describeError(writer.error))") }
            next += 1
            // Finished as soon as the last chapter is in: until then the
            // writer holds the audio back a few seconds past the last
            // chapter start, waiting for more text.
            if next == chapters.count { text.markAsFinished() }
        }
        /// Appends the chapters starting by t that the writer takes now.
        func feedChapters(upTo t: Double) throws {
            guard let text else { return }
            while next < chapters.count, chapters[next].start <= t, text.isReadyForMoreMediaData {
                try appendChapter()
            }
        }
        /// Waits until input takes more data; while audio waits, the writer
        /// may want the next chapter first.
        func waitReady(_ input: AVAssetWriterInput, what: String) throws {
            var since = Date()
            var fed = next
            while !input.isReadyForMoreMediaData {
                if Task.isCancelled { throw CancellationError() }
                if writer.status == .failed { throw RPCError(Code.internal, "Encoding failed: \(describeError(writer.error))") }
                if input === audio { try feedChapters(upTo: .infinity) }
                if next != fed { fed = next; since = Date() }
                if Date().timeIntervalSince(since) > 60 {
                    throw RPCError(Code.internal, "The encoder stopped taking \(what) (chapter \(next) of \(chapters.count))")
                }
                Thread.sleep(forTimeInterval: 0.002)
            }
        }

        do {
            let reader = try AVAssetReader(asset: media.asset)
            let output = AVAssetReaderAudioMixOutput(audioTracks: [media.audioTracks[0]], audioSettings: [
                AVFormatIDKey: kAudioFormatLinearPCM, AVLinearPCMBitDepthKey: 32, AVLinearPCMIsFloatKey: true,
                AVLinearPCMIsBigEndianKey: false, AVLinearPCMIsNonInterleaved: false,
                AVSampleRateKey: rate, AVNumberOfChannelsKey: channels,
            ])
            reader.add(output)
            guard reader.startReading() else { throw EngineError.unsupportedMedia(media.url.path, reader.error?.localizedDescription ?? "reading failed") }
            while let buffer = output.copyNextSampleBuffer() {
                if Task.isCancelled { throw CancellationError() }
                let t = CMSampleBufferGetPresentationTimeStamp(buffer).seconds
                try feedChapters(upTo: .infinity) // all of them, as soon as the writer takes them
                try waitReady(audio, what: "audio")
                guard audio.append(buffer) else { throw RPCError(Code.internal, "AAC encoding failed: \(describeError(writer.error))") }
                if duration > 0 {
                    progress.fraction = min(t / duration, 1)
                    progress.send("encoding")
                }
            }
            if reader.status == .failed { throw EngineError.unsupportedMedia(media.url.path, reader.error?.localizedDescription ?? "reading failed") }
            audio.markAsFinished()
            if let text {
                while next < chapters.count {
                    try waitReady(text, what: "chapters")
                    try appendChapter()
                }
            }
            progress.fraction = 1
            progress.send("writing", force: true)
            await writer.finishWriting()
            guard writer.status == .completed else {
                throw RPCError(Code.internal, "Could not write \(p.output.path): \(describeError(writer.error))")
            }
        } catch {
            writer.cancelWriting()
            try? FileManager.default.removeItem(at: tmp)
            throw error
        }
        guard rename(tmp.path, dest.path) == 0 else {
            let e = String(cString: strerror(errno))
            try? FileManager.default.removeItem(at: tmp)
            throw RPCError(Code.internal, "Could not write \(dest.path): \(e)")
        }
        let bytes = ((try? FileManager.default.attributesOfItem(atPath: dest.path)[.size]) as? NSNumber)?.int64Value ?? 0
        _ = started
        return EncodeResult(output: dest.path, format: format, bytes: bytes, duration: (duration * 1000).rounded() / 1000, bitrate: bitrate)
    }

    /// The iTunes tags: ©nam, ©alb = title; ©ART, aART = author; ©wrt =
    /// narrator; ©day; ©gen; desc and ©cmt = description; covr.
    static func tags(_ m: EncodeMetadata, cover: Data?) -> [AVMetadataItem] {
        var items: [AVMetadataItem] = []
        func add(_ key: AVMetadataKey, _ value: String?) {
            guard let v = nonEmpty(value) else { return }
            let item = AVMutableMetadataItem()
            item.keySpace = .iTunes
            item.key = key.rawValue as NSString
            item.value = v as NSString
            items.append(item)
        }
        add(.iTunesMetadataKeySongName, m.title)
        add(.iTunesMetadataKeyAlbum, m.title)
        add(.iTunesMetadataKeyArtist, m.author)
        add(.iTunesMetadataKeyAlbumArtist, m.author)
        add(.iTunesMetadataKeyComposer, m.narrator)
        add(.iTunesMetadataKeyReleaseDate, m.year)
        add(.iTunesMetadataKeyUserGenre, m.genre)
        add(.iTunesMetadataKeyDescription, m.description)
        add(.iTunesMetadataKeyUserComment, m.description)
        if let cover {
            let item = AVMutableMetadataItem()
            item.keySpace = .iTunes
            item.key = AVMetadataKey.iTunesMetadataKeyCoverArt.rawValue as NSString
            item.value = cover as NSData
            let png = cover.starts(with: [0x89, 0x50, 0x4E, 0x47])
            item.dataType = (png ? kCMMetadataBaseDataType_PNG : kCMMetadataBaseDataType_JPEG) as String
            items.append(item)
        }
        return items
    }

    /// A QuickTime 'text' sample description for chapter titles, built as
    /// the file format lays it out (big-endian): the CMTextFormatDescription
    /// extension keys produce one AVAssetWriter rejects (-12715).
    static func chapterFormat() throws -> CMFormatDescription {
        var d: [UInt8] = []
        func u32(_ v: UInt32) { d += [UInt8(v >> 24), UInt8((v >> 16) & 0xFF), UInt8((v >> 8) & 0xFF), UInt8(v & 0xFF)] }
        func u16(_ v: UInt16) { d += [UInt8(v >> 8), UInt8(v & 0xFF)] }
        u32(0); d += Array("text".utf8); d += [0, 0, 0, 0, 0, 0]; u16(1) // size (set below), type, reserved, data reference
        u32(0) // display flags
        u32(0) // justification
        u16(0); u16(0); u16(0) // background colour
        u16(0); u16(0); u16(0); u16(0) // default text box
        d += [UInt8](repeating: 0, count: 8) // reserved
        u16(0); u16(0) // font number, face
        d += [0]; u16(0) // reserved
        u16(0); u16(0); u16(0) // foreground colour
        d += [0] // text name: an empty Pascal string
        let n = UInt32(d.count)
        d[0] = UInt8(n >> 24); d[1] = UInt8((n >> 16) & 0xFF); d[2] = UInt8((n >> 8) & 0xFF); d[3] = UInt8(n & 0xFF)
        var fd: CMFormatDescription?
        let status = d.withUnsafeBufferPointer {
            CMTextFormatDescriptionCreateFromBigEndianTextDescriptionData(allocator: kCFAllocatorDefault, bigEndianTextDescriptionData: $0.baseAddress!,
                                                                         size: $0.count, flavor: nil, mediaType: kCMMediaType_Text, formatDescriptionOut: &fd)
        }
        guard status == noErr, let fd else { throw RPCError(Code.internal, "Could not describe the chapter track (\(status))") }
        return fd
    }

    /// One chapter title as a QuickTime text sample: a 16-bit big-endian
    /// length, the UTF-8 text, and an 'encd' atom saying UTF-8.
    static func chapterSample(_ title: String, start: Double, end: Double, format: CMFormatDescription) throws -> CMSampleBuffer {
        let utf8 = Array(title.utf8.prefix(65_000))
        var bytes: [UInt8] = [UInt8(utf8.count >> 8), UInt8(utf8.count & 0xFF)] + utf8
        bytes += [0, 0, 0, 12, 0x65, 0x6E, 0x63, 0x64, 0, 0, 0x01, 0x00] // 'encd': kCFStringEncodingUTF8
        var block: CMBlockBuffer?
        var status = CMBlockBufferCreateWithMemoryBlock(allocator: kCFAllocatorDefault, memoryBlock: nil, blockLength: bytes.count,
                                                        blockAllocator: kCFAllocatorDefault, customBlockSource: nil, offsetToData: 0,
                                                        dataLength: bytes.count, flags: kCMBlockBufferAssureMemoryNowFlag, blockBufferOut: &block)
        guard status == noErr, let block else { throw RPCError(Code.internal, "chapter sample (\(status))") }
        status = bytes.withUnsafeBytes { CMBlockBufferReplaceDataBytes(with: $0.baseAddress!, blockBuffer: block, offsetIntoDestination: 0, dataLength: bytes.count) }
        guard status == noErr else { throw RPCError(Code.internal, "chapter sample (\(status))") }
        var timing = CMSampleTimingInfo(duration: CMTime(seconds: end - start, preferredTimescale: 1000),
                                        presentationTimeStamp: CMTime(seconds: start, preferredTimescale: 1000), decodeTimeStamp: .invalid)
        var size = bytes.count
        var sample: CMSampleBuffer?
        status = CMSampleBufferCreate(allocator: kCFAllocatorDefault, dataBuffer: block, dataReady: true, makeDataReadyCallback: nil,
                                      refcon: nil, formatDescription: format, sampleCount: 1, sampleTimingEntryCount: 1,
                                      sampleTimingArray: &timing, sampleSizeEntryCount: 1, sampleSizeArray: &size, sampleBufferOut: &sample)
        guard status == noErr, let sample else { throw RPCError(Code.internal, "chapter sample (\(status))") }
        return sample
    }
}

/// An NSError with its domain, code and underlying errors, for logs.
func describeError(_ error: Error?) -> String {
    guard let e = error as NSError? else { return "unknown error" }
    var s = "\(e.localizedDescription) (\(e.domain) \(e.code))"
    if let u = e.userInfo[NSUnderlyingErrorKey] as? NSError {
        s += " ← " + describeError(u)
    }
    if let r = e.localizedFailureReason { s += ": \(r)" }
    return s
}
