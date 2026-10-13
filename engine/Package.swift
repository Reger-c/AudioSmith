// swift-tools-version:6.2
import PackageDescription

// audiosmith-engine is the TTS backend of AudioSmith: a long-running helper
// process that loads MLX Audio models and turns text into WAV files. The Go
// app talks to it with JSON lines over stdin/stdout: protocol v2
// (docs/protocol-v2.md; the wire types are the AudioSmithProtocol target,
// which has no dependencies, so its tests run without MLX).
//
// Build it with xcodebuild (scripts/build-engine.sh), not `swift build`:
// only Xcode compiles MLX's Metal kernels into default.metallib.
//
// Dependencies: the compatibility set Voxt validated, kept together:
//   hehehai/mlx-audio-swift 2a6e75d  (upstream mlx-audio-swift + Voxt fixes)
//   mlx-swift 0.31.6, mlx-swift-lm c6446cf, swift-transformers 1.3.4, swift-huggingface 0.10.2
// Do not move to mlx-swift 0.32.x yet: it makes Metal command encoders
// thread-local, and mlx-audio-swift builds graphs in Tasks that hop threads,
// which crashes generation with "There is no Stream(gpu, N) in current thread".
let package = Package(
    name: "AudioSmithEngine",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "audiosmith-engine", targets: ["AudioSmithEngine"]),
    ],
    dependencies: [
        .package(
            url: "https://github.com/hehehai/mlx-audio-swift.git",
            revision: "2a6e75d28ae6a399ba7c7aec842384ef7a3142b5"
        ),
        .package(url: "https://github.com/ml-explore/mlx-swift.git", exact: "0.31.6"),
        .package(
            url: "https://github.com/ml-explore/mlx-swift-lm.git",
            revision: "c6446cf7bfb7cea76408013b614d4b2c530eaa03"
        ),
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
                .product(name: "MLX", package: "mlx-swift"),
                .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
            ],
            path: "Sources/AudioSmithEngine",
            swiftSettings: [.swiftLanguageMode(.v5)]
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
