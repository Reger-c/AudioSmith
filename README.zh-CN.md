# AudioSmith

[English](README.md) · **简体中文**

AudioSmith 是一款运行在 Apple 芯片 Mac 上的本地语音工作室。

它能做：文字转语音（预设、克隆、设计音色）、多角色故事与有声书、视频配音、转写与全局听写，另外提供本地 OpenAI 兼容 API、MCP 服务和工作流。

所有模型都用 [MLX](https://github.com/ml-explore/mlx) 在 Mac 的 GPU 上运行。不需要 Python，不需要云账号；除非你主动选择，数据不会离开这台 Mac。

> **状态：0.3 预览版。** 已实现 VoiceStudio 开源版的全部功能，并已在 0.2 完成验证；0.3 重新设计了界面，可能还有粗糙的地方，见[已知限制](#已知限制)。

![工作室](docs/screenshots/studio.png)

## 安装

**要求**
- macOS 15 及以上，Apple 芯片（M1 及以上）。
- 建议 16 GB 内存；8 GB 可用轻量模型。
- 模型需要约 1.3–15 GB 磁盘空间，取决于模型包（快速 1.3 GB、均衡 4.7、高质量 12.8、最强 14.9）。

**步骤**
1. 从 [Releases](https://github.com/hehehai/AudioSmith/releases) 下载 DMG，或[从源码构建](README.md#build-from-source)。
2. 把 **AudioSmith** 拖进“应用程序”。
3. 目前是 ad-hoc 签名，首次打开会被 macOS 拦截。任选一种方式放行：
   - 在终端执行 `xattr -dr com.apple.quarantine /Applications/AudioSmith.app`；
   - 或先尝试打开一次，再到 **系统设置 → 隐私与安全性** 点 **仍要打开**（macOS 15 起，右键 → 打开已不能绕过拦截）。
4. 首次运行会进入设置向导：
   - **检查这台 Mac**：系统版本、芯片、内存、磁盘、网络、权限。
   - **推荐模型包**：根据内存推荐“快速 / 均衡 / 高质量 / 最强”之一，也可以跳过，之后在“模型”页下载。
   - **隐私和听写**：设置完成后，用 Kokoro 跟你打声招呼。

模型从 huggingface.co 或 hf-mirror.com 下载，自动选更快的那个，并走系统代理。国内网络下载慢时，可在 **设置 → 网络** 选择 hf-mirror.com。

**权限**（用到时才会请求）
- **麦克风**：录制参考音、听写、实时转写。
- **辅助功能**：听写时把文字输入到其他应用。

ad-hoc 签名的版本每次更新后，macOS 会忘掉辅助功能授权，需要在 **系统设置 → 隐私与安全性 → 辅助功能** 里重新勾选 AudioSmith。

## 功能

| 模块 | 能做什么 |
|---|---|
| **工作室** | 支持标记的脚本编辑器：停顿、重音、快慢、`[[词\|读法]]`、非语言标签。<br>可导入 TXT/MD/DOCX/EPUB/SRT/VTT。<br>参数随模型变化：音色、语言、语速、步数、引导强度、目标时长、seed。<br>长文本分段生成，边生成边播放，带分段缓存。<br>音量均衡到 −18 LUFS、发音词典、音频质检。<br>导出 WAV（16/24/32 位）；装了 ffmpeg 后还能导出 MP3/FLAC/Opus。 |
| **音色发现 / 我的音色 / 设计** | 从录音或任意音视频文件克隆音色：自动选最佳片段、自动转写、降噪，可录制本人授权声明。<br>用中英文描述设计音色（Qwen3 VoiceDesign、OmniVoice）。<br>约 1,200 个原型音色可试听。<br>`.asvoice` 导入导出，兼容 VoiceStudio 的 `.ovsvoice`。 |
| **故事 / 有声书 / 项目库** | 多角色剧本，自动分配角色。<br>有声书：导入 EPUB 等，按章节渲染；每章符合 ACX 或播客响度标准；跟读播放器；导出带章节和封面的 M4B。<br>可断点续渲，只重渲改过的行。 |
| **视频配音 / 批量** | 分离人声与背景、识别说话人（≤4 人）、转写。<br>翻译可用本地 LLM、Ollama、云端模型、DeepL、Google、微软，支持术语表和按时长改写。<br>逐行克隆配音并贴合时长。<br>时间线编辑、A/B 对比、视频预览、增量重配。<br>导出多音轨 MP4（带软字幕）、M4A、WAV、SRT/VTT/ASS、分轨。<br>支持批量队列和监控文件夹。 |
| **转写 / 听写** | 词级时间戳；导出 SRT/VTT/TXT/JSON；可用 LLM 清理文字。<br>全局听写：⌘⇧Space 切换或按住说话，文字直接输入任意应用，并保留原剪贴板。<br>听写约 0.3 秒出结果。 |
| **本地 LLM** | 内置 Qwen3 4B（默认）或 Gemma 4 E2B（轻量），用于离线翻译、文字清理、术语提取。<br>调用顺序：本机 → Ollama；云端只有你主动选择才会用。 |
| **本地 API / MCP / 集成 / 工具** | `127.0.0.1:3901` 上的 OpenAI 兼容 `/v1`，另有原生 `/api` 和 WebSocket 流式接口。<br>MCP 支持 HTTP 和 stdio。<br>提供 Claude Code、Cursor、Codex、n8n、OpenAI SDK 的配置页。<br>工具：A/B 对比、语音转语音、语速贴合、语气指令、水印检测。 |
| **工作流** | 节点画布：朗读、转写、翻译、换声、响度、条件、导出等步骤。<br>可续跑，最多 50 个输入，带模板。 |
| **电话** | 基于你自己的 Twilio 账号的 AI 电话代理。<br>强制播报 AI 身份声明，只能使用设计音色或已验证授权的克隆音色，可人工接管。<br>未设置前不生效。 |
| **负责任使用** | 生成的音频默认带 AudioSeal 隐形水印，可在“设置 → 隐私”关闭。<br>克隆需要授权录音，非商用模型需要先确认许可。 |

## 模型

| 任务 | 默认 | 其他 | 许可 |
|---|---|---|---|
| 语音合成 | Kokoro 82M | Qwen3-TTS 0.6B/1.7B、Chatterbox、MOSS-Nano、Kitten、Pocket、Soprano、Marvis | Apache-2.0 / MIT / CC-BY-4.0 |
| 646 种语言合成 | — | **OmniVoice**（克隆、音色设计、精确时长） | **CC-BY-NC-4.0，禁止商用**，使用前需确认许可 |
| 转写 | Whisper large-v3 turbo | Qwen3-ASR 0.6B（听写默认）、Parakeet v3 | MIT / Apache-2.0 / CC-BY-4.0 |
| 分离 / 说话人 | HTDemucs、Sortformer | Mel-RoFormer | MIT / NVIDIA Open Model |
| 语言模型 | Qwen3 4B Instruct 2507 | Gemma 4 E2B | Apache-2.0 |
| 水印 | AudioSeal | | MIT |

Marvis 标注为 Apache-2.0，但它的训练数据 Emilia 不允许商用。商用前请自行确认。

## 已知限制

- 目前是 ad-hoc 签名，还没有公证和自动更新。
- ffmpeg 不内置，属于可选：装了 Homebrew 版 ffmpeg 后，才能导出 MP3/Opus/FLAC、读取 WebM/MKV、给视频烧录硬字幕。
- 说话人识别最多 4 人；听写要说完一句才出文字，不能边说边出；界面只有英文和简体中文。
- 时间拉伸超过约 ±5% 时，水印会失效。因此配音的水印在贴合时长之后再加。

开发、构建、测试和架构说明见[英文 README](README.md)与 [docs/architecture.md](docs/architecture.md)。

## 许可证

[Apache-2.0](LICENSE)。模型权重各有自己的许可，见上方“模型”一节。界面图标为 480 Design 的 [Solar](https://icon-sets.iconify.design/solar/)，CC BY 4.0 许可（见 [NOTICE](NOTICE)）。
