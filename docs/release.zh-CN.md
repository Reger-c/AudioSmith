# 发布 AudioSmith（打包、签名、公证、自动更新）

English: [release.md](release.md)

发布 = 推送一个标签 `v<版本号>`。之后 GitHub Actions（[`.github/workflows/release.yml`](../.github/workflows/release.yml)）会在 Apple 芯片的 macOS 机器上：

1. 编译 Swift 引擎（`scripts/build-engine.sh`，需要 Xcode 26 和它的 Metal 工具链）；
2. 用 `go tool mygo build` 打包应用：用你的 **Developer ID** 签名，开启 hardened runtime，带上应用的 entitlements（[`entitlements.plist`](../entitlements.plist)：麦克风）和引擎的 entitlements（[`engine/Support/engine.entitlements`](../engine/Support/engine.entitlements)），做成磁盘映像（DMG），交给 Apple **公证**并把公证票据钉（staple）进去；
3. 生成用你的更新密钥签名的**更新文件**：`audiosmith-<版本>-darwin-arm64.tar.gz`、相对最近最多三个版本的增量包（delta），以及 `update-darwin-arm64.json`（更新清单，更新说明取自 `CHANGELOG.md` 里这个版本的那一节）；
4. 校验构建（`scripts/verify-release.sh`）之后，发布到家族官网 **actnow.dev**（`scripts/release-publish.sh`）。AudioSmith 的下载、更新和更新日志都由官网提供：
   - 把 DMG 以 `audiosmith/AudioSmith-<版本>.dmg` 的名字、连同更新文件一起上传到官网的 R2 存储桶 `actnow-releases`（`go tool mygo publish`，MyGo 补丁 0009），更新清单最后上传。已安装的 AudioSmith 每天读一次 `https://actnow.dev/updates/audiosmith/update-darwin-arm64.json`（或通过菜单 AudioSmith → 检查更新…）：**清单一上传，它们就能看到新版本**；
   - 在官网登记这个版本（`POST https://actnow.dev/api/releases`，`scripts/actnow-register.sh`），官网的[下载页](https://actnow.dev/download/audiosmith)（需要免费账号）和[更新日志](https://actnow.dev/audiosmith/changelog)就会列出它；
   - 创建一个**草稿 GitHub Release**，带更新说明和下载页链接（源码仍在 GitHub，AGPL）。你检查后点发布。

标签带预发布后缀（例如 `v0.4.0-beta.5`）时，做出来的是 **Beta 版**：在官网放在 `audiosmith/beta/` 下，只有打开了「接收 Beta 版更新」的副本才会读；在 GitHub 上是 prerelease（预发布）（见下文 [Beta 版和正式版](#beta-版和正式版更新通道)）。

**从 GitHub 迁到 actnow.dev（0.4.0-beta.5）。** 0.4.0-beta.4 及以前的副本从 GitHub Release 读取更新。`release.yml` 顶部的 `GITHUB_FEED` 为 `"true"` 时，发布还会把更新包放到这个版本的 GitHub Release 上，并附一个指向它的清单，让这些旧副本也能更新到这个版本；从这个版本起，应用读的是 actnow.dev。0.4.0-beta.5 发布之后把它改成 `GITHUB_FEED: "false"`。之后的 GitHub Release 仍会附上官网的清单（指向 actnow.dev），错过过渡版本的旧副本也能找到最新版本。

做法完全按照 MyGo 官方文档：[自动更新](https://github.com/egoist/mygo/blob/main/docs/updates.md)、[updater 插件](https://github.com/egoist/mygo/blob/main/docs/plugins/updater.md)、[打包分发](https://github.com/egoist/mygo/blob/main/docs/distribution.md)、[GitHub Actions](https://github.com/egoist/mygo/blob/main/docs/github-actions.md)。

仓库里**不保存任何发布相关的配置**：`mygo.json` 没有 `updates` 段，也没有签名身份。工作流在构建前用 `scripts/release-config.sh` 临时加上。所以其他所有构建（`go run`、`mygo dev`、`scripts/mac.sh go tool mygo build`、CI）都是 ad-hoc 签名、不会自动更新（`mygo.Updater.Enabled()` 为 false，设置 → 诊断与关于 里也会这样提示）。

## 一次性准备

需要：Apple 开发者账号（你已经有了）、GitHub CLI（先 `gh auth login`）、本仓库在你的 Mac 上，以及官网的凭据（第 5 步）。下面的 `gh secret set` 都在仓库目录里执行。

### 第 1 步：创建 Developer ID Application 证书

只有账号持有人（Account Holder）能创建。两种方式任选：

- Xcode → 设置（Settings）→ Accounts → 选你的团队 → Manage Certificates… → 左下角 **+** → **Developer ID Application**；
- 或者在 [developer.apple.com → Certificates](https://developer.apple.com/account/resources/certificates/list) → **+** → Developer ID → **Developer ID Application**（选 G2 Sub-CA）。需要先用「钥匙串访问」生成 CSR：钥匙串访问 → 证书助理 → 从证书颁发机构请求证书 → 存储到磁盘；上传 CSR，下载证书后双击安装。

检查：终端运行 `security find-identity -v -p codesigning`，应能看到 `Developer ID Application: <你的名字> (<TEAMID>)`。括号里的 10 个字符就是你的 **Team ID**（也可在 developer.apple.com → Membership details 里看到）。

钥匙串里如果还有别的团队（旧账号）的 Developer ID 证书：第 2 步导出时认准新团队的那一张（看括号里的 Team ID）；在本机运行 `scripts/release-local.sh` 时设 `APPLE_TEAM_ID=<新 Team ID>`，脚本才知道用哪一张（有多张又没指定时会报错，不会随便挑一张）。

### 第 2 步：导出 .p12，设置签名相关的 secrets

打开「钥匙串访问」→ 登录（login）→ 我的证书 → 找到 Developer ID Application 证书（展开能看到下面的私钥）→ 右键 → **导出…** → 格式选 `.p12`，设一个强密码。然后：

```sh
base64 -i DeveloperID.p12 | gh secret set MACOS_CERTIFICATE
gh secret set MACOS_CERTIFICATE_PASSWORD     # 输入 .p12 的密码
gh secret set APPLE_TEAM_ID                  # 输入 Team ID，例如 AB12CD34EF
rm DeveloperID.p12                           # 本地不要留，备份放到密码管理器里
```

### 第 3 步：公证凭据（两种方式选一种）

**方式 A：Apple ID + App 专用密码**（MyGo 文档用的方式，最简单）。在 [account.apple.com](https://account.apple.com) → 登录与安全 → App 专用密码 里生成一个。

```sh
gh secret set APPLE_ID             # 开发者账号的 Apple ID（邮箱）
gh secret set APPLE_APP_PASSWORD   # 刚生成的 App 专用密码（xxxx-xxxx-xxxx-xxxx）
```

**方式 B：App Store Connect API 密钥**（不依赖 Apple ID 密码，可单独吊销）。[App Store Connect → 用户和访问 → 集成 → App Store Connect API](https://appstoreconnect.apple.com/access/integrations/api) → 团队密钥 → **+**，访问权限选 **Developer** → 下载 `AuthKey_<KEYID>.p8`（只能下载一次），记下 **密钥 ID（Key ID）** 和 **Issuer ID**。

```sh
gh secret set APPLE_API_KEY < AuthKey_ABC123DEFG.p8
gh secret set APPLE_API_KEY_ID     # 例如 ABC123DEFG
gh secret set APPLE_API_ISSUER     # Issuer ID（一串 UUID）
```

设置了 `APPLE_API_KEY` 时工作流用方式 B，否则用方式 A。

如果也想在自己的 Mac 上发布（`scripts/release-local.sh`），把同样的凭据存进钥匙串，配置名叫 `notary`（只需一次）：

```sh
xcrun notarytool store-credentials notary --apple-id 你的AppleID --team-id AB12CD34EF --password xxxx-xxxx-xxxx-xxxx
# 或：xcrun notarytool store-credentials notary --key AuthKey_ABC123DEFG.p8 --key-id ABC123DEFG --issuer <Issuer ID>
```

### 第 4 步：生成更新签名密钥

```sh
go tool mygo keygen
```

它会在 `~/Library/Application Support/mygo/update-keys/` 写出 `mygo-update.key`（私钥）和 `mygo-update.pub`（公钥）。已安装的 AudioSmith **永远只接受用这把私钥签名的更新**，所以：

- **一定把 .key 备份到密码管理器**（丢了的话，所有已安装的用户只能手动重新下载安装）；
- 绝不要提交到仓库；
- 发布过之后不要再用 `-force` 重新生成。

然后设置：

```sh
keys="$HOME/Library/Application Support/mygo/update-keys"
gh secret set MYGO_UPDATER_PRIVATE_KEY < "$keys/mygo-update.key"
gh variable set MYGO_UPDATER_PUBLIC_KEY < "$keys/mygo-update.pub"
```

注意公钥是仓库的 **variable（变量）**，不是 secret：它是公开的，`mygo build` 会把它编进应用。之所以不写进 `mygo.json`，是为了让别处构建的副本不会自动更新；工作流会检查私钥和公钥是否是同一对。

### 第 5 步：官网的 R2 密钥和登记 token

- **R2**：Cloudflare 控制台 → R2 → Manage API tokens → 创建一个 **API token**，权限 **Object Read & Write**，只限存储桶 `actnow-releases`。记下它的 **Access Key ID** 和 **Secret Access Key**（S3 凭据；endpoint `https://8a8325619b834f43256fd77f030b11a2.r2.cloudflarestorage.com` 写在 `scripts/actnow.sh` 里）。
- **登记 token**：官网给 audiosmith 的 release-ingest token（ActNow 项目把它放在 `~/.config/actnow/ingest-tokens.env`）。

```sh
gh secret set R2_ACCESS_KEY_ID
gh secret set R2_SECRET_ACCESS_KEY
gh secret set ACTNOW_INGEST_TOKEN
```

MyGo 上传时读的是 `AWS_ACCESS_KEY_ID` / `AWS_SECRET_ACCESS_KEY`，`scripts/release-publish.sh` 会把 R2 的名字映射过去。打标签触发的构建缺这些 secret 时，会在构建前就报出缺哪几个并停止；试跑三个都不需要。

### 第 6 步：试跑一次（不发布）

GitHub → Actions → **Release** → Run workflow，勾选 **sign**。它会用你的 secrets 构建、签名、公证，跑校验脚本（`scripts/verify-release.sh`），显示正式发布时会发布什么（`scripts/release-publish.sh --dry-run`：要上传到 R2 的文件、登记请求的内容、GitHub 上的文件），并把 DMG、更新文件和 GitHub 文件作为这次运行的附件（artifact）上传，**不会碰官网和任何 Release**。下载 DMG，在 Mac 上检查：

```sh
spctl --assess --type open --context context:primary-signature -vv AudioSmith-0.4.0-beta.5.dmg
# 应显示 accepted，source=Notarized Developer ID
```

不勾 **sign** 时是 ad-hoc 构建，不需要任何 secrets，用来确认引擎和应用能在 GitHub 的机器上编译通过。

## 每次发布

Beta 版（`0.4.0-beta.2`）和正式版（`0.4.0`）步骤完全一样，由版本号决定是哪一种。

1. 在 `CHANGELOG.md` 里把 `## Unreleased` 改成版本号和日期，例如 `## 0.4.0 — 2026-10-20` 或 `## 0.4.0-beta.2 — 2026-10-18`（更新窗口显示的就是这一节；缺了这一节构建会直接失败）。标题的第一个词必须**正好**是版本号：`## 0.4.0-beta.1` 不算 0.4.0 的那一节，所以 Beta 之后的正式版要有自己的一节（可以把各个 Beta 的内容汇总进去）。之后可以在上面再新开一个 `## Unreleased`。
2. 把 `mygo.json` 的 `"version"` 改成同一个版本号。这是唯一的版本号来源：关于页、设置、本地 API 的 `/health`、MCP 和更新器都读它（`mygo.App.Version()`）。
3. 确认应用里带的第三方许可文本 `THIRD-PARTY-NOTICES.txt` 是最新的：`scripts/third-party-notices.sh --check`。如果不是（写入之后依赖变过；CI 的 `go test` 也会失败），运行 `scripts/third-party-notices.sh`（Swift 依赖的版本变过时加 `--fetch`），把文件和这次发布一起提交。
4. 提交、打标签、推送：

   ```sh
   git commit -am "AudioSmith 0.4.0"
   git tag v0.4.0
   git push origin main v0.4.0
   ```

5. Actions → Release 开始构建（第一次约 30–60 分钟，之后有引擎缓存会快些），校验通过后发布到 actnow.dev：**已安装的副本马上就能收到**（Beta 版：打开了 Beta 更新的副本），官网的下载页和更新日志也会列出它。检查：`curl -s https://actnow.dev/updates/audiosmith/beta/update-darwin-arm64.json | jq '{version, url, deltas: [.deltas[]?.from]}'`（正式版去掉 `beta/`）。
6. 检查草稿 GitHub Release `v0.4.0`（说明来自 `CHANGELOG.md`，附下载链接），然后点 **Publish release**（或者 `gh release edit v0.4.0 --draft=false`）。
   - **Beta 版**的草稿已经标成 **pre-release**。发布时保持「Set as a pre-release」勾选（不要勾「Set as the latest release」）。
   - `GITHUB_FEED` 为 `"true"` 时，仍读 GitHub 的旧副本在它发布后才会更新。

标签和 `mygo.json` 里的版本不一致时（例如打了 `v0.4.1` 但版本还是 `0.4.0`；由 `scripts/release-check.sh` 检查），工作流第一步就会失败，什么都不构建。对同一个标签重新运行工作流，会重新构建并覆盖上传的文件（同名），更新官网的登记（官网返回 200）和草稿里的文件。发布中途失败时，也可以用已有的构建手动完成：环境里设好 secrets，运行 `scripts/release-publish.sh`。

撤回已发布的版本：在官网后台（/panel/releases）隐藏它，并重新上传上一个版本的清单（或者发一个修复版）；已经装上的副本会保留它。

## Beta 版和正式版（更新通道）

设置 → 诊断与关于 → 软件更新 → **接收 Beta 版更新** 决定更新通道（MyGo 补丁 `0003-update-prereleases.patch` 和 `0008-update-url-prereleases.patch`，`mygo.Updater.SetPrereleases`）：

- **关闭：只收正式版。** 读 `https://actnow.dev/updates/audiosmith/update-darwin-arm64.json`（R2 `audiosmith/`）。
- **打开：Beta 版和正式版都收。** 同时读它和 `https://actnow.dev/updates/audiosmith/beta/update-darwin-arm64.json`（R2 `audiosmith/beta/`），取较新的那个（`0.4.0-beta.1 < 0.4.0-beta.2 < 0.4.0 < 0.4.1-beta.1`）。Beta 版的文件都放在 `beta/` 下；正式版发布时也会把自己的清单写进 `beta/`（除非那里已经有更新的 Beta），所以 Beta 清单总是两者中最新的版本。没有 Beta 清单（404）时只读正式版的。
- **默认值：** Beta 版副本默认打开（装了 Beta 的人想要下一个 Beta），正式版默认关闭，直到用户自己选（存在 `settings.Updates.Beta`）。
- **不会降级。** 在 Beta 版里关掉它，会一直停留在这个 Beta，直到有比它新的正式版：0.4.0-beta.5 关掉 Beta 后会更新到 0.4.0，绝不会退回 0.3.x。
- **增量包**两个通道都有：`mygo build` 会同时参考两份清单，取最新的最多三个版本做增量，所以 0.4.0-beta.6 有相对 0.4.0-beta.5 的增量，0.4.0 有相对各个 Beta 和上一个正式版的增量。0.4.0-beta.5 是官网上的第一个版本，没有增量（在它之前官网还没有清单）。
- **Mac 上的版本号。** MyGo 把完整版本号（`0.4.0-beta.1`）写进 `CFBundleShortVersionString` 和 `CFBundleVersion`，`mygo.App.Version()` 再从那里读回来；更新器拿它来比较版本，所以必须保留预发布后缀。只有 App Store 要求纯数字版本号，Developer ID 公证和 Gatekeeper 不检查（VS Code Insiders 也是这样发 `1.x.0-insider` 的）。万一公证拒绝，构建日志里 `notarytool log` 会写明原因。
- 「关于」里 Beta 版的版本号旁边有一个 **Beta** 标记。

**0.4.0-beta.1 … beta.4 的副本**仍然读 GitHub（`scripts/release-config.sh` 加 `UPDATES_FEED=github` 可以构建这样的副本）：关闭 Beta 时读 `releases/latest/download/update-darwin-arm64.json`，打开时通过 GitHub API 找最新的 Release 或 prerelease（补丁 0003）。`scripts/release-publish.sh` 创建的每个 GitHub Release 都给它们附一个 `update-darwin-arm64.json`：过渡期（`GITHUB_FEED=true`）指向 GitHub Release 上的更新包，过渡之后就是官网的那份。签名的只是更新包（清单里带着它们的签名），清单本身不签名，所以两份清单装的是同一个签过名的更新包。检查：`curl -sL https://github.com/hehehai/AudioSmith/releases/download/v0.4.0-beta.5/update-darwin-arm64.json | jq '{version, url}'`。

## 端到端验证一次更新

验证更新需要两个签名过的版本：一个装着，一个更新的。

1. 从旧版本的 DMG 把 AudioSmith 拖到 `/Applications` 并打开（用管理员账户，这样它才能替换自己）。
2. 发布新版本。检查更新源：`curl -s https://actnow.dev/updates/audiosmith/update-darwin-arm64.json | jq '{version, size, deltas: [.deltas[]?.from]}'`（Beta 版：`…/updates/audiosmith/beta/update-darwin-arm64.json`；只发过 Beta 时正式版的清单是 404）。
3. 在旧版本里：菜单 AudioSmith → **检查更新…** → 窗口显示新版本和更新说明 → **安装更新** → 下载（有增量包时只下增量）→ **立即重新启动**。
4. 设置 → 诊断与关于：「关于」里显示新版本号，「软件更新」里显示「上次检查：…」。麦克风和辅助功能权限保持不变（两个版本都是你的 Developer ID 签名）。

自动检查：每天一次，启动 10 秒后若距上次检查已满一天就检查。设置 → 诊断与关于 → 软件更新 里可以关掉自动检查，或打开「无需询问直接安装」。

## 在自己的 Mac 上发布（可选）

`scripts/release-local.sh` 和工作流做的事一样，使用你钥匙串里的 Developer ID、`notary` 配置和第 4 步的更新密钥：

```sh
scripts/release-local.sh                  # 签名 + 公证 + 更新文件，输出在 build/darwin-arm64
scripts/release-local.sh --upload         # 同上，并发布：先 actnow.dev，再草稿 GitHub Release
scripts/release-local.sh --skip-notarize  # 只签名不公证：快速检查签名
scripts/release-local.sh --dry-run        # ad-hoc，与工作流的试跑一样
```

`--upload` 会运行 `scripts/release-publish.sh`，需要环境里有 `R2_ACCESS_KEY_ID`、`R2_SECRET_ACCESS_KEY` 和 `ACTNOW_INGEST_TOKEN`（过渡版本再加 `GITHUB_FEED=true`）。脚本结束后会把 `mygo.json` 恢复原样。

这几个脚本也可以单独运行：`scripts/release-publish.sh [--dry-run] [--skip-github]` 发布 `build/darwin-arm64` 里已有的构建；`scripts/actnow-register.sh --dmg <文件> [--version …] [--dry-run]` 重新在官网登记一个版本。

## Secrets 和变量一览

| 名称 | 类型 | 内容 | 用途 |
|---|---|---|---|
| `MACOS_CERTIFICATE` | secret | Developer ID Application 的 `.p12`，base64 | 签名 |
| `MACOS_CERTIFICATE_PASSWORD` | secret | `.p12` 的密码 | 签名 |
| `APPLE_TEAM_ID` | secret | Team ID（10 个字符） | 签名、公证 |
| `APPLE_ID` | secret | 开发者账号的 Apple ID | 公证（方式 A） |
| `APPLE_APP_PASSWORD` | secret | App 专用密码 | 公证（方式 A） |
| `APPLE_API_KEY` | secret | `AuthKey_<KEYID>.p8` 的内容 | 公证（方式 B） |
| `APPLE_API_KEY_ID` | secret | 密钥 ID | 公证（方式 B） |
| `APPLE_API_ISSUER` | secret | Issuer ID | 公证（方式 B） |
| `MYGO_UPDATER_PRIVATE_KEY` | secret | `mygo-update.key` 的内容 | 更新签名 |
| `MYGO_UPDATER_PUBLIC_KEY` | **variable** | `mygo-update.pub` 的内容 | 更新签名 |
| `R2_ACCESS_KEY_ID` | secret | R2 API token 的 Access Key ID（对 `actnow-releases` 有读写权限） | 正式发布（试跑不需要） |
| `R2_SECRET_ACCESS_KEY` | secret | 它的 Secret Access Key | 正式发布（试跑不需要） |
| `ACTNOW_INGEST_TOKEN` | secret | 官网给 audiosmith 的 release-ingest token | 正式发布（试跑不需要） |

`GITHUB_TOKEN` 是工作流自带的，用来创建草稿 Release 并上传文件，不用设置。

## 补充说明

- **构建机器**：`macos-26`（Apple 芯片，Xcode 26）。引擎需要 Xcode 26（`swift-tools-version: 6.2`）；工作流会选最新的 `Xcode_26*.app`，缺 Metal 工具链时自动下载。如果 `macos-26` 不可用，装有 Xcode 26 的 `macos-15` 也一样能用。
- **Entitlements**：应用只需要 `com.apple.security.device.audio-input`（会申请麦克风）；引擎同样只需要它（listen 进程录音）。其他都不需要：AudioSmith 不是沙盒应用，不需要网络 entitlement；听写自动粘贴用的是 `CGEventPost`，靠「辅助功能」权限而不是 entitlement；MLX 的 Metal 内核是预编译的，不需要 JIT。`scripts/verify-release.sh` 会检查两个二进制的签名、hardened runtime、时间戳和 entitlements。
- **从 ad-hoc 换到 Developer ID 后的权限**：对 macOS 的隐私设置来说，本地 ad-hoc 构建和 Developer ID 正式版是两个不同的应用。第一次装正式版后，需要重新授予麦克风和辅助功能权限（系统设置 → 隐私与安全性；先删掉旧的 AudioSmith 条目）。之后的正式版更新会保留这些权限。
- **许可证**：0.4.0-beta.1 之后的版本采用 GNU AGPL-3.0（之前的版本是 Apache-2.0）。应用的 `Contents/Resources` 里带有 `LICENSE`、`NOTICE` 和 `THIRD-PARTY-NOTICES.txt`（所有第三方组件完整的许可和 NOTICE 文本，由 `scripts/third-party-notices.sh` 生成；mygo.json 的 `resources`），访达里显示的版权信息也写明了许可（`copyright` → `NSHumanReadableCopyright`），设置 → 诊断与关于 里有源代码链接，也能打开第三方许可。发布版本从公开仓库的 tag 构建，GitHub 会在 Release 上附上对应的源码包：保留 tag，不要发布源码没有推送的构建。
- **增量更新**：`mygo build` 会按官网已发布的清单（正式版和 `beta/`）下载之前版本的更新包（并用公钥校验）来生成增量包。官网上的第一个版本（0.4.0-beta.5）没有增量；之后每个版本都有相对最近最多三个已发布版本的增量包。
- **DMG 不公开**：官网只通过需要登录的下载页提供 `audiosmith/…/*.dmg`；`/updates/audiosmith/` 只提供清单、更新包和增量包（`update-*.json` 不缓存，其余长期缓存）。
- **公证失败**：构建日志里有 submission ID，用 `xcrun notarytool log <ID> --keychain-profile notary` 查看 Apple 拒绝的原因。
- **报错「the update signing key does not match updates.publicKey」**：secret 里的私钥和变量里的公钥不是同一对，用同一次 `mygo keygen` 的输出重新设置两者。
