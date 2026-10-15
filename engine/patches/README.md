# AudioSmith's patches to mlx-audio-swift

`scripts/fetch-engine-deps.sh` checks out upstream [Blaizzy/mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift) at the commit it pins and applies these patches in name order into `engine/.build/deps/mlx-audio-swift`, which `engine/Package.swift` uses as a local package. Keep each patch small and send it upstream; drop it when upstream has the fix.

| Patch | Why |
|---|---|
| `0001-kokoro-eval-before-yield.patch` | mlx-swift 0.32 gives each thread its own default stream. Kokoro's `generateStream` yields an unevaluated array from its own `Task`; the consumer then crashes evaluating it ("There is no Stream(gpu, N) in current thread"). Evaluate before yielding. |
| `0002-whisper-task-and-language-detection.patch` | `WhisperModel.task` ("transcribe"/"translate"), and language detection when none is given (the logits after `<|startoftranscript|>`, as OpenAI's `detect_language`), so results report the language. |
| `0003-chatterbox-language-and-spaces.patch` | The regular Chatterbox tokenizer splits on whitespace, so spaces must be spelled `[SPACE]` as in Resemble's tokenizer; the multilingual model also takes lowercase NFKD text, a `[lang]` prefix, Cangjie codes for Chinese (`Cangjie5_TC.json`) and jamo for Korean (Resemble's MTLTokenizer, MIT). |
| `0004-kitten-read-voices-npz.patch` | The KittenTTS 0.8 repos ship their voices as `voices.npz`, which the loader refused ("convert to safetensors"): read the stored zip of float32 `.npy` arrays directly; and refuse inputs over 510 phonemes (ALBERT has 512 positions; longer ones crashed MLX) as Kokoro does. |
| `0005-whisper-initial-prompt.patch` | `WhisperModel.initialPrompt`: the transcription request's `prompt` (dictation vocabulary, names) goes after `<|startofprev|>` before the SOT sequence of every 30 s window, at most 223 tokens (its end kept), as OpenAI's `initial_prompt`. Upstream Whisper took no prompt, so the engine passed `prompt` to Qwen3-ASR only (thread #44 §8.10). |

Make a patch: change the checkout in `engine/.build/deps/mlx-audio-swift`, `git diff > engine/patches/mlx-audio-swift/000N-name.patch`, then rebuild (the stamp in the checkout notices the new patch and re-applies the series from a clean checkout).
