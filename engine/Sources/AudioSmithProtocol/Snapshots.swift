import Foundation

/// The finished snapshots of a model in the Hugging Face cache, for a job
/// that names the model without its directory (`models.load` without
/// `path`, a job without `modelPath`). The cache is shared with other
/// apps (the ActNow family spec §7.1), which may have another commit of
/// the same repo there: the engine must not pick one. Only a single
/// snapshot is unambiguous; with several, the app has to name the one it
/// pinned (`model_path_required`).
public enum SnapshotChoice: Equatable, Sendable {
    case none
    case one(URL)
    /// The snapshots' names (commits), sorted.
    case several([String])
}

/// snapshotChoice looks at `<repo>/snapshots`: its non-empty directories,
/// leaving out snapshots being built (`<commit>.incomplete`) and hidden
/// files.
public func snapshotChoice(_ snapshots: URL) -> SnapshotChoice {
    let fm = FileManager.default
    let names = ((try? fm.contentsOfDirectory(atPath: snapshots.path)) ?? []).filter { name in
        guard !name.hasPrefix("."), !name.hasSuffix(".incomplete") else { return false }
        var isDir: ObjCBool = false
        let path = snapshots.appendingPathComponent(name).path
        guard fm.fileExists(atPath: path, isDirectory: &isDir), isDir.boolValue else { return false }
        return !((try? fm.contentsOfDirectory(atPath: path)) ?? []).isEmpty
    }.sorted()
    switch names.count {
    case 0: return .none
    case 1: return .one(snapshots.appendingPathComponent(names[0]))
    default: return .several(names)
    }
}
