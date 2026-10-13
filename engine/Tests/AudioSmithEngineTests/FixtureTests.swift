import Foundation
import XCTest
@testable import AudioSmithProtocol

/// Decodes every message of engine/Fixtures with the Swift types and
/// re-encodes it: the result must equal the fixture, so the Swift types
/// carry every field the Go side (and the spec) has. The Go side runs the
/// same fixtures (go test ./internal/engine/proto).
final class FixtureTests: XCTestCase {
    static let fixtures = URL(fileURLWithPath: #filePath)
        .deletingLastPathComponent().deletingLastPathComponent().deletingLastPathComponent()
        .appendingPathComponent("Fixtures")

    func fixtureFiles() throws -> [URL] {
        let files = try FileManager.default.contentsOfDirectory(at: Self.fixtures, includingPropertiesForKeys: nil)
            .filter { $0.pathExtension == "jsonl" }
            .sorted { $0.lastPathComponent < $1.lastPathComponent }
        XCTAssertGreaterThanOrEqual(files.count, 8, "fixtures in \(Self.fixtures.path)")
        return files
    }

    func testFixturesRoundTrip() throws {
        var checked = 0
        for file in try fixtureFiles() {
            let text = try String(contentsOf: file, encoding: .utf8)
            var methodByID: [String: String] = [:]
            for (n, line) in text.split(separator: "\n").enumerated() where !line.isEmpty {
                let where_ = "\(file.lastPathComponent):\(n + 1)"
                let wrapper = try XCTUnwrap(
                    try JSONSerialization.jsonObject(with: Data(line.utf8)) as? [String: Any], where_)
                guard let msgObj = wrapper["msg"] else {
                    // legacy_v1.jsonl: a raw protocol-1 line.
                    XCTAssertNotNil(wrapper["line"], where_)
                    continue
                }
                let to = try XCTUnwrap(wrapper["to"] as? String, where_)
                let raw = try JSONSerialization.data(withJSONObject: msgObj)
                let msg = try Incoming.decode(raw)
                let reencoded: Data
                switch msg.kind {
                case .request:
                    XCTAssertEqual(to, "engine", where_)
                    methodByID[msg.id!] = msg.method!
                    reencoded = try roundTrip(msg, params: paramsType(msg.method!), where: where_)
                case .notification:
                    XCTAssertEqual(to, "app", where_)
                    let type = paramsType(msg.method!)
                    XCTAssertNotNil(type, "\(where_): unknown notification \(msg.method!)")
                    reencoded = try roundTrip(msg, params: type, where: where_)
                case .response:
                    XCTAssertEqual(to, "app", where_)
                    let method = try XCTUnwrap(methodByID[msg.id!], "\(where_): response to an unknown id")
                    if let err = msg.error {
                        XCTAssertTrue(Code.all.contains(err.code), "\(where_): unknown code \(err.code)")
                        reencoded = try makeEncoder().encode(Outgoing<Empty>.failure(msg.id!, err))
                    } else {
                        let type = try XCTUnwrap(resultType(method), "\(where_): no result type for \(method)")
                        reencoded = try encodeResponse(msg, type)
                    }
                case .invalid:
                    XCTFail("\(where_): not a request, notification or response")
                    continue
                }
                let want = try JSONSerialization.jsonObject(with: raw) as! NSDictionary
                let got = try JSONSerialization.jsonObject(with: reencoded) as! NSDictionary
                XCTAssertEqual(got, want, "\(where_): re-encoded differs\n got \(String(decoding: reencoded, as: UTF8.self))\nwant \(String(decoding: raw, as: UTF8.self))")
                checked += 1
            }
        }
        XCTAssertGreaterThan(checked, 40)
    }

    /// Decodes params as type (or keeps them raw for an unknown method)
    /// and encodes the message again.
    func roundTrip(_ msg: Incoming, params type: (Decodable & Encodable).Type?, where: String) throws -> Data {
        guard let type else {
            let raw = try msg.params.map { try JSONDecoder().decode(JSONValue.self, from: $0) }
            return try makeEncoder().encode(Outgoing<JSONValue>(id: msg.id, method: msg.method, params: raw))
        }
        return try encodeParams(msg, type)
    }

    func encodeParams<T: Decodable & Encodable>(_ msg: Incoming, _ type: T.Type) throws -> Data {
        let params = try msg.params.map { try JSONDecoder().decode(T.self, from: $0) }
        return try makeEncoder().encode(Outgoing<T>(id: msg.id, method: msg.method, params: params))
    }

    func encodeResponse<T: Decodable & Encodable>(_ msg: Incoming, _ type: T.Type) throws -> Data {
        let result = try JSONDecoder().decode(T.self, from: msg.result!)
        return try makeEncoder().encode(Outgoing<T>.response(msg.id!, result))
    }

    func testChunkDataIsBase64() throws {
        let c = ChunkParams(job: "j", seq: 0, sampleRate: 24000, channels: 1, encoding: "pcm16le", data: Data([0, 1, 255]), final: true)
        let json = String(decoding: try makeEncoder().encode(c), as: UTF8.self)
        XCTAssertTrue(json.contains(#""data":"AAH/""#), json)
    }

    func testIncomingKinds() throws {
        XCTAssertEqual(try Incoming.decode(Data(#"{"id":"c1","method":"engine.stats"}"#.utf8)).kind, .request)
        XCTAssertEqual(try Incoming.decode(Data(#"{"method":"log","params":{}}"#.utf8)).kind, .notification)
        XCTAssertEqual(try Incoming.decode(Data(#"{"id":"c1","result":{}}"#.utf8)).kind, .response)
        XCTAssertEqual(try Incoming.decode(Data(#"{"id":"c1"}"#.utf8)).kind, .invalid)
        XCTAssertThrowsError(try Incoming.decode(Data("[1]".utf8)))
        // Missing optional members take their defaults; unknown ones are ignored.
        let p = try Incoming.decode(Data(#"{"id":"j","method":"tts.synthesize","params":{"model":"m","text":"t","output":{"path":"/x.wav"},"future":1}}"#.utf8))
            .decodeParams(SynthesizeParams.self)
        XCTAssertNil(p.speed)
        XCTAssertNil(p.stream)
        XCTAssertEqual(p.output.path, "/x.wav")
    }
}
