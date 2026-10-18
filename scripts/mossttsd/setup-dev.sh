#!/usr/bin/env bash
# Developer setup for the MOSS-TTSD port (thread #82); never shipped.
#
#   scripts/mossttsd/setup-dev.sh [venv|tok|ttsd|all]
#
# Makes, under $MOSS_DEV (default <repo>/../moss-dev):
#   bin/uv, venv/      Python 3.12 with the official reference's dependencies
#                      (torch 2.9.1 on the CPU, transformers 5.0.0, as the
#                      model card asks) plus mlx for the 8-bit conversion and
#                      huggingface_hub;
#   src/moss-tts/      OpenMOSS/MOSS-TTS at the commit below (the TTSD Space's
#                      text normalization and conversation recipe);
#   upstream/tok/      OpenMOSS-Team/MOSS-Audio-Tokenizer at the pinned
#                      revision (7.1 GB, float32);
#   upstream/ttsd/     OpenMOSS-Team/MOSS-TTSD-v1.0 at the pinned revision
#                      (16.7 GB, bfloat16).
# The two upstream folders are large: download one, convert it and make its
# fixtures (parity.sh), then delete it (the Mac keeps ≥ 6 GB free).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
D="${MOSS_DEV:-$(dirname "$ROOT")/moss-dev}"
MOSS_TTS_COMMIT="934d6826b084c46a0d033402174d5f8ac4ed2519" # OpenMOSS/MOSS-TTS, 2026-09-06
TTSD_REVISION="c7cd852d87aff71cab5bd2b9b05509cedc0ef1ba"   # OpenMOSS-Team/MOSS-TTSD-v1.0
TOK_REVISION="3cd226ba2947efa357ef453bcad111b6eafba782"    # OpenMOSS-Team/MOSS-Audio-Tokenizer
step="${1:-venv}"
mkdir -p "$D"
cd "$D"
export UV_CACHE_DIR="$D/uvcache" UV_PYTHON_INSTALL_DIR="$D/uvpython" HF_HOME="$D/hf" HF_HUB_DISABLE_TELEMETRY=1
if [[ $step == venv || $step == all ]]; then
  if [[ ! -x bin/uv ]]; then
    curl -LsSf https://astral.sh/uv/install.sh | env UV_INSTALL_DIR="$D/bin" INSTALLER_NO_MODIFY_PATH=1 UV_NO_MODIFY_PATH=1 sh
  fi
  if [[ ! -d src/moss-tts ]]; then
    git clone -q https://github.com/OpenMOSS/MOSS-TTS.git src/moss-tts
    (cd src/moss-tts && git checkout -q "$MOSS_TTS_COMMIT")
  fi
  [[ -d venv ]] || bin/uv venv -q -p 3.12 venv
  . venv/bin/activate
  bin/uv pip install -q "torch==2.9.1" "torchaudio==2.9.1" "transformers==5.0.0" "safetensors==0.6.2" \
    "numpy==2.1.0" einops scipy soundfile tqdm mlx huggingface_hub
  python -c 'import torch, transformers, mlx.core as mx; print("torch", torch.__version__, "transformers", transformers.__version__, "mlx", mx.__version__)'
fi
fetch() { # repo revision dir [pattern...]
  . "$D/venv/bin/activate"
  python - "$@" <<'PY'
import sys
from huggingface_hub import snapshot_download
p = snapshot_download(sys.argv[1], revision=sys.argv[2], local_dir=sys.argv[3], allow_patterns=sys.argv[4:] or None)
print("downloaded", p)
PY
}
if [[ $step == tok || $step == all ]]; then
  fetch OpenMOSS-Team/MOSS-Audio-Tokenizer "$TOK_REVISION" "$D/upstream/tok"
  # The processor's code and the text tokenizer (small), for the fixtures.
  fetch OpenMOSS-Team/MOSS-TTSD-v1.0 "$TTSD_REVISION" "$D/upstream/ttsd" '*.py' '*.json' '*.jinja' '*.txt' '*.md'
fi
if [[ $step == ttsd || $step == all ]]; then
  fetch OpenMOSS-Team/MOSS-TTSD-v1.0 "$TTSD_REVISION" "$D/upstream/ttsd"
fi
du -sh "$D"/* 2>/dev/null | sort -h
df -h ~ | tail -1
