#!/usr/bin/env bash
# The SoulX-Podcast port's parity run on the Mac (thread #80; developer tool).
#
#   scripts/soulx/parity.sh [convert|reference|check|check32|all]
#
# Expects setup-dev.sh's folder ($SOULX_DEV, default <repo>/../soulx-dev) and
# the staged engine (scripts/build-engine.sh). Heavy steps wait for memory
# and take the shared GPU lock (lockrun.sh). Steps:
#   convert    s3gen.safetensors + the 8-bit LLM into $SOULX_DEV/converted,
#              keys checked against the Swift module;
#   reference  the official code's fixtures ($SOULX_DEV/fixtures.safetensors)
#              and the sampler cases of the Swift unit tests;
#   check      the engine against them with the converted (8-bit) model;
#   check32    the same with upstream's language model cast to float32 (the
#              strict logits check; ~7 GB).
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
D="${SOULX_DEV:-$(dirname "$ROOT")/soulx-dev}"
REFS="${SOULX_REFS:-$(dirname "$ROOT")/refs}"
ENGINE="$ROOT/resources/darwin-arm64/engine/audiosmith-engine"
LOCK="$ROOT/scripts/soulx/lockrun.sh"
S3TOK="${S3TOK:-$(ls -d "$HOME"/.cache/huggingface/hub/models--mlx-community--S3TokenizerV2/snapshots/*/ | head -1)}"
. "$D/venv/bin/activate"
export HF_HOME="$D/hf" HF_HUB_OFFLINE=1
step="${1:-all}"

if [[ $step == convert || $step == all ]]; then
  "$ENGINE" --soulx-dump-params "$D/swift-params.json"
  "$LOCK" python "$ROOT/scripts/soulx/convert.py" "$D/upstream" "$D/converted" --swift-params "$D/swift-params.json"
fi
if [[ $step == reference || $step == all ]]; then
  "$LOCK" python "$ROOT/scripts/soulx/reference.py" "$D/src/soulx" "$D/upstream" "$D/fixtures.safetensors" \
    --ref "$REFS/design-child.wav" "今天放学的时候，我在路上看见一只小鸟，它飞得好高好高呀，我追了好久呢。" \
    --ref "$REFS/design-mom.wav" "今天晚上我们早点吃饭，吃完饭你先把作业写完，然后再看一会儿书，好不好？" \
    --turn 0 "妈妈，我想领养一只小猫，可以吗？" --s3tok-dir "$D/s3tok" \
    --sampler-json "$ROOT/engine/Tests/AudioSmithEngineTests/Fixtures/soulx-sampler.json"
fi
if [[ $step == check || $step == all ]]; then
  "$LOCK" "$ENGINE" --soulx-check "$D/fixtures.safetensors" "$D/converted" "$S3TOK"
fi
if [[ $step == check32 || $step == all ]]; then
  "$LOCK" "$ENGINE" --soulx-check "$D/fixtures.safetensors" "$D/converted" "$S3TOK" --llm-dir "$D/upstream" --fp32
fi
