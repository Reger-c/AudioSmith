---
license: apache-2.0
language:
- zh
- en
library_name: mlx
pipeline_tag: text-to-speech
base_model: OpenMOSS-Team/MOSS-TTSD-v1.0
tags:
- mlx
- text-to-speech
- dialogue
- podcast
- voice-cloning
- moss-ttsd
- moss-tts-family
---

# MOSS-TTSD v1.0 for MLX (8-bit)

[MOSS-TTSD v1.0](https://huggingface.co/OpenMOSS-Team/MOSS-TTSD-v1.0) by OpenMOSS (MOSI.AI), with its [MOSS-Audio-Tokenizer](https://huggingface.co/OpenMOSS-Team/MOSS-Audio-Tokenizer), converted for Apple silicon. It speaks a whole multi-speaker dialogue (1 to 5 speakers; trained on 20 languages, Mandarin and English first) as one recording, in voices cloned from a short reference clip and its transcript per speaker.

This conversion is made for [AudioSmith](https://github.com/hehehai/AudioSmith)'s engine (Swift + MLX), which runs it in its stories' 「整场对话」 (whole dialogue) mode. It is not a drop-in replacement for the official PyTorch checkpoint, and needs a Mac with 24 GB of memory or more (about 12 GB while it speaks).

## Files

| File | What |
|---|---|
| `config.json`, `model-0000N-of-00002.safetensors`, `model.safetensors.index.json` | MOSS-TTSD (MossTTSDelay: a Qwen3-8B backbone, 16 audio-codebook embeddings, 17 heads), upstream's tensor names. Every Linear and Embedding is quantized to 8 bits (MLX affine, group size 64, bfloat16 scales and biases); norms stay bfloat16. 8.9 GB. |
| `codec.safetensors`, `codec_config.json` | MOSS-Audio-Tokenizer (24 kHz ↔ 12.5 frames a second): weight norm folded, 1×1 convolutions as matrices, attention projections renamed, the first 16 of 32 quantizers (the ones MOSS-TTSD reads); Linear layers whose input is a multiple of 64 in 8 bits (float32 scales), the rest float32. 2.0 GB. |
| tokenizer files, `generation_config.json`, `chat_template.jinja` | Upstream's, unchanged. |
| `conversion.json` | The converter and its commit. |
| `LICENSE`, `NOTICE` | Apache-2.0 and the attributions. |

## How it was converted

With [`scripts/mossttsd/convert.py`](https://github.com/hehehai/AudioSmith/blob/main/scripts/mossttsd/convert.py) from OpenMOSS-Team/MOSS-TTSD-v1.0 at revision `c7cd852d87aff71cab5bd2b9b05509cedc0ef1ba` and OpenMOSS-Team/MOSS-Audio-Tokenizer at `3cd226ba2947efa357ef453bcad111b6eafba782` (MLX 0.32).

## How AudioSmith runs it

As the official TTSD Space's voice-clone-and-continuation mode (OpenMOSS/MOSS-TTS `clis/moss_ttsd_app.py`): each speaker's clip is encoded alone into the user message's `Reference(s)`, all clips back to back are encoded once more as the start of the assistant's audio, and the text is every reference transcript as `[Sn] …` followed by the dialogue, through the Space's `normalize_text`. Sampling as the model card recommends and the official `generate()` does it: audio temperature 1.1, top-p 0.9, top-k 50, repetition penalty 1.1 (on every code already in the sequence, prompt included); text channel temperature 1.1, top-p 0.9, top-k 50. Decoding streams 8-second chunks, as the official processor does.

## Parity with the official code

AudioSmith's engine was checked against the official PyTorch implementation (transformers 5.0.0, torch 2.9.1, float32 on the CPU) on the same inputs:

| Check | Result |
|---|---|
| Text normalization and the prompt (570 rows of text + 16 codebooks) | identical |
| Sampling: the processed distributions | identical |
| The delay-pattern generation loop (a tiny random model, greedy, 80 steps) | identical row for row |
| Language model logits, this file's weights in both (float32) | audio heads KL 6·10⁻¹¹, top-1 100 %; text head KL 2·10⁻¹² |
| Language model logits, this 8-bit model vs upstream bfloat16 weights | audio heads KL 0.013, top-1 76 % |
| Codec codes, this file's weights in both | 100 % equal; decoded waveform SNR 101–108 dB |
| Codec, this 8-bit codec vs upstream float32 | codebook 0 99 % equal, all 16 codebooks 86–90 %; decoded waveform SNR 34 dB |

On a MacBook Air M4 (24 GB), an 8-line Mandarin dialogue of two cloned speakers (about 28 s) takes about 36 s (0.79× real time, 11 rows a second) with a 12.2 GB MLX peak; character error rate 0–0.8 %, speaker similarity to the references 0.80–0.90.

## Licence

Apache License 2.0, as the original models; see `LICENSE` and `NOTICE`.

## Usage

Clone a voice only with its owner's consent. Do not impersonate people, and do not use generated speech for fraud, deception or any illegal purpose. You are responsible for what you generate with it.

## Credits

- [MOSS-TTSD](https://huggingface.co/OpenMOSS-Team/MOSS-TTSD-v1.0) and [MOSS-Audio-Tokenizer](https://huggingface.co/OpenMOSS-Team/MOSS-Audio-Tokenizer) (OpenMOSS, MOSI.AI; [MOSS-TTS](https://github.com/OpenMOSS/MOSS-TTS); arXiv 2602.10934).
- [Qwen3](https://github.com/QwenLM/Qwen3) (Alibaba Cloud): the base language model.
- [MLX](https://github.com/ml-explore/mlx), [mlx-swift](https://github.com/ml-explore/mlx-swift), [mlx-swift-lm](https://github.com/ml-explore/mlx-swift-lm).
