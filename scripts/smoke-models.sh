#!/usr/bin/env bash
# The per-model smoke suite (e2e_smoke_test.go): downloads each catalogue
# model, runs it in the engine offline and checks what it makes. Every
# catalogue model passes it before it is listed. Mac only, with network.
#
#   scripts/smoke-models.sh                      # every catalogue entry
#   scripts/smoke-models.sh kokoro-82m-bf16,omnivoice-bf16
#
# AUDIOSMITH_SMOKE_CACHE (model folder, default the HF cache),
# AUDIOSMITH_SMOKE_SANDBOX=1 (the engine under sandbox-exec, no network),
# AUDIOSMITH_SMOKE_KEEP=1 (keep what it downloaded), AUDIOSMITH_SMOKE_REPORT
# (default /tmp/audiosmith-smoke/report.md) tune it. Set SKIP_BUILD=1 to
# use the staged engine as it is.
set -euo pipefail
ROOT="$(cd "$(dirname "$0")/.." && pwd)"
cd "$ROOT"
[[ -n "${SKIP_BUILD:-}" ]] || scripts/build-engine.sh
AUDIOSMITH_E2E=1 AUDIOSMITH_SMOKE="${1:-all}" go test -run 'TestModelSmoke' -v -count=1 -timeout "${TIMEOUT:-4h}" . 2>&1 |
  grep --line-buffered -vE '^=== (RUN|PAUSE|CONT)' | tee /tmp/audiosmith-smoke.log
