import Foundation
import XCTest
@testable import AudioSmithText

/// MOSS-Transcribe-Diarize's output read into speaker turns (thread #48).
final class MossTextTests: XCTestCase {
    func testTurns() {
        let out = "[0.48][S01]Welcome everyone[1.66][1.80][S02]Thanks for having me.[2.50]"
        XCTAssertEqual(parseMossTurns(out), [
            MossTurn(start: 0.48, end: 1.66, speaker: "S01", text: "Welcome everyone"),
            MossTurn(start: 1.8, end: 2.5, speaker: "S02", text: "Thanks for having me."),
        ])
    }

    func testChineseAndNewlines() {
        let out = "[0.00][S01]今天我们聊聊 AudioSmith。[3.90]\n[4.20][S02]好啊，从哪儿说起？[7.66]"
        let t = parseMossTurns(out)
        XCTAssertEqual(t.map(\.text), ["今天我们聊聊 AudioSmith。", "好啊，从哪儿说起？"])
        XCTAssertEqual(t.map(\.speaker), ["S01", "S02"])
        XCTAssertEqual(joinMossTexts(t.map(\.text)), "今天我们聊聊 AudioSmith。好啊，从哪儿说起？")
    }

    func testMissingTimesAndLabels() {
        // No end time, then a label without a start, then plain text.
        let out = "[1.0][S01]one[S02]two[5.0] three"
        let t = timeMossTurns(parseMossTurns(out), duration: 9)
        XCTAssertEqual(t.count, 3)
        XCTAssertEqual(t[0], MossTurn(start: 1, end: 1, speaker: "S01", text: "one"))
        XCTAssertEqual(t[1].speaker, "S02")
        XCTAssertEqual(t[1].end, 5)
        XCTAssertEqual(t[2], MossTurn(start: 5, end: 9, speaker: nil, text: "three"))
        // Plain text without any tag: one turn over the whole audio.
        XCTAssertEqual(timeMossTurns(parseMossTurns("Hello there."), duration: 3),
                       [MossTurn(start: 0, end: 3, speaker: nil, text: "Hello there.")])
    }

    func testEventsAndStrayTags() {
        let t = parseMossTurns("[0.5][S01]我们[laughter]走吧[2.0][2.1][S02]Hello [music] world [s01][3.0]")
        XCTAssertEqual(t.map(\.text), ["我们走吧", "Hello world"])
        XCTAssertEqual(cleanMossText("  a  [applause]  b "), "a b")
    }

    func testTimesClamped() {
        let t = timeMossTurns([MossTurn(start: 8, end: 12, speaker: "S01", text: "x")], duration: 10)
        XCTAssertEqual(t[0].start, 8)
        XCTAssertEqual(t[0].end, 10)
    }

    func testInstruction() {
        XCTAssertEqual(mossInstruction(prompt: nil), mossInstruction)
        XCTAssertEqual(mossInstruction(prompt: " \n "), mossInstruction)
        XCTAssertEqual(mossInstruction(prompt: "AudioSmith、MLX\nQwen3, AudioSmith"),
                       mossInstruction + "热词提示：AudioSmith, MLX, Qwen3")
    }

    func testScriptLanguage() {
        XCTAssertEqual(cjkLanguage("今天我们用 AudioSmith 和 MLX 做 iOS 版本的配音"), "zh")
        XCTAssertEqual(cjkLanguage("今日はいい天気ですね"), "ja")
        XCTAssertEqual(cjkLanguage("안녕하세요 반갑습니다"), "ko")
        XCTAssertNil(cjkLanguage("Hello, nice to meet you. 你"))
    }
}
