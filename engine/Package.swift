// swift-tools-version:6.2
import PackageDescription

// audiosmith-engine is the TTS backend of AudioSmith: a long-running helper
// process that loads MLX Audio models and turns text into WAV files. The Go
// app talks to it with JSON lines over stdin/stdout (see Sources/AudioSmithEngine/Protocol.swift).
//
// Build it with xcodebuild (scripts/build-engine.sh), not `swift build`:
// only Xcode compiles MLX's Metal kernels into default.metallib.
let package = Package(
    name: "AudioSmithEngine",
    platforms: [.macOS(.v14)],
    products: [
        .executable(name: "audiosmith-engine", targets: ["AudioSmithEngine"]),
    ],
    dependencies: [
        // Pinned: mlx-audio-swift moves fast and has no stable tags we can rely on.
        .package(
            url: "https://github.com/Blaizzy/mlx-audio-swift.git",
            revision: "244458d7b61e54ff135073ddc9ed76782bb20758"
        ),
        // The MLX that mlx-audio-swift resolves to at that revision (0.32.3).
        .package(url: "https://github.com/ml-explore/mlx-swift.git", .upToNextMinor(from: "0.32.3")),
        .package(url: "https://github.com/ml-explore/mlx-swift-lm.git", .upToNextMinor(from: "3.32.3")),
    ],
    targets: [
        .executableTarget(
            name: "AudioSmithEngine",
            dependencies: [
                .product(name: "MLXAudioCore", package: "mlx-audio-swift"),
                .product(name: "MLXAudioTTS", package: "mlx-audio-swift"),
                .product(name: "MLX", package: "mlx-swift"),
                .product(name: "MLXLMCommon", package: "mlx-swift-lm"),
            ],
            path: "Sources/AudioSmithEngine",
            swiftSettings: [.swiftLanguageMode(.v5)]
        ),
    ]
)
