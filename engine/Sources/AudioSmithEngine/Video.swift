import AudioSmithProtocol
import AVFoundation
import CoreGraphics
import Foundation
import ImageIO
import UniformTypeIdentifiers

// media.thumbnail and media.transcode (M4).

extension MediaWorker {
    func thumbnail(_ p: ThumbnailParams, job: String) async throws -> ThumbnailResult {
        guard !p.times.isEmpty, p.times.count == p.outputs.count else { throw EngineError.badRequest("times and outputs must have the same length") }
        for o in p.outputs where !o.hasPrefix("/") { throw EngineError.badRequest("\(o) is not an absolute path") }
        let media = try await openMedia(p.input)
        guard try await !media.asset.loadTracks(withMediaType: .video).isEmpty else {
            throw EngineError.unsupportedMedia(media.url.path, "it has no video track")
        }
        let duration = try await media.asset.load(.duration).seconds
        let gen = AVAssetImageGenerator(asset: media.asset)
        gen.appliesPreferredTrackTransform = true
        let tolerance = CMTime(seconds: 0.5, preferredTimescale: 600)
        gen.requestedTimeToleranceBefore = tolerance
        gen.requestedTimeToleranceAfter = tolerance
        if let w = p.width, w > 0 { gen.maximumSize = CGSize(width: w, height: 0) }
        var frames: [Frame] = []
        let progress = JobProgress(out: out, job: job)
        for (i, t) in p.times.enumerated() {
            try Task.checkCancellation()
            let at = min(max(t, 0), max(duration - 0.05, 0))
            let (image, actual) = try await gen.image(at: CMTime(seconds: at, preferredTimescale: 600))
            let url = URL(fileURLWithPath: p.outputs[i])
            try FileManager.default.createDirectory(at: url.deletingLastPathComponent(), withIntermediateDirectories: true)
            let png = url.pathExtension.lowercased() == "png"
            let tmp = url.deletingLastPathComponent().appendingPathComponent(".\(url.lastPathComponent).\(getpid())")
            guard let dest = CGImageDestinationCreateWithURL(tmp as CFURL, (png ? UTType.png : UTType.jpeg).identifier as CFString, 1, nil) else {
                throw RPCError(Code.internal, "Could not write \(url.path)")
            }
            CGImageDestinationAddImage(dest, image, png ? nil : [kCGImageDestinationLossyCompressionQuality: 0.8] as CFDictionary)
            guard CGImageDestinationFinalize(dest), rename(tmp.path, url.path) == 0 else {
                try? FileManager.default.removeItem(at: tmp)
                throw RPCError(Code.internal, "Could not write \(url.path)")
            }
            frames.append(Frame(time: round3(actual.seconds), path: url.path, width: image.width, height: image.height))
            progress.fraction = Double(i + 1) / Double(p.times.count)
            progress.send("thumbnails")
        }
        return ThumbnailResult(frames: frames)
    }

    func transcode(_ p: TranscodeParams, job: String) async throws -> TranscodeResult {
        guard p.output.path.hasPrefix("/") else { throw EngineError.badRequest("output.path must be an absolute path") }
        guard (p.output.format ?? "mp4").lowercased() == "mp4" else { throw EngineError.badRequest("Unknown output format \(p.output.format!)") }
        let media = try await openMedia(p.input)
        let videoTracks = try await media.asset.loadTracks(withMediaType: .video)
        guard let video = videoTracks.first else { throw EngineError.unsupportedMedia(media.url.path, "it has no video track") }
        guard try await video.load(.isDecodable) else { throw EngineError.unsupportedMedia(media.url.path, "its video cannot be decoded") }
        let maxHeight = (p.maxHeight ?? 0) > 0 ? p.maxHeight! : 720
        let preset = switch maxHeight {
        case ..<540: AVAssetExportPreset640x480
        case ..<720: AVAssetExportPreset960x540
        case ..<1080: AVAssetExportPreset1280x720
        default: AVAssetExportPreset1920x1080
        }
        // Without audio: a composition of the video track alone.
        var asset: AVAsset = media.asset
        if p.noAudio == true {
            let comp = AVMutableComposition()
            let duration = try await media.asset.load(.duration)
            guard let track = comp.addMutableTrack(withMediaType: .video, preferredTrackID: kCMPersistentTrackID_Invalid) else {
                throw RPCError(Code.internal, "Could not make the composition")
            }
            try track.insertTimeRange(CMTimeRange(start: .zero, duration: duration), of: video, at: .zero)
            track.preferredTransform = try await video.load(.preferredTransform)
            asset = comp
        }
        guard let session = AVAssetExportSession(asset: asset, presetName: preset) else {
            throw EngineError.unsupportedMedia(media.url.path, "it cannot be exported as \(preset)")
        }
        let dest = URL(fileURLWithPath: p.output.path)
        let dir = dest.deletingLastPathComponent()
        try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
        let tmp = dir.appendingPathComponent(".\(dest.deletingPathExtension().lastPathComponent).\(getpid()).mp4")
        try? FileManager.default.removeItem(at: tmp)
        let progress = JobProgress(out: out, job: job)
        progress.send("transcoding", force: true)
        let watcher = Task {
            for await state in session.states(updateInterval: 0.25) {
                if case .exporting(let p) = state {
                    progress.fraction = p.fractionCompleted
                    progress.send("transcoding")
                }
            }
        }
        defer { watcher.cancel() }
        do {
            try await session.export(to: tmp, as: .mp4)
        } catch {
            try? FileManager.default.removeItem(at: tmp)
            if Task.isCancelled { throw CancellationError() }
            throw EngineError.unsupportedMedia(media.url.path, describeError(error))
        }
        guard rename(tmp.path, dest.path) == 0 else {
            try? FileManager.default.removeItem(at: tmp)
            throw RPCError(Code.internal, "Could not write \(dest.path)")
        }
        let result = AVURLAsset(url: dest)
        var size = CGSize.zero
        if let t = try await result.loadTracks(withMediaType: .video).first {
            let natural = try await t.load(.naturalSize)
            let transform = try await t.load(.preferredTransform)
            let r = CGRect(origin: .zero, size: natural).applying(transform)
            size = CGSize(width: abs(r.width), height: abs(r.height))
        }
        let bytes = ((try? FileManager.default.attributesOfItem(atPath: dest.path)[.size]) as? NSNumber)?.int64Value ?? 0
        return TranscodeResult(output: dest.path, bytes: bytes, duration: round3(try await result.load(.duration).seconds),
                               width: Int(size.width), height: Int(size.height))
    }
}
