#!/usr/bin/env bash
# The MOSS-TTSD port's conversion and parity runs on the Mac (thread #82;
# developer tool).
#
#   scripts/mossttsd/parity.sh STEP…
#
# Expects setup-dev.sh's folder ($MOSS_DEV, default <repo>/../moss-dev), the
# references in $MOSS_REFS (default <repo>/../refs: design-child.wav,
# design-mom.wav) and, for the checks, the staged engine
# (scripts/build-engine.sh). Heavy steps wait for memory and take the
# shared GPU lock (lockrun.sh: ≥ 60 % free for MOSS-TTSD, 50 % for the
# rest). Steps, in order (each upstream folder can go after its fixtures):
#   tok        convert the codec (needs upstream/tok), then its fixtures
#              (fixtures-tok.safetensors: the official processor's codes and
#              waveforms, also with our 8-bit weights dequantized);
#   cases      the sampler cases (engine/Tests/…/Fixtures/mossttsd-sampler.json)
#              and the tiny random model's greedy run ($MOSS_DEV/tiny);
#   ttsd       convert the language model (needs upstream/ttsd), then its
#              fixtures (fixtures-ttsd.safetensors: the prompt and the 17
#              heads' logits, official bf16 weights and ours dequantized);
#   codec      the engine's codec against fixtures-tok;
#   tiny       the engine's generation loop against the tiny run;
#   lm         the engine's language model (bfloat16, as the app runs it);
#   lm32       the same with float32 activations: the strict check.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/../.." && pwd)"
D="${MOSS_DEV:-$(dirname "$ROOT")/moss-dev}"
REFS="${MOSS_REFS:-$(dirname "$ROOT")/refs}"
ENGINE="$ROOT/resources/darwin-arm64/engine/audiosmith-engine"
LOCK="$ROOT/scripts/mossttsd/lockrun.sh"
. "$D/venv/bin/activate"
export HF_HOME="$D/hf" HF_HUB_OFFLINE=1
CHILD="今天放学的时候，我在路上看见一只小鸟，它飞得好高好高呀，我追了好久呢。"
MOM="今天晚上我们早点吃饭，吃完饭你先把作业写完，然后再看一会儿书，好不好？"
for step in "$@"; do
  case $step in
  tok)
    FREE_PCT=50 "$LOCK" python "$ROOT/scripts/mossttsd/convert.py" tok "$D/upstream/tok" "$D/converted"
    FREE_PCT=50 "$LOCK" python "$ROOT/scripts/mossttsd/reference.py" tok "$D" "$D/converted" "$D/fixtures-tok.safetensors" \
      --ref "$REFS/design-child.wav" "$CHILD" --ref "$REFS/design-mom.wav" "$MOM" ;;
  cases)
    python "$ROOT/scripts/mossttsd/reference.py" cases "$D" "$ROOT/engine/Tests/AudioSmithEngineTests/Fixtures/mossttsd-sampler.json" "$D/tiny" ;;
  ttsd)
    # The official logits first; then the conversion deletes each upstream
    # shard once converted (the Mac's disk), then our 8-bit logits.
    for which in official convert q8; do
      if [[ $which == convert ]]; then
        "$LOCK" python "$ROOT/scripts/mossttsd/convert.py" ttsd "$D/upstream/ttsd" "$D/converted" $([[ -n ${KEEP_UPSTREAM:-} ]] || echo --delete-source)
        continue
      fi
      "$LOCK" python "$ROOT/scripts/mossttsd/reference.py" ttsd "$D" "$D/converted" "$D/fixtures-tok.safetensors" "$D/fixtures-ttsd.safetensors" \
        --turn 1 "妈妈，我想领养一只小猫，可以吗？" --turn 2 "领养是好事，可是养猫要花很多时间照顾它。你能一直照顾它吗？" --turn 1 "我能！我每天喂它，给它换水，铲猫砂。" \
        --which $which
    done ;;
  codec) FREE_PCT=50 "$LOCK" "$ENGINE" --moss-check codec "$D/fixtures-tok.safetensors" "$D/converted" ;;
  tiny) FREE_PCT=50 "$LOCK" "$ENGINE" --moss-check tiny "$D/tiny" ;;
  lm) "$LOCK" "$ENGINE" --moss-check lm "$D/fixtures-ttsd.safetensors" "$D/fixtures-tok.safetensors" "$D/converted" ;;
  lm32) "$LOCK" "$ENGINE" --moss-check lm "$D/fixtures-ttsd.safetensors" "$D/fixtures-tok.safetensors" "$D/converted" --fp32 ;;
  *) echo "unknown step $step" >&2; exit 2 ;;
  esac
done
