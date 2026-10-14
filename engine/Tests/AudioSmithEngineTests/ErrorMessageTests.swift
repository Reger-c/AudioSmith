import Foundation
import XCTest
@testable import AudioSmithProtocol

/// errorMessage keeps an error's own words: the engine used
/// localizedDescription, which for a Swift error that is not a
/// LocalizedError is Cocoa's generic sentence ("The operation couldn’t be
/// completed. (MLXAudioTTS.MossTTSNanoError error 0.)").
final class ErrorMessageTests: XCTestCase {
    enum Described: Error, CustomStringConvertible {
        case invalidInput(String)
        var description: String {
            switch self { case .invalidInput(let m): m }
        }
    }

    enum Localized: Error, LocalizedError {
        case bad
        var errorDescription: String? { "Localized words" }
    }

    enum Bare: Error {
        case missingReference
    }

    func testOwnWords() {
        let moss = Described.invalidInput("MOSS-TTS-Nano requires refAudio for voice_clone generation.")
        XCTAssertTrue(moss.localizedDescription.contains("error 0"), "the bug: \(moss.localizedDescription)")
        XCTAssertEqual(errorMessage(moss), "MOSS-TTS-Nano requires refAudio for voice_clone generation.")
        XCTAssertEqual(errorMessage(Localized.bad), "Localized words")
        XCTAssertEqual(errorMessage(Bare.missingReference), "missingReference")
        let ns = NSError(domain: "PocketTTSModel", code: 2, userInfo: [NSLocalizedDescriptionKey: "Missing audio prompt for voice: x"])
        XCTAssertEqual(errorMessage(ns), "Missing audio prompt for voice: x")
        let posix = NSError(domain: NSPOSIXErrorDomain, code: 2)
        XCTAssertEqual(errorMessage(posix), posix.localizedDescription)
    }
}
