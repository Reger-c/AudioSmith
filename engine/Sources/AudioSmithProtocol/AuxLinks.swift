import Foundation

/// The auxiliary repos of a model job (`auxPaths`: repo id → the snapshot
/// directory the app pinned; Kokoro's G2P repos, Chatterbox's S3
/// tokenizer, Whisper's tokenizer). mlx-audio-swift finds them by repo id
/// in its own folders of the cache, `<hub>/mlx-audio/<owner>_<name>` (and
/// `…_tokenizer_only` for Whisper's tokenizer), which it fills from the
/// snapshot the repo's ref names: with another commit of the repo in the
/// shared cache (another ActNow app's, family spec §7.1) that may be the
/// wrong one. The engine links those folders to the app's snapshots
/// before the job instead.
public struct AuxParams: Decodable, Sendable {
    public var auxPaths: [String: String]?
}

public enum AuxLinkError: Error, Equatable, Sendable {
    /// The app named a snapshot directory that is not there.
    case missing(repo: String, path: String)
    case badRepo(String)
}

/// The folders of the cache where mlx-audio-swift looks for repo.
public func auxFolders(_ repo: String, hub: URL) -> [URL] {
    let base = hub.appendingPathComponent("mlx-audio")
    let name = repo.replacingOccurrences(of: "/", with: "_")
    if repo.hasPrefix("openai/whisper-") {
        return [base.appendingPathComponent(name + "_tokenizer_only")]
    }
    return [base.appendingPathComponent(name)]
}

/// linkSnapshot makes dir hold a symbolic link to each file of snapshot
/// (hidden files left out), replacing what dir had under those names: a
/// copy of another commit, or links elsewhere. Other files of dir stay
/// (mlx-audio-swift's own config.json for a repo without one).
public func linkSnapshot(_ snapshot: URL, into dir: URL) throws {
    let fm = FileManager.default
    try fm.createDirectory(at: dir, withIntermediateDirectories: true)
    for name in try fm.contentsOfDirectory(atPath: snapshot.path) where !name.hasPrefix(".") {
        let link = dir.appendingPathComponent(name).path
        let target = snapshot.appendingPathComponent(name).path
        if (try? fm.destinationOfSymbolicLink(atPath: link)) == target { continue }
        try? fm.removeItem(atPath: link)
        do {
            try fm.createSymbolicLink(atPath: link, withDestinationPath: target)
        } catch {
            // Another worker linked it at the same moment.
            guard (try? fm.destinationOfSymbolicLink(atPath: link)) == target else { throw error }
        }
    }
}

/// linkAuxRepos links every auxiliary repo's folders (auxFolders) to its
/// snapshot directory; a directory that is not there is an error.
public func linkAuxRepos(_ aux: [String: String], hub: URL) throws {
    for repo in aux.keys.sorted() {
        guard let path = aux[repo], !path.isEmpty else { continue }
        let parts = repo.split(separator: "/")
        guard parts.count == 2, !repo.contains(".."), !parts.contains(where: { $0.isEmpty }) else { throw AuxLinkError.badRepo(repo) }
        var isDir: ObjCBool = false
        guard FileManager.default.fileExists(atPath: path, isDirectory: &isDir), isDir.boolValue else {
            throw AuxLinkError.missing(repo: repo, path: path)
        }
        for dir in auxFolders(repo, hub: hub) {
            try linkSnapshot(URL(fileURLWithPath: path), into: dir)
        }
    }
}
