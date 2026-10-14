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

        let out = URL(fileURLWithPath: p.output.path)
        let dir = out.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let tmp = dir.appendingPathComponent(".\(out.deletingPathExtension().lastPathComponent).\(getpid()).m4a")
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

        // Chapters are appended as the audio passes their starts, so the
        // writer can interleave; the last runs to the end of the book.
        var next = 0
        func feedChapters(upTo t: Double) throws {
            guard let text, let textFormat else { return }
            while next < chapters.count, chapters[next].start <= t {
                let c = chapters[next]
                let end = next + 1 < chapters.count ? chapters[next + 1].start : max(c.end, duration)
                let sample = try Self.chapterSample(c.title, start: c.start, end: max(end, c.start + 0.001), format: textFormat)
                while !text.isReadyForMoreMediaData {
                    if Task.isCancelled { throw CancellationError() }
                    Thread.sleep(forTimeInterval: 0.002)
                }
                guard text.append(sample) else { throw RPCError(Code.internal, "Could not write chapter \(next + 1): \(writer.error?.localizedDescription ?? "")") }
                next += 1
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
                try feedChapters(upTo: t)
                while !audio.isReadyForMoreMediaData {
                    if Task.isCancelled { throw CancellationError() }
                    Thread.sleep(forTimeInterval: 0.002)
                }
                guard audio.append(buffer) else { throw RPCError(Code.internal, "AAC encoding failed: \(writer.error?.localizedDescription ?? "")") }
                if duration > 0 {
                    progress.fraction = min(t / duration, 1)
                    progress.send("encoding")
                }
            }
            if reader.status == .failed { throw EngineError.unsupportedMedia(media.url.path, reader.error?.localizedDescription ?? "reading failed") }
            try feedChapters(upTo: .infinity)
            audio.markAsFinished()
            text?.markAsFinished()
            progress.fraction = 1
            progress.send("writing", force: true)
            await writer.finishWriting()
            guard writer.status == .completed else {
                throw RPCError(Code.internal, "Could not write \(p.output.path): \(writer.error?.localizedDescription ?? "unknown")")
            }
        } catch {
            writer.cancelWriting()
            try? FileManager.default.removeItem(at: tmp)
            throw error
        }
        guard rename(tmp.path, out.path) == 0 else {
            let e = String(cString: strerror(errno))
            try? FileManager.default.removeItem(at: tmp)
            throw RPCError(Code.internal, "Could not write \(out.path): \(e)")
        }
        let bytes = ((try? FileManager.default.attributesOfItem(atPath: out.path)[.size]) as? NSNumber)?.int64Value ?? 0
        _ = started
        return EncodeResult(output: out.path, format: format, bytes: bytes, duration: (duration * 1000).rounded() / 1000, bitrate: bitrate)
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

    /// A QuickTime text sample description for chapter titles.
    static func chapterFormat() throws -> CMFormatDescription {
        let ext: [String: Any] = [
            kCMTextFormatDescriptionExtension_DisplayFlags as String: 0,
            kCMTextFormatDescriptionExtension_BackgroundColor as String: [
                kCMTextFormatDescriptionColor_Red as String: 0, kCMTextFormatDescriptionColor_Green as String: 0,
                kCMTextFormatDescriptionColor_Blue as String: 0, kCMTextFormatDescriptionColor_Alpha as String: 255,
            ],
            kCMTextFormatDescriptionExtension_DefaultTextBox as String: [
                kCMTextFormatDescriptionRect_Top as String: 0, kCMTextFormatDescriptionRect_Left as String: 0,
                kCMTextFormatDescriptionRect_Bottom as String: 0, kCMTextFormatDescriptionRect_Right as String: 0,
            ],
            kCMTextFormatDescriptionExtension_DefaultStyle as String: [
                kCMTextFormatDescriptionStyle_StartChar as String: 0, kCMTextFormatDescriptionStyle_EndChar as String: 0,
                kCMTextFormatDescriptionStyle_Font as String: 1, kCMTextFormatDescriptionStyle_FontFace as String: 0,
                kCMTextFormatDescriptionStyle_ForegroundColor as String: [
                    kCMTextFormatDescriptionColor_Red as String: 255, kCMTextFormatDescriptionColor_Green as String: 255,
                    kCMTextFormatDescriptionColor_Blue as String: 255, kCMTextFormatDescriptionColor_Alpha as String: 255,
                ],
                kCMTextFormatDescriptionStyle_FontSize as String: 12,
            ],
            kCMTextFormatDescriptionExtension_FontTable as String: ["1": "Sans-Serif"],
        ]
        var fd: CMFormatDescription?
        let status = CMFormatDescriptionCreate(allocator: kCFAllocatorDefault, mediaType: kCMMediaType_Text,
                                               mediaSubType: kCMTextFormatType_QTText, extensions: ext as CFDictionary,
                                               formatDescriptionOut: &fd)
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
