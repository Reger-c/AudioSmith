#!/usr/bin/env bash
# lockrun.sh CMD...: runs CMD (a heavy SoulX step on the shared Mac) once at
# least 50 % of memory is free, under the shared GPU lock /tmp/as-gpu.lock.
# Waits up to WAIT_MIN minutes (default 60) for memory, without holding the
# lock meanwhile, and re-checks after taking it.
set -uo pipefail
WAIT_MIN="${WAIT_MIN:-60}"
free_pct() { memory_pressure | awk '/free percentage/ {gsub("%", "", $NF); print $NF}'; }
wait_free() {
  local i
  for ((i = 0; i < WAIT_MIN * 2; i++)); do
    [[ "$(free_pct)" -ge 50 ]] && return 0
    sleep 30
  done
  echo "lockrun: only $(free_pct) % of memory free after ${WAIT_MIN} min; not starting" >&2
  return 3
}
while true; do
  wait_free || exit 3
  until mkdir /tmp/as-gpu.lock 2>/dev/null; do sleep 2; done
  echo "c80 soulx $$ $(date +%T)" > /tmp/as-gpu.lock/owner
  if [[ "$(free_pct)" -ge 50 ]]; then break; fi
  rm -rf /tmp/as-gpu.lock
  sleep 30
done
trap 'rm -rf /tmp/as-gpu.lock' EXIT
echo "lockrun: $(free_pct) % free, running: $*" >&2
"$@"
