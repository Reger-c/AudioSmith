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
// Dependencies: upstream mlx-audio-swift with AudioSmith's patch series
// (scripts/fetch-engine-deps.sh prepares it in .build/deps; see
// engine/patches/README.md), mlx-swift 0.32.3 and mlx-swift-lm 3.32.3.
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
