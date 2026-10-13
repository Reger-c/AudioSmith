import AudioSmithProtocol
import AVFoundation
import CoreAudio
import Foundation

// The listen worker's microphone: capture.devices, capture.start (a job
// that records until capture.stop), with an input meter about 20 times a
// second.

// MARK: Devices (Core Audio)

private func property<T>(_ object: AudioObjectID, _ selector: AudioObjectPropertySelector,
                         scope: AudioObjectPropertyScope = kAudioObjectPropertyScopeGlobal, _ value: inout T) -> Bool {
    var address = AudioObjectPropertyAddress(mSelector: selector, mScope: scope, mElement: kAudioObjectPropertyElementMain)
    var size = UInt32(MemoryLayout<T>.size)
    return AudioObjectGetPropertyData(object, &address, 0, nil, &size, &value) == noErr
}

private func stringProperty(_ object: AudioObjectID, _ selector: AudioObjectPropertySelector) -> String? {
    var value: Unmanaged<CFString>?
    guard property(object, selector, &value), let s = value?.takeRetainedValue() else { return nil }
    return s as String
}

private func inputChannelCount(_ device: AudioObjectID) -> Int {
    var address = AudioObjectPropertyAddress(mSelector: kAudioDevicePropertyStreamConfiguration,
                                             mScope: kAudioDevicePropertyScopeInput, mElement: kAudioObjectPropertyElementMain)
    var size: UInt32 = 0
    guard AudioObjectGetPropertyDataSize(device, &address, 0, nil, &size) == noErr, size > 0 else { return 0 }
    let raw = UnsafeMutableRawPointer.allocate(byteCount: Int(size), alignment: MemoryLayout<AudioBufferList>.alignment)
    defer { raw.deallocate() }
    guard AudioObjectGetPropertyData(device, &address, 0, nil, &size, raw) == noErr else { return 0 }
    let list = UnsafeMutableAudioBufferListPointer(raw.assumingMemoryBound(to: AudioBufferList.self))
    return list.reduce(0) { $0 + Int($1.mNumberChannels) }
}

private func transportName(_ t: UInt32) -> String? {
    switch t {
    case kAudioDeviceTransportTypeBuiltIn: "builtin"
    case kAudioDeviceTransportTypeUSB: "usb"
    case kAudioDeviceTransportTypeBluetooth, kAudioDeviceTransportTypeBluetoothLE: "bluetooth"
    case kAudioDeviceTransportTypeAggregate, kAudioDeviceTransportTypeAutoAggregate: "aggregate"
    case kAudioDeviceTransportTypeVirtual: "virtual"
    default: nil
    }
}

/// Core Audio ids of the devices with inputs, and the default input.
private func inputDeviceIDs() -> (all: [AudioObjectID], defaultID: AudioObjectID) {
    var address = AudioObjectPropertyAddress(mSelector: kAudioHardwarePropertyDevices,
                                             mScope: kAudioObjectPropertyScopeGlobal, mElement: kAudioObjectPropertyElementMain)
    var size: UInt32 = 0
    var ids: [AudioObjectID] = []
    if AudioObjectGetPropertyDataSize(AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size) == noErr {
        ids = [AudioObjectID](repeating: 0, count: Int(size) / MemoryLayout<AudioObjectID>.size)
        AudioObjectGetPropertyData(AudioObjectID(kAudioObjectSystemObject), &address, 0, nil, &size, &ids)
    }
    var def = AudioObjectID(0)
    _ = property(AudioObjectID(kAudioObjectSystemObject), kAudioHardwarePropertyDefaultInputDevice, &def)
    return (ids.filter { inputChannelCount($0) > 0 }, def)
}

func captureDevices() -> [CaptureDevice] {
    let (ids, def) = inputDeviceIDs()
    var out: [CaptureDevice] = []
    for id in ids {
        guard let uid = stringProperty(id, kAudioDevicePropertyDeviceUID) else { continue }
        let name = stringProperty(id, kAudioObjectPropertyName) ?? uid
        var rate = Float64(0)
        _ = property(id, kAudioDevicePropertyNominalSampleRate, &rate)
        var transport = UInt32(0)
        _ = property(id, kAudioDevicePropertyTransportType, &transport)
        out.append(CaptureDevice(id: uid, name: name, channels: inputChannelCount(id), sampleRate: Int(rate),
                                 default: id == def ? true : nil, transport: transportName(transport)))
    }
    return out.sorted { ($0.default ?? false) && !($1.default ?? false) }
}

private func deviceID(uid: String) -> AudioObjectID? {
    inputDeviceIDs().all.first { stringProperty($0, kAudioDevicePropertyDeviceUID) == uid }
}

private func isAlive(_ id: AudioObjectID) -> Bool {
    var alive = UInt32(0)
    return property(id, kAudioDevicePropertyDeviceIsAlive, &alive) && alive != 0
}

// MARK: Recording

/// Runs the listen worker's recordings (one at a time: concurrency 1).
final class Recorder: @unchecked Sendable {
    let out: Writer
    private let lock = NSLock()
    private var stops: [String: () -> Void] = [:]

    init(out: Writer) {
        self.out = out
    }

    /// Ends the recording of job; false when none runs. The job answers
    /// before this returns true (Server waits for it).
    func stop(_ job: String) -> Bool {
        lock.lock()
        let stop = stops.removeValue(forKey: job)
        lock.unlock()
        stop?()
        return stop != nil
    }

    func record(_ p: CaptureParams, job: String) async throws -> CaptureResult {
        let (url, bits) = try outputURL(p.output)
        let fileChannels = p.channels ?? 1
        guard fileChannels == 1 || fileChannels == 2 else { throw EngineError.badRequest("channels must be 1 or 2") }

        // Microphone access: the first time macOS asks the user.
        switch AVCaptureDevice.authorizationStatus(for: .audio) {
        case .authorized:
            break
        case .notDetermined:
            if !(await AVCaptureDevice.requestAccess(for: .audio)) { throw EngineError.permissionDenied("microphone") }
        default:
            throw EngineError.permissionDenied("microphone")
        }
        try Task.checkCancellation()

        // The device.
        let (ids, def) = inputDeviceIDs()
        let device: AudioObjectID
        if let uid = nonEmpty(p.device) {
            guard let id = deviceID(uid: uid) else { throw EngineError.deviceUnavailable(uid) }
            device = id
        } else {
            guard def != 0, ids.contains(def) else { throw EngineError.deviceUnavailable("default input") }
            device = def
        }
        let uid = stringProperty(device, kAudioDevicePropertyDeviceUID) ?? p.device ?? ""
        let deviceChannels = inputChannelCount(device)
        var picks = (p.inputChannels ?? []).map { $0 - 1 }
        if picks.isEmpty { picks = fileChannels == 2 && deviceChannels >= 2 ? [0, 1] : [0] }
        for c in picks where c < 0 || c >= deviceChannels {
            throw EngineError.badRequest("The input has \(deviceChannels) channel(s); there is no channel \(c + 1)")
        }

        let engine = AVAudioEngine()
        let input = engine.inputNode
        if let unit = input.audioUnit {
            var id = device
            let status = AudioUnitSetProperty(unit, kAudioOutputUnitProperty_CurrentDevice, kAudioUnitScope_Global, 0,
                                              &id, UInt32(MemoryLayout<AudioObjectID>.size))
            if status != noErr { throw EngineError.deviceUnavailable(uid) }
        }
        let format = input.outputFormat(forBus: 0)
        guard format.sampleRate > 0, format.channelCount > 0 else { throw EngineError.deviceUnavailable(uid) }
        let deviceRate = Int(format.sampleRate)
        let fileRate = (p.sampleRate ?? 0) > 0 ? p.sampleRate! : deviceRate
        let writer = try WAVWriter(url: url, sampleRate: fileRate, channels: fileChannels, bits: bits)
        let session = CaptureSession(out: out, job: job, writer: writer, picks: picks, fileChannels: fileChannels,
                                     deviceRate: deviceRate, fileRate: fileRate, maxSeconds: p.maxSeconds ?? 0)

        input.installTap(onBus: 0, bufferSize: 1024, format: format) { buffer, _ in
            session.receive(buffer)
        }
        engine.prepare()
        do {
            try engine.start()
        } catch {
            input.removeTap(onBus: 0)
            writer.cancel()
            throw EngineError.deviceUnavailable(uid)
        }
        out.notify(Method.jobProgress, ProgressParams(job: job, stage: "recording"))

        let reason: String = await withTaskCancellationHandler {
            await withCheckedContinuation { (k: CheckedContinuation<String, Never>) in
                session.setOnEnd { k.resume(returning: $0) }
                lock.withLock { stops[job] = { session.end(CaptureStopped) } }
                // The device going away (unplugged, sleep).
                let watcher = Thread {
                    while !session.isEnded {
                        if !isAlive(device) { session.end(CaptureDeviceLost); break }
                        Thread.sleep(forTimeInterval: 0.25)
                    }
                }
                watcher.start()
            }
        } onCancel: {
            session.end("cancelled")
        }
        lock.withLock { _ = stops.removeValue(forKey: job) }
        engine.stop()
        input.removeTap(onBus: 0)
        session.drain()
        if reason == "cancelled" || Task.isCancelled {
            writer.cancel()
            throw CancellationError()
        }
        if let error = session.writeError {
            writer.cancel()
            throw RPCError(Code.internal, "Could not write \(url.path): \(error.localizedDescription)")
        }
        try writer.finish()
        return CaptureResult(output: url.path, sampleRate: fileRate, channels: fileChannels, frames: writer.frames,
                             audioSeconds: Double(writer.frames) / Double(fileRate), device: uid,
                             peak: session.peakDB, clipped: session.clipped > 0 ? session.clipped : nil, reason: reason)
    }
}

let CaptureStopped = "stopped"
let CaptureDeviceLost = "deviceLost"
let CaptureMaxSeconds = "maxSeconds"

/// One recording: converts, writes and meters what the tap delivers, on a
/// serial queue off the audio thread.
private final class CaptureSession: @unchecked Sendable {
    let out: Writer
    let job: String
    let writer: WAVWriter
    let picks: [Int]
    let fileChannels: Int
    let deviceRate: Int
    let fileRate: Int
    let maxFrames: Int64
    private let queue = DispatchQueue(label: "audiosmith.capture")
    private let lock = NSLock()
    private var converter: AVAudioConverter?
    private let convertFormat: AVAudioFormat
    private let fileFormat: AVAudioFormat

    private var onEnd: ((String) -> Void)?
    private var ended = false
    private var endReason = ""
    var writeError: Error?
    var clipped = 0
    var peakDB: Double = -120
    // Meter state, per ~50 ms of audio.
    private var meterFrames = 0
    private var meterSum: Double = 0
    private var meterPeak: Float = 0
    private var meterClipped = false
    private var written: Int64 = 0

    init(out: Writer, job: String, writer: WAVWriter, picks: [Int], fileChannels: Int, deviceRate: Int, fileRate: Int, maxSeconds: Double) {
        self.out = out
        self.job = job
        self.writer = writer
        self.picks = picks
        self.fileChannels = fileChannels
        self.deviceRate = deviceRate
        self.fileRate = fileRate
        maxFrames = maxSeconds > 0 ? Int64(maxSeconds * Double(fileRate)) : 0
        convertFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(deviceRate),
                                      channels: AVAudioChannelCount(fileChannels), interleaved: false)!
        fileFormat = AVAudioFormat(commonFormat: .pcmFormatFloat32, sampleRate: Double(fileRate),
                                   channels: AVAudioChannelCount(fileChannels), interleaved: false)!
        if deviceRate != fileRate {
            converter = AVAudioConverter(from: convertFormat, to: fileFormat)
        }
    }

    /// The tap: picks the channels into a buffer of the file's channel
    /// count at the device rate, then hands it to the queue.
    func receive(_ buffer: AVAudioPCMBuffer) {
        guard let data = buffer.floatChannelData else { return }
        let n = Int(buffer.frameLength)
        let channels = Int(buffer.format.channelCount)
        guard n > 0, let picked = AVAudioPCMBuffer(pcmFormat: convertFormat, frameCapacity: AVAudioFrameCount(n)),
              let dst = picked.floatChannelData else { return }
        picked.frameLength = AVAudioFrameCount(n)
        let interleavedSource = buffer.format.isInterleaved
        func sample(_ ch: Int, _ i: Int) -> Float {
            let c = min(ch, channels - 1)
            return interleavedSource ? data[0][i * channels + c] : data[c][i]
        }
        if fileChannels == 1 {
            let scale = 1 / Float(picks.count)
            for i in 0 ..< n {
                var v: Float = 0
                for c in picks { v += sample(c, i) }
                dst[0][i] = v * scale
            }
        } else {
            let left = picks[0], right = picks.count > 1 ? picks[1] : picks[0]
            for i in 0 ..< n {
                dst[0][i] = sample(left, i)
                dst[1][i] = sample(right, i)
            }
        }
        queue.async { self.process(picked) }
    }

    private func process(_ buffer: AVAudioPCMBuffer) {
        if isEnded { return }
        var outBuffer = buffer
        if let converter {
            let capacity = AVAudioFrameCount(Double(buffer.frameLength) * Double(fileRate) / Double(deviceRate) + 32)
            guard let converted = AVAudioPCMBuffer(pcmFormat: fileFormat, frameCapacity: capacity) else { return }
            var given = false
            var error: NSError?
            converter.convert(to: converted, error: &error) { _, status in
                if given {
                    status.pointee = .noDataNow
                    return nil
                }
                given = true
                status.pointee = .haveData
                return buffer
            }
            outBuffer = converted
        }
        guard let ch = outBuffer.floatChannelData else { return }
        var n = Int(outBuffer.frameLength)
        if maxFrames > 0 { n = min(n, Int(maxFrames - written)) }
        if n <= 0 { return }
        var interleaved = [Float](repeating: 0, count: n * fileChannels)
        for i in 0 ..< n {
            for c in 0 ..< fileChannels {
                let v = ch[c][i]
                interleaved[i * fileChannels + c] = v
                let a = abs(v)
                meterSum += Double(v * v)
                if a > meterPeak { meterPeak = a }
                if a >= 0.999 {
                    clipped += 1
                    meterClipped = true
                }
            }
        }
        do {
            try writer.append(interleaved[...])
        } catch {
            writeError = error
            end(CaptureStopped)
            return
        }
        written += Int64(n)
        meterFrames += n
        if meterFrames >= fileRate / 20 {
            let rms = sqrt(meterSum / Double(meterFrames * fileChannels))
            let peak = Double(meterPeak)
            peakDB = max(peakDB, db(peak))
            out.notify(Method.captureLevel, LevelParams(job: job, seconds: round3(Double(written) / Double(fileRate)),
                                                        rms: round3(db(rms)), peak: round3(db(peak)),
                                                        clipped: meterClipped ? true : nil))
            meterFrames = 0
            meterSum = 0
            meterPeak = 0
            meterClipped = false
        }
        if maxFrames > 0, written >= maxFrames { end(CaptureMaxSeconds) }
    }

    var isEnded: Bool { lock.withLock { ended } }

    /// Sets what runs when the recording ends (at once if it already has).
    func setOnEnd(_ fn: @escaping (String) -> Void) {
        let reason: String? = lock.withLock {
            if ended { return endReason }
            onEnd = fn
            return nil
        }
        if let reason { fn(reason) }
    }

    func end(_ reason: String) {
        let call: ((String) -> Void)? = lock.withLock {
            if ended { return nil }
            ended = true
            endReason = reason
            let f = onEnd
            onEnd = nil
            return f
        }
        call?(reason)
    }

    /// Waits for the buffers already queued.
    func drain() {
        queue.sync {}
        peakDB = max(peakDB, db(Double(meterPeak)))
    }
}

private func db(_ x: Double) -> Double {
    x <= 1e-6 ? -120 : max(-120, 20 * log10(x))
}
