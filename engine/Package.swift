// swift-tools-version:6.2
import Foundation
import PackageDescription

/// The engine's own Info.plist, linked into the binary (Support/Info.plist).
let infoPlist = URL(fileURLWithPath: #filePath).deletingLastPathComponent().appendingPathComponent("Support/Info.plist").path

// audiosmith-engine is the TTS backend of AudioSmith: a long-running helper
// process that loads MLX Audio models and turns text into WAV files. The Go
// app talks to it with JSON lines over stdin/stdout: protocol v2
// (docs/protocol-v2.md; the wire types are the AudioSmithProtocol target,
// which has no dependencies, so its tests run without MLX).
//
// Build it with xcodebuild (scripts/build-engine.sh), not `swift build`:
// only Xcode compiles MLX's Metal kernels into default.metallib.
//
// Dependencies: upstream mlx-audio-swift with AudioSmith's patch series
// (scripts/fetch-engine-deps.sh prepares it in .build/deps; see
// engine/patches/README.md), mlx-swift 0.32.3 and mlx-swift-lm 3.32.3
// (MLXLLM + MLXGuidedGeneration for llm.generate).
// mlx-swift 0.32 makes default streams thread-local: a model that yields an
// unevaluated MLXArray from its own Task crashes the consumer with "There
// is no Stream(gpu, N) in current thread". The patches evaluate before
// yielding where scripts/smoke-models.sh found it; every catalogue model
// passes that smoke suite before it is listed.
let package = Package(
    name: "AudioSmithEngine",
    platforms: [.macOS(.v15)],
    products: [
        .executable(name: "audiosmith-engine", targets: ["AudioSmithEngine"]),
    ],
    dependencies: [
        .package(path: ".build/deps/mlx-audio-swift"),
        .package(url: "https://github.com/ml-explore/mlx-swift.git", exact: "0.32.3"),
        .package(url: "https://github.com/ml-explore/mlx-swift-lm.git", exact: "3.32.3"),
        // Tokenizers for the local LLM (already in the graph through
        // mlx-audio-swift; Apache-2.0).
        .package(url: "https://github.com/huggingface/swift-transformers.git", exact: "1.3.5"),
        // Separation (M4), both MIT: HTDemucs and Mel-RoFormer ports to MLX
        // (built and measured in the M0 spikes, research/m0-spikes.md §5).
        .package(url: "https://github.com/kylehowells/demucs-mlx-swift.git", revision: "c8482739c621b90a64bf1a9f013712f6fea44eda"),
        .package(url: "https://github.com/xocialize/mel-roformer-mlx-swift.git", revision: "22162f8fd5cd8cc09abc47a87c1ecddf3baf5ee3"),
    ],
    targets: [
        .target(
            name: "AudioSmithProtocol",
            path: "Sources/AudioSmithProtocol",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
        .executableTarget(
            name: "AudioSmithEngine",
            dependencies: [
                "AudioSmithProtocol",
                .product(name: "MLXAudioCore", package: "mlx-audio-swift"),
                .product(name: "MLXAudioTTS", package: "mlx-audio-swift"),
                .product(name: "MLXAudioSTT", package: "mlx-audio-swift"),
                .product(name: "MLXAudioVAD", package: "mlx-audio-swift"),
                .product(name: "MLXAudioSTS", package: "mlx-audio-swift"),
                .product(name: "MLX", package: "mlx-swift"),
                .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
                // The local LLM (llm.generate): MLXLLM's model families and
                // xgrammar JSON constraints (vendored C++, Apache-2.0).
                .product(name: "MLXLLM", package: "mlx-swift-lm"),
                .product(name: "MLXGuidedGeneration", package: "mlx-swift-lm"),
                .product(name: "Tokenizers", package: "swift-transformers"),
                .product(name: "DemucsMLX", package: "demucs-mlx-swift"),
                .product(name: "SwiftRoFormer", package: "mel-roformer-mlx-swift"),
            ],
            path: "Sources/AudioSmithEngine",
            swiftSettings: [.swiftLanguageMode(.v5)],
            linkerSettings: [.unsafeFlags(["-Xlinker", "-sectcreate", "-Xlinker", "__TEXT", "-Xlinker", "__info_plist", "-Xlinker", infoPlist])]
        ),
        // Decodes engine/Fixtures with the Swift types (scripts/test-engine.sh).
        .testTarget(
            name: "AudioSmithEngineTests",
            dependencies: ["AudioSmithProtocol"],
            path: "Tests/AudioSmithEngineTests",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
