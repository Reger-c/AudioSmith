#!/usr/bin/env bash
# lockrun.sh CMD...: runs CMD (a heavy MOSS-TTSD step on the shared Mac) once
# at least FREE_PCT % of memory is free (default 60: MOSS-TTSD needs about
# 12 GB), under the shared GPU lock /tmp/as-gpu.lock. Waits up to WAIT_MIN
# minutes (default 60) for memory, without holding the lock meanwhile, and
# re-checks after taking it. Refuses to start with less than 6 GB of free
# disk.
set -uo pipefail
WAIT_MIN="${WAIT_MIN:-60}"
FREE_PCT="${FREE_PCT:-60}"
free_pct() { memory_pressure | awk '/free percentage/ {gsub("%", "", $NF); print $NF}'; }
wait_free() {
  local i
  for ((i = 0; i < WAIT_MIN * 2; i++)); do
    [[ "$(free_pct)" -ge $FREE_PCT ]] && return 0
    sleep 30
  done
  echo "lockrun: only $(free_pct) % of memory free after ${WAIT_MIN} min; not starting" >&2
  return 3
}
disk_gb=$(df -g "$HOME" | awk 'NR==2 {print $4}')
if [[ "${disk_gb:-0}" -lt 6 ]]; then
  echo "lockrun: only ${disk_gb} GB of disk free; not starting" >&2
  exit 4
fi
while true; do
  wait_free || exit 3
  until mkdir /tmp/as-gpu.lock 2>/dev/null; do sleep 2; done
  echo "c82 mossttsd $$ $(date +%T)" > /tmp/as-gpu.lock/owner
  if [[ "$(free_pct)" -ge $FREE_PCT ]]; then break; fi
  rm -rf /tmp/as-gpu.lock
  sleep 30
done
trap 'rm -rf /tmp/as-gpu.lock' EXIT
echo "lockrun: $(free_pct) % free, running: $*" >&2
"$@"
