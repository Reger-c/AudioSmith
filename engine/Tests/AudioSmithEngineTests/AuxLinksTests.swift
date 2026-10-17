import Foundation
import XCTest
@testable import AudioSmithProtocol

/// A model's auxiliary repos load from the snapshots the app pinned, also
/// when the cache holds another commit of the repo and mlx-audio-swift
/// copied that one before (family spec §7.1); a snapshot that is not there
/// is a clear error.
final class AuxLinksTests: XCTestCase {
    func testLinksPinnedSnapshot() throws {
        let fm = FileManager.default
        let hub = fm.temporaryDirectory.appendingPathComponent("aux-\(UUID().uuidString)")
        defer { try? fm.removeItem(at: hub) }
        let repo = "beshkenadze/kitten-tts-g2p"
        let snaps = hub.appendingPathComponent("models--beshkenadze--kitten-tts-g2p/snapshots")
        let pinned = snaps.appendingPathComponent(String(repeating: "a", count: 40))
        let other = snaps.appendingPathComponent(String(repeating: "b", count: 40))
        for (dir, content) in [(pinned, "pinned"), (other, "other")] {
            try fm.createDirectory(at: dir, withIntermediateDirectories: true)
            try Data(content.utf8).write(to: dir.appendingPathComponent("g2p.safetensors"))
        }
        // mlx-audio-swift's copy of the other commit, and its own config.json.
        let folder = auxFolders(repo, hub: hub)[0]
        XCTAssertEqual(folder.lastPathComponent, "beshkenadze_kitten-tts-g2p")
        try fm.createDirectory(at: folder, withIntermediateDirectories: true)
        try Data("other".utf8).write(to: folder.appendingPathComponent("g2p.safetensors"))
        try Data("{}".utf8).write(to: folder.appendingPathComponent("config.json"))

        try linkAuxRepos([repo: pinned.path], hub: hub)
        let read = try String(contentsOf: folder.appendingPathComponent("g2p.safetensors"), encoding: .utf8)
        XCTAssertEqual(read, "pinned")
        XCTAssertEqual(try fm.destinationOfSymbolicLink(atPath: folder.appendingPathComponent("g2p.safetensors").path), pinned.appendingPathComponent("g2p.safetensors").path)
        XCTAssertTrue(fm.fileExists(atPath: folder.appendingPathComponent("config.json").path))
        // Again (every job): nothing changes.
        try linkAuxRepos([repo: pinned.path], hub: hub)
        XCTAssertEqual(try String(contentsOf: folder.appendingPathComponent("g2p.safetensors"), encoding: .utf8), "pinned")

        XCTAssertThrowsError(try linkAuxRepos([repo: snaps.appendingPathComponent("c").path], hub: hub)) { error in
            XCTAssertEqual(error as? AuxLinkError, .missing(repo: repo, path: snaps.appendingPathComponent("c").path))
        }
        XCTAssertThrowsError(try linkAuxRepos(["../x": pinned.path], hub: hub))
        XCTAssertEqual(auxFolders("openai/whisper-large-v3", hub: hub)[0].lastPathComponent, "openai_whisper-large-v3_tokenizer_only")
    }
}
