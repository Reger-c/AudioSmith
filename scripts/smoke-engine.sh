#!/usr/bin/env bash
# Runs the staged engine once per case and checks it wrote audio.
# Models download on first use. Usage: scripts/smoke-engine.sh [outdir]
set -uo pipefail

ROOT="$(cd "$(dirname "$0")/.." && pwd)"
ENGINE="${AUDIOSMITH_ENGINE:-$ROOT/resources/darwin-arm64/engine/audiosmith-engine}"
OUT="${1:-/tmp/audiosmith-smoke}"
rm -rf "$OUT"
mkdir -p "$OUT"

fail=0
run() { # name, then engine arguments
  local name="$1"
  shift
  local started=$SECONDS
  "$ENGINE" "$@" --output "$OUT/$name.wav" > "$OUT/$name.json" 2> "$OUT/$name.err"
  local code=$?
  if [[ $code -eq 0 && -s "$OUT/$name.wav" ]]; then
    printf 'ok    %-10s %3ss  %s\n' "$name" $((SECONDS - started)) "$(grep '"result"' "$OUT/$name.json")"
  else
    fail=1
    printf 'FAIL  %-10s exit %s\n' "$name" "$code"
    grep -v 'files$' "$OUT/$name.err" | tail -5 | sed 's/^/      /'
  fi
}

"$ENGINE" --version || exit 1
run kokoro-en --text "Hello from AudioSmith. This voice was generated on your Mac with MLX." --voice af_heart
run kokoro-zh --text "你好，这是在 Mac 上用 MLX 生成的语音。" --voice zf_xiaobei
run kokoro-fast --text "The same voice, a little faster." --voice am_adam --speed 1.3
run kokoro-24 --text "Twenty-four bit, streamed." --voice af_heart --bits 24 --stream
exit $fail
