import Foundation

/// Little-endian 16-bit PCM of samples in [-1, 1] (tts.chunk's "pcm16le").
func pcm16(_ samples: ArraySlice<Float>) -> Data {
    var data = Data(capacity: samples.count * 2)
    for s in samples {
        let v = Int16(max(-1, min(1, s)) * 32767)
        withUnsafeBytes(of: v.littleEndian) { data.append(contentsOf: $0) }
    }
    return data
}

/// Writes a mono WAV file of 16- or 24-bit PCM, or 32-bit float. It writes
/// a temporary file beside url and renames it into place, so url never
/// holds a partial file.
func writeWAV(_ samples: [Float], sampleRate: Int, bits: Int, to url: URL) throws {
    let bytesPerSample = bits / 8
    let dataSize = samples.count * bytesPerSample
    var d = Data(capacity: 44 + dataSize)
    func u32(_ v: Int) { withUnsafeBytes(of: UInt32(v).littleEndian) { d.append(contentsOf: $0) } }
    func u16(_ v: Int) { withUnsafeBytes(of: UInt16(v).littleEndian) { d.append(contentsOf: $0) } }
    d.append(contentsOf: Array("RIFF".utf8))
    u32(36 + dataSize)
    d.append(contentsOf: Array("WAVEfmt ".utf8))
    u32(16)
    u16(bits == 32 ? 3 : 1) // IEEE float, or PCM
    u16(1)
    u32(sampleRate)
    u32(sampleRate * bytesPerSample)
    u16(bytesPerSample)
    u16(bits)
    d.append(contentsOf: Array("data".utf8))
    u32(dataSize)
    switch bits {
    case 32:
        for s in samples { withUnsafeBytes(of: s.bitPattern.littleEndian) { d.append(contentsOf: $0) } }
    case 24:
        for s in samples {
            let v = Int32(max(-1, min(1, s)) * 8_388_607)
            d.append(UInt8(truncatingIfNeeded: v))
            d.append(UInt8(truncatingIfNeeded: v >> 8))
            d.append(UInt8(truncatingIfNeeded: v >> 16))
        }
    default:
        d.append(pcm16(samples[...]))
    }

    let dir = url.deletingLastPathComponent()
    try FileManager.default.createDirectory(at: dir, withIntermediateDirectories: true)
    let tmp = dir.appendingPathComponent(".\(url.lastPathComponent).\(getpid()).part")
    try d.write(to: tmp)
    guard rename(tmp.path, url.path) == 0 else {
        let err = String(cString: strerror(errno))
        try? FileManager.default.removeItem(at: tmp)
        throw RPCErrorWrap.rename(err)
    }
}

enum RPCErrorWrap: Error, LocalizedError {
    case rename(String)
    var errorDescription: String? {
        switch self {
        case .rename(let e): "rename: \(e)"
        }
    }
}
