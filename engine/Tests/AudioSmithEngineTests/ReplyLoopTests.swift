import AudioSmithText
import XCTest

/// Constrained replies caught in a loop (AudioSmithText/ReplyLoop.swift).
final class ReplyLoopTests: XCTestCase {
    func testLoop() {
        let block = Array(100..<112) // 12 tokens
        let head = [1, 2, 3, 4, 5]
        XCTAssertTrue(ReplyLoop.ends(head + block + block + block))
        XCTAssertFalse(ReplyLoop.ends(head + block + block))
        // Still looping halfway through a fourth copy: the suffix is a
        // rotation of the block, repeated.
        XCTAssertTrue(ReplyLoop.ends(head + block + block + block + Array(block[0..<5])))
        // Short repeats (a list of the same word) are not a loop.
        XCTAssertFalse(ReplyLoop.ends(head + [7, 8, 7, 8, 7, 8, 7, 8, 7, 8]))
        XCTAssertTrue(ReplyLoop.ends(Array(repeating: 9, count: 24)))
        XCTAssertFalse(ReplyLoop.ends(Array(repeating: 9, count: 23)))
    }

    /// Array items that differ (ids, numbers) are no loop.
    func testItems() {
        var tokens: [Int] = []
        for n in 1...10 { tokens += [50, 51, 52, 53, 1000 + n, 54, 55, 56, 57, 58] }
        XCTAssertFalse(ReplyLoop.ends(tokens))
    }
}
