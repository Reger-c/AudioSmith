---
license: apache-2.0
language:
- zh
- en
library_name: mlx
pipeline_tag: text-to-speech
base_model: Soul-AILab/SoulX-Podcast-1.7B
tags:
- mlx
- text-to-speech
- dialogue
- podcast
- voice-cloning
- soulx-podcast
- cosyvoice2
---

# SoulX-Podcast-1.7B for MLX (8-bit)

[SoulX-Podcast-1.7B](https://huggingface.co/Soul-AILab/SoulX-Podcast-1.7B) by Soul AI Lab, converted for Apple silicon. It speaks a multi-speaker dialogue (up to 4 speakers, Mandarin and English) turn by turn, each turn hearing the ones before it, in voices cloned from a short reference clip and its transcript per speaker.

This conversion is made for [AudioSmith](https://github.com/hehehai/AudioSmith)'s engine (Swift + MLX), which runs it in its stories' 「整场对话」 (whole dialogue) mode. It is not a drop-in replacement for the official PyTorch checkpoint.

## Files

| File | What |
|---|---|
| `config.json`, `model.safetensors` | The Qwen3-1.7B language model (vocabulary 159,488: text, 6,561 speech tokens, tags), quantized to 8 bits with mlx-lm (group size 64, bfloat16 scales; 8.5 bits per weight). The output layer is tied to the embeddings. |
| `s3gen.safetensors` | CosyVoice2's flow matching (encoder + CFM decoder), HiFT vocoder (`mel2wav.*`) and CAM++ speaker encoder (`speaker_encoder.*`), float32, keyed for mlx-audio-swift's `CausalMaskedDiffWithXvec`. |
| tokenizer files, `generation_config.json`, `soulxpodcast_config.json` | Upstream's, unchanged. |
| `conversion.json` | The converter and its commit. |
| `LICENSE`, `NOTICE` | Apache-2.0 and the attributions. |

The S3 speech tokenizer (25 Hz) is not included: use [mlx-community/S3TokenizerV2](https://huggingface.co/mlx-community/S3TokenizerV2).

## How it was converted

With [`scripts/soulx/convert.py`](https://github.com/hehehai/AudioSmith/blob/main/scripts/soulx/convert.py) from Soul-AILab/SoulX-Podcast-1.7B at revision `7d381e3aa4f2ac9e23201b57957fd0499f8f2fd1`:

- the language model: `mlx_lm` convert, 8 bits, group size 64, bfloat16;
- `flow.pt` and `hift.pt`: modules renamed, weight norm folded, convolutions transposed to MLX's layout;
- `campplus.onnx`: initializers renamed; the batch norms the ONNX exporter had folded into convolutions are written back as identity batch norms carrying the folded biases.

The flow, HiFT and CAM++ weights are byte-identical upstream to [FunAudioLLM/CosyVoice2-0.5B](https://huggingface.co/FunAudioLLM/CosyVoice2-0.5B)'s.

## Parity with the official code

AudioSmith's engine was checked against the official PyTorch implementation (soulxpodcast @ 5ac9c0e, transformers 4.57.1) on the same inputs:

| Stage | Result |
|---|---|
| 16 kHz log-mel / S3 tokens | max difference 1.3·10⁻⁴; 100 % of tokens equal |
| CAM++ x-vector | cosine 0.999998 |
| 24 kHz flow mel | max difference 1.5·10⁻⁴ |
| Prompt token ids | identical |
| Language model logits, float32 | identical top 100, KL < 10⁻⁴ |
| This 8-bit model vs float32 | argmax equal, top-100 overlap 99, KL 0.0014 |
| Sampling (repetition penalty → temperature → top-k → top-p, RAS) | processed scores identical |
| Flow matching with fixed noise | correlation 1.00000 |
| HiFT waveform (no random phase/noise) | SNR 52 dB |

Sampling must follow upstream: repetition penalty 1.25 on the current turn's tokens, temperature 0.6, top-k 100, top-p 0.9, repetition-aware sampling (window 25, ratio 0.2), 8 to 3,000 tokens a turn, stop at token 151675. (Dropping temperature and top-k/p, as one community port did, raises the character error rate from about 1 % to 4–10 %.)

## Licence

Apache License 2.0, as the original model; see `LICENSE` and `NOTICE`.

## Usage disclaimer

From the original model card: this model is for research and legitimate use. **Do not clone anyone's voice without their consent, do not impersonate people, and do not use it for fraud, deception or any illegal purpose.** You are responsible for what you generate with it.

## Credits

- [SoulX-Podcast](https://github.com/Soul-AILab/SoulX-Podcast) (Soul AI Lab, Northwestern Polytechnical University and others; arXiv 2510.23541).
- [CosyVoice2](https://github.com/FunAudioLLM/CosyVoice) (Alibaba Tongyi SpeechTeam): flow matching, HiFT, S3 tokenizer.
- [3D-Speaker](https://github.com/modelscope/3D-Speaker): CAM++.
- [Qwen3](https://github.com/QwenLM/Qwen3) (Alibaba Cloud): the base language model.
- [MLX](https://github.com/ml-explore/mlx), [mlx-lm](https://github.com/ml-explore/mlx-lm), [mlx-audio-swift](https://github.com/Blaizzy/mlx-audio-swift).
