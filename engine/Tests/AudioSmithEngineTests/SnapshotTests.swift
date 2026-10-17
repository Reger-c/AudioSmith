import Foundation
import XCTest
@testable import AudioSmithProtocol

/// The engine loads a model by its snapshot directory; without one from
/// the app it takes the only snapshot in the cache, never one of several
/// (another app may have put another commit there; family spec §7.1).
final class SnapshotTests: XCTestCase {
    func testChoice() throws {
        let fm = FileManager.default
        let root = fm.temporaryDirectory.appendingPathComponent("snapshots-\(UUID().uuidString)")
        defer { try? fm.removeItem(at: root) }
        let snaps = root.appendingPathComponent("models--acme--tiny").appendingPathComponent("snapshots")
        XCTAssertEqual(snapshotChoice(snaps), .none, "no repo")

        func snapshot(_ name: String, file: Bool) throws {
            let dir = snaps.appendingPathComponent(name)
            try fm.createDirectory(at: dir, withIntermediateDirectories: true)
            if file {
                try Data("{}".utf8).write(to: dir.appendingPathComponent("config.json"))
            }
        }
        let a = String(repeating: "a", count: 40), b = String(repeating: "b", count: 40)
        try snapshot(b, file: false) // empty: not a snapshot
        try snapshot(a + ".incomplete", file: true) // being built
        XCTAssertEqual(snapshotChoice(snaps), .none)
        try snapshot(a, file: true)
        XCTAssertEqual(snapshotChoice(snaps), .one(snaps.appendingPathComponent(a)))
        try Data("{}".utf8).write(to: snaps.appendingPathComponent(b).appendingPathComponent("config.json"))
        XCTAssertEqual(snapshotChoice(snaps), .several([a, b]))
    }

    func testCode() {
        XCTAssertTrue(Code.all.contains(Code.modelPathRequired))
        XCTAssertEqual(Code.modelPathRequired, "model_path_required")
    }
}
