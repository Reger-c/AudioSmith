#!/usr/bin/env bash
# Developer setup for the SoulX-Podcast port (thread #80); never shipped.
# Makes, under $SOULX_DEV (default ~/code/soulx-dev):
#   bin/uv, venv/          Python 3.11 with the official reference's
#                          dependencies (torch 2.7.1, transformers 4.57.1,
#                          s3tokenizer, onnxruntime, diffusers) plus mlx-lm
#                          for the 8-bit conversion and huggingface_hub;
#   src/soulx/             the official repo (Soul-AILab/SoulX-Podcast) at
#                          the commit below;
#   upstream/              Soul-AILab/SoulX-Podcast-1.7B at the pinned
#                          revision (LLM, flow.pt, hift.pt, campplus.onnx).
# Then: convert.py (our weights) and reference.py (parity fixtures).
set -euo pipefail
D="${SOULX_DEV:-$HOME/code/soulx-dev}"
SOULX_COMMIT="${SOULX_COMMIT:-5ac9c0e1cfe596396200c7d38e3fd53b7b3fbf4b}"
REVISION="7d381e3aa4f2ac9e23201b57957fd0499f8f2fd1" # Soul-AILab/SoulX-Podcast-1.7B, 2025-12-18
mkdir -p "$D"
cd "$D"
export UV_CACHE_DIR="$D/uvcache" UV_PYTHON_INSTALL_DIR="$D/uvpython" HF_HOME="$D/hf" HF_HUB_DISABLE_TELEMETRY=1
if [[ ! -x bin/uv ]]; then
  curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR="$D/bin" INSTALLER_NO_MODIFY_PATH=1 UV_NO_MODIFY_PATH=1 sh
fi
if [[ ! -d src/soulx ]]; then
  git clone -q https://github.com/Soul-AILab/SoulX-Podcast.git src/soulx
  (cd src/soulx && git checkout -q "$SOULX_COMMIT")
fi
(cd src/soulx && git log -1 --format='SoulX-Podcast %H %ad')
[[ -d venv ]] || bin/uv venv -q -p 3.11 venv
. venv/bin/activate
bin/uv pip install -q "torch==2.7.1" "torchaudio==2.7.1" "transformers==4.57.1" "accelerate==1.10.1" \
  s3tokenizer onnxruntime onnx diffusers einops librosa scipy soundfile numpy safetensors \
  "mlx-lm" huggingface_hub
python -c 'import torch, transformers, mlx.core as mx, mlx_lm; print("torch", torch.__version__, "transformers", transformers.__version__, "mlx", mx.__version__, "mlx_lm", mlx_lm.__version__)'
python - "$REVISION" "$D/upstream" <<'PY'
import sys
from huggingface_hub import snapshot_download
p = snapshot_download("Soul-AILab/SoulX-Podcast-1.7B", revision=sys.argv[1], local_dir=sys.argv[2],
    allow_patterns=["*.json", "*.txt", "model-*.safetensors", "flow.pt", "hift.pt", "campplus.onnx", "README.md"])
print("downloaded", p)
PY
du -sh "$D"/* | sort -h
df -h ~ | tail -1
