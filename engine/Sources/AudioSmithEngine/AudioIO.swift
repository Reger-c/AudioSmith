import AudioSmithProtocol
import AVFoundation
import CoreMedia
import Foundation

// Reading media with AVFoundation: media.probe, media.decode, and the
// inputs of the gpu jobs (references, recordings to transcribe…), which
// may be any format AVFoundation reads, video included.

/// Checks an input path: absolute and existing.
func inputURL(_ path: String?) throws -> URL {
    guard let path, !path.isEmpty else { throw EngineError.badRequest("Missing input") }
    guard path.hasPrefix("/") else { throw EngineError.badRequest("\(path) is not an absolute path") }
    guard FileManager.default.fileExists(atPath: path) else { throw EngineError.fileNotFound(path) }
    return URL(fileURLWithPath: path)
}

/// Checks an output path and its format.
func outputURL(_ o: Output) throws -> (URL, Int) {
    guard o.path.hasPrefix("/") else { throw EngineError.badRequest("output.path must be an absolute path") }
    guard (o.format ?? "wav") == "wav" else { throw EngineError.badRequest("Unsupported output format \(o.format!)") }
    let bits = o.bits ?? 16
    guard [16, 24, 32].contains(bits) else { throw EngineError.badRequest("Unsupported bits \(bits)") }
    return (URL(fileURLWithPath: o.path), bits)
}

private func fourCC(_ code: FourCharCode) -> String {
    let bytes = [24, 16, 8, 0].map { UInt8((code >> $0) & 0xFF) }
    return String(decoding: bytes, as: UTF8.self).trimmingCharacters(in: .whitespaces)
}

private func audioCodecName(_ id: AudioFormatID) -> String {
    switch id {
    case kAudioFormatLinearPCM: "lpcm"
    case kAudioFormatMPEG4AAC, kAudioFormatMPEG4AAC_HE, kAudioFormatMPEG4AAC_HE_V2, kAudioFormatMPEG4AAC_LD, kAudioFormatMPEG4AAC_ELD: "aac"
    case kAudioFormatMPEGLayer3: "mp3"
    case kAudioFormatAppleLossless: "alac"
    case kAudioFormatFLAC: "flac"
    case kAudioFormatOpus: "opus"
    case kAudioFormatAC3: "ac3"
    case kAudioFormatEnhancedAC3: "eac3"
    case kAudioFormatULaw: "ulaw"
    case kAudioFormatALaw: "alaw"
    default: fourCC(id)
    }
}

private func videoCodecName(_ sub: FourCharCode) -> String {
    switch sub {
    case kCMVideoCodecType_H264: "h264"
    case kCMVideoCodecType_HEVC, kCMVideoCodecType_HEVCWithAlpha: "hevc"
    case kCMVideoCodecType_AppleProRes422, kCMVideoCodecType_AppleProRes4444, kCMVideoCodecType_AppleProRes422HQ,
         kCMVideoCodecType_AppleProRes422LT, kCMVideoCodecType_AppleProRes422Proxy: "prores"
    case kCMVideoCodecType_MPEG4Video: "mpeg4"
    case kCMVideoCodecType_VP9: "vp9"
    case kCMVideoCodecType_AV1: "av1"
    default: fourCC(sub)
    }
}

/// An opened media file: its asset, tracks and description.
struct Media {
    let url: URL
    let asset: AVURLAsset
    let audioTracks: [AVAssetTrack]
    let probe: ProbeResult
}

/// Opens path with AVFoundation: file_not_found, or unsupported_media when
/// AVFoundation cannot read it.
func openMedia(_ path: String?) async throws -> Media {
    let url = try inputURL(path)
    let asset = AVURLAsset(url: url, options: [AVURLAssetPreferPreciseDurationAndTimingKey: true])
    let audio: [AVAssetTrack]
    let video: [AVAssetTrack]
    let duration: CMTime
    do {
        guard try await asset.load(.isReadable) else { throw EngineError.unsupportedMedia(url.path, "AVFoundation cannot read it") }
        audio = try await asset.loadTracks(withMediaType: .audio)
        video = try await asset.loadTracks(withMediaType: .video)
        duration = try await asset.load(.duration)
    } catch let e as EngineError {
        throw e
    } catch {
        throw EngineError.unsupportedMedia(url.path, error.localizedDescription)
    }
    if audio.isEmpty && video.isEmpty {
        throw EngineError.unsupportedMedia(url.path, "it has no audio or video")
    }
    var ext = url.pathExtension.lowercased()
    if ext == "wave" { ext = "wav" }
    if ext == "aif" || ext == "aifc" { ext = "aiff" }
    var p = ProbeResult(format: ext.isEmpty ? "unknown" : ext, duration: duration.isNumeric ? duration.seconds : 0,
                        hasAudio: !audio.isEmpty, hasVideo: !video.isEmpty,
                        audioTracks: audio.isEmpty ? nil : audio.count)
    if let track = audio.first {
        if let desc = try? await track.load(.formatDescriptions).first,
           let asbd = CMAudioFormatDescriptionGetStreamBasicDescription(desc)?.pointee {
            p.codec = audioCodecName(asbd.mFormatID)
            p.sampleRate = Int(asbd.mSampleRate)
            p.channels = Int(asbd.mChannelsPerFrame)
            if asbd.mFormatID == kAudioFormatLinearPCM, asbd.mBitsPerChannel > 0 { p.bits = Int(asbd.mBitsPerChannel) }
        }
        if let rate = try? await track.load(.estimatedDataRate), rate > 0 { p.bitRate = Int(rate) }
    }
    if let track = video.first {
        let size = (try? await track.load(.naturalSize)) ?? .zero
        let fps = (try? await track.load(.nominalFrameRate)) ?? 0
        var codec = "unknown"
        if let desc = try? await track.load(.formatDescriptions).first {
            codec = videoCodecName(CMFormatDescriptionGetMediaSubType(desc))
        }
        p.video = VideoInfo(codec: codec, width: Int(abs(size.width)), height: Int(abs(size.height)), fps: (Double(fps) * 100).rounded() / 100)
    }
    return Media(url: url, asset: asset, audioTracks: audio, probe: p)
}

/// Reads a window of an audio track as interleaved float samples at
/// sampleRate/channels (nil keeps the source's), calling sink with each
/// buffer and progress with the fraction read. Cancellable.
func readAudio(
    _ media: Media, sampleRate: Int?, channels: Int?, start: Double = 0, duration: Double = 0, track: Int = 0,
    progress: ((Double) -> Void)? = nil, sink: ([Float]) throws -> Void
) throws -> (sampleRate: Int, channels: Int) {
    guard !media.audioTracks.isEmpty else { throw EngineError.unsupportedMedia(media.url.path, "it has no audio") }
    guard track >= 0, track < media.audioTracks.count else {
        throw EngineError.badRequest("\(media.url.lastPathComponent) has \(media.audioTracks.count) audio track(s), not \(track + 1)")
    }
    let rate = sampleRate ?? media.probe.sampleRate ?? 48000
    let ch = channels ?? min(media.probe.channels ?? 2, 2)
    let reader: AVAssetReader
    do {
        reader = try AVAssetReader(asset: media.asset)
    } catch {
        throw EngineError.unsupportedMedia(media.url.path, error.localizedDescription)
    }
    let settings: [String: Any] = [
        AVFormatIDKey: kAudioFormatLinearPCM,
        AVLinearPCMBitDepthKey: 32,
        AVLinearPCMIsFloatKey: true,
        AVLinearPCMIsBigEndianKey: false,
        AVLinearPCMIsNonInterleaved: false,
        AVSampleRateKey: rate,
        AVNumberOfChannelsKey: ch,
    ]
    let output = AVAssetReaderAudioMixOutput(audioTracks: [media.audioTracks[track]], audioSettings: settings)
    output.alwaysCopiesSampleData = false
    guard reader.canAdd(output) else { throw EngineError.unsupportedMedia(media.url.path, "its audio cannot be converted") }
    reader.add(output)
    let total = media.probe.duration
    if start > 0 || duration > 0 {
        let from = CMTime(seconds: max(start, 0), preferredTimescale: 48000)
        let length = duration > 0 ? CMTime(seconds: duration, preferredTimescale: 48000) : .positiveInfinity
        reader.timeRange = CMTimeRange(start: from, duration: length)
    }
    let span = duration > 0 ? duration : max(total - start, 0.001)
    guard reader.startReading() else {
        throw EngineError.unsupportedMedia(media.url.path, reader.error?.localizedDescription ?? "reading failed")
    }
    defer { if reader.status == .reading { reader.cancelReading() } }
    while let buffer = output.copyNextSampleBuffer() {
        if Task.isCancelled { throw CancellationError() }
        guard let block = CMSampleBufferGetDataBuffer(buffer) else { continue }
        let length = CMBlockBufferGetDataLength(block)
        var floats = [Float](repeating: 0, count: length / 4)
        let status = floats.withUnsafeMutableBytes { raw in
            CMBlockBufferCopyDataBytes(block, atOffset: 0, dataLength: length, destination: raw.baseAddress!)
        }
        guard status == kCMBlockBufferNoErr else { continue }
        try sink(floats)
        if let progress {
            let t = CMSampleBufferGetPresentationTimeStamp(buffer).seconds - start
            progress(min(max(t / span, 0), 1))
        }
    }
    if reader.status == .failed {
        throw EngineError.unsupportedMedia(media.url.path, reader.error?.localizedDescription ?? "reading failed")
    }
    return (rate, ch)
}

/// Decodes path to mono samples at sampleRate (inputs of the gpu jobs).
func decodeMono(_ path: String?, sampleRate: Int) async throws -> [Float] {
    let media = try await openMedia(path)
    var samples: [Float] = []
    _ = try readAudio(media, sampleRate: sampleRate, channels: 1) { samples.append(contentsOf: $0) }
    if samples.isEmpty { throw EngineError.unsupportedMedia(media.url.path, "it has no audio samples") }
    return samples
}

/// The media worker's jobs.
final class MediaWorker: @unchecked Sendable {
    let out: Writer

    init(out: Writer) {
        self.out = out
    }

    func probe(_ p: ProbeParams) async throws -> ProbeResult {
        try await openMedia(p.input).probe
    }

    func decode(_ p: DecodeParams, job: String) async throws -> DecodeResult {
        let (url, bits) = try outputURL(p.output)
        let media = try await openMedia(p.input)
        guard media.probe.hasAudio else { throw EngineError.unsupportedMedia(media.url.path, "it has no audio") }
        if let ch = p.channels, ch != 0, !(1 ... 8).contains(ch) { throw EngineError.badRequest("channels \(ch)") }
        let progress = JobProgress(out: out, job: job)
        progress.send("decoding", force: true)
        let started = Date()
        let rate = (p.sampleRate ?? 0) > 0 ? p.sampleRate : nil
        let channels = (p.channels ?? 0) > 0 ? p.channels : nil
        let outRate = rate ?? media.probe.sampleRate ?? 48000
        let outCh = channels ?? min(media.probe.channels ?? 2, 2)
        let writer = try WAVWriter(url: url, sampleRate: outRate, channels: outCh, bits: bits)
        do {
            _ = try readAudio(media, sampleRate: outRate, channels: outCh, start: p.start ?? 0, duration: p.duration ?? 0,
                              track: p.track ?? 0, progress: { f in
                                  progress.fraction = f
                                  progress.send("decoding")
                              }, sink: { try writer.append($0[...]) })
            try Task.checkCancellation()
            progress.fraction = 1
            progress.send("decoding", force: true)
            try writer.finish()
        } catch {
            writer.cancel()
            throw error
        }
        return DecodeResult(output: url.path, sampleRate: outRate, channels: outCh, frames: writer.frames,
                            audioSeconds: Double(writer.frames) / Double(outRate), source: media.probe,
                            seconds: Date().timeIntervalSince(started))
    }
}
