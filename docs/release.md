# Releasing AudioSmith

中文版：[release.zh-CN.md](release.zh-CN.md)

A release is a tag `v<version>`. GitHub Actions ([`.github/workflows/release.yml`](../.github/workflows/release.yml)) then, on an Apple-silicon macOS runner:

1. builds the Swift engine (`scripts/build-engine.sh`, Xcode 26 and its Metal toolchain);
2. builds the app with `go tool mygo build`: signed with your **Developer ID** with the hardened runtime, the app's entitlements ([`entitlements.plist`](../entitlements.plist): microphone) and the engine's ([`engine/Support/engine.entitlements`](../engine/Support/engine.entitlements)), in a disk image that Apple **notarizes**, with the ticket stapled;
3. writes the **update files**, signed with your update key: `audiosmith-<version>-darwin-arm64.tar.gz`, deltas from up to three earlier releases, and `update-darwin-arm64.json` (the manifest, with this version's section of `CHANGELOG.md` as the release notes);
4. uploads them, with the disk image, to a **draft release**, which you review and publish. Installed copies check the latest published release once a day (and from AudioSmith → Check for Updates…) and update to it.

This follows MyGo's guides: [auto-updates](https://github.com/egoist/mygo/blob/main/docs/updates.md), [the updater plugin](https://github.com/egoist/mygo/blob/main/docs/plugins/updater.md), [distribution](https://github.com/egoist/mygo/blob/main/docs/distribution.md) and [GitHub Actions](https://github.com/egoist/mygo/blob/main/docs/github-actions.md).

Nothing about releases is in the repository: `mygo.json` has no `updates` section and no signing identity. The workflow adds them for its build (`scripts/release-config.sh`). So every other build — `go run`, `mygo dev`, `scripts/mac.sh go tool mygo build`, CI — is signed ad hoc and never updates itself (`mygo.Updater.Enabled()` is false; Settings → Diagnostics and about says so).

## What you set up once

You need: the Apple Developer Program (you have it), the GitHub CLI (`gh auth login`) and this repository on your Mac.

### 1. A Developer ID Application certificate

Only the account holder can create one.

- Xcode → Settings → Accounts → your team → Manage Certificates… → **+** → **Developer ID Application**; or
- [developer.apple.com → Certificates](https://developer.apple.com/account/resources/certificates/list) → **+** → Developer ID → **Developer ID Application** (G2 Sub-CA), with a certificate signing request from Keychain Access (Certificate Assistant → Request a Certificate from a Certificate Authority → Saved to disk), then download it and double-click it.

Check: `security find-identity -v -p codesigning` lists `Developer ID Application: <Name> (<TEAMID>)`. The 10 characters in brackets are your **team ID** (also on developer.apple.com → Membership details).

### 2. Export it as .p12 and add the signing secrets

Keychain Access → login → My Certificates → the Developer ID Application certificate (with its private key under it) → right-click → **Export…** → `.p12`, with a strong password. Then:

```sh
base64 -i DeveloperID.p12 | gh secret set MACOS_CERTIFICATE
gh secret set MACOS_CERTIFICATE_PASSWORD     # the .p12's password
gh secret set APPLE_TEAM_ID                  # e.g. AB12CD34EF
rm DeveloperID.p12                           # keep a copy in your password manager instead
```

### 3. Notarization credentials (one of the two)

**A. Apple ID and an app-specific password** (what MyGo's guide uses). Create the password at [account.apple.com](https://account.apple.com) → Sign-In and Security → App-Specific Passwords.

```sh
gh secret set APPLE_ID             # the Apple ID of your developer account
gh secret set APPLE_APP_PASSWORD   # the app-specific password (xxxx-xxxx-xxxx-xxxx)
```

**B. An App Store Connect API key** (not tied to your Apple ID's password; revocable on its own). [App Store Connect → Users and Access → Integrations → App Store Connect API](https://appstoreconnect.apple.com/access/integrations/api) → Team Keys → **+**, access **Developer** → download `AuthKey_<KEYID>.p8` (only once) and note the **Key ID** and the **Issuer ID**.

```sh
gh secret set APPLE_API_KEY < AuthKey_ABC123DEFG.p8
gh secret set APPLE_API_KEY_ID     # ABC123DEFG
gh secret set APPLE_API_ISSUER     # the Issuer ID, a UUID
```

With `APPLE_API_KEY` set the workflow uses B, else A.

For releases from your Mac (`scripts/release-local.sh`), store the same credentials once in your keychain under the profile `notary`:

```sh
xcrun notarytool store-credentials notary --apple-id you@example.com --team-id AB12CD34EF --password xxxx-xxxx-xxxx-xxxx
# or: xcrun notarytool store-credentials notary --key AuthKey_ABC123DEFG.p8 --key-id ABC123DEFG --issuer <issuer-uuid>
```

### 4. The update key

```sh
go tool mygo keygen
```

writes `mygo-update.key` (secret) and `mygo-update.pub` (public) to `~/Library/Application Support/mygo/update-keys/`. Installed copies accept only updates signed with this key, for good: **back the .key up in your password manager** (losing it strands every installed copy: they would need a manual reinstall), never commit it, and never replace it (`-force`) once a release is out. Then:

```sh
keys="$HOME/Library/Application Support/mygo/update-keys"
gh secret set MYGO_UPDATER_PRIVATE_KEY < "$keys/mygo-update.key"
gh variable set MYGO_UPDATER_PUBLIC_KEY < "$keys/mygo-update.pub"
```

The public key is a repository **variable**, not a secret: `mygo build` links it into the app. It stays out of `mygo.json` so that copies built elsewhere don't update themselves; the workflow checks the secret key matches it.

### 5. A dry run

GitHub → Actions → **Release** → Run workflow, with **sign** ticked. It builds, signs and notarizes with your secrets, verifies (`scripts/verify-release.sh`), and attaches the disk image and update files to the run as an artifact, without touching any release. Download the disk image and check on your Mac:

```sh
spctl --assess --type open --context context:primary-signature -vv "AudioSmith 0.3.0 arm64.dmg"   # accepted, source=Notarized Developer ID
```

Without **sign** it builds ad hoc and needs no secrets: that checks that the engine and the app build on GitHub's runner.

## Every release

1. In `CHANGELOG.md`, rename `## Unreleased` to the version and date, e.g. `## 0.4.0 — 2026-10-20` (the update window shows this section; the build fails without it). Start a new `## Unreleased` above it when you like.
2. Set `"version"` in `mygo.json` to it. It is the only version: the app's About and Settings, the Local API's `/health`, MCP and the updater all read it (`mygo.App.Version()`).
3. Commit, tag and push:

   ```sh
   git commit -am "AudioSmith 0.4.0"
   git tag v0.4.0
   git push origin main v0.4.0
   ```

4. Actions → Release builds (about 30–60 min the first time, less with the engine cache) and uploads to the draft release `v0.4.0`. Review it — the notes come from `CHANGELOG.md`; edit them if you like — and **Publish release** (or `gh release edit v0.4.0 --draft=false`). Installed copies see it from then on.

A tag that is not `v` + the version in `mygo.json` fails at once and builds nothing. Running the workflow again for a tag replaces the files of its draft.

## Checking an update end to end

Updates need two signed releases: one installed, one newer.

1. Install the older release from its disk image into `/Applications` and open it (on a Mac where you are an administrator, so it can replace itself).
2. Publish the newer one. Check the feed: `curl -sL https://github.com/hehehai/AudioSmith/releases/latest/download/update-darwin-arm64.json | jq '{version, size, deltas: [.deltas[]?.from]}'`.
3. In the older copy: AudioSmith → **Check for Updates…** → the window shows the new version and its notes → **Install Update** → it downloads (the delta if there is one) → **Relaunch**.
4. Settings → Diagnostics and about shows the new version under About, and "Last checked …" under Software updates. The microphone and Accessibility permissions stay granted: both versions carry your Developer ID.

The daily check starts 10 seconds after launch when a day has passed since the last one. Settings → Diagnostics and about turns it off, or on "Install without asking".

## From your Mac instead

`scripts/release-local.sh` does what the workflow does, with your keychain's Developer ID, the `notary` profile and the update keys of step 4:

```sh
scripts/release-local.sh                  # signed, notarized, with updates, in build/darwin-arm64
scripts/release-local.sh --upload         # and uploads to the draft release v<version>
scripts/release-local.sh --skip-notarize  # signed only: quick check of the signing
scripts/release-local.sh --dry-run        # ad hoc, like the workflow's dry run
```

It puts `mygo.json` back when it is done.

## Secrets and variables

| Name | Kind | What | Needed |
|---|---|---|---|
| `MACOS_CERTIFICATE` | secret | the Developer ID Application `.p12`, base64 | signing |
| `MACOS_CERTIFICATE_PASSWORD` | secret | its password | signing |
| `APPLE_TEAM_ID` | secret | your team ID (10 characters) | signing, notarization |
| `APPLE_ID` | secret | Apple ID of the developer account | notarization (A) |
| `APPLE_APP_PASSWORD` | secret | an app-specific password of it | notarization (A) |
| `APPLE_API_KEY` | secret | contents of `AuthKey_<KEYID>.p8` | notarization (B) |
| `APPLE_API_KEY_ID` | secret | its Key ID | notarization (B) |
| `APPLE_API_ISSUER` | secret | the Issuer ID | notarization (B) |
| `MYGO_UPDATER_PRIVATE_KEY` | secret | contents of `mygo-update.key` | updates |
| `MYGO_UPDATER_PUBLIC_KEY` | **variable** | contents of `mygo-update.pub` | updates |

`GITHUB_TOKEN` is the workflow's own; it creates the draft release and uploads to it.

## Notes

- **Runner.** `macos-26` (Apple silicon, Xcode 26). The engine needs Xcode 26 (`swift-tools-version: 6.2`); the workflow picks the newest `Xcode_26*.app` and downloads the Metal toolchain when it is missing. If `macos-26` is unavailable, `macos-15` with Xcode 26 installed works the same.
- **Entitlements.** The app: `com.apple.security.device.audio-input` (it asks for the microphone). The engine: the same (its listen worker records). Nothing else: AudioSmith isn't sandboxed, so it needs no network entitlements; dictation pastes with `CGEventPost`, which the Accessibility permission allows; MLX's Metal kernels are compiled ahead of time, so no JIT. `scripts/verify-release.sh` checks both binaries' signatures, hardened runtime, timestamps and entitlements.
- **Permissions after switching to Developer ID.** Copies signed ad hoc (local builds) and the Developer ID release are different apps to macOS's privacy settings: after installing the first release, grant the microphone and Accessibility again (System Settings → Privacy & Security; remove the old AudioSmith entry first). Later releases keep them.
- **Delta updates** need the earlier releases' archives, which `mygo build` downloads from the published releases (and checks against the key). The first release has none; each later one has deltas from up to three published versions.
- **Notarization failed?** `xcrun notarytool log <submission-id> --keychain-profile notary` (the build prints the ID) lists what Apple rejected.
- **"the update signing key does not match updates.publicKey":** the secret and the variable are of different pairs; set both from the same `mygo keygen` output.
