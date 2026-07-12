#!/usr/bin/env bash
# Umbra M1 E2E smoke. Run on the Mac (never CI): boots a real VM.
set -euo pipefail
cd "$(dirname "$0")/.."

CREATED_ROOT=0
if [ -z "${UMBRA_ROOT:-}" ]; then
  UMBRA_ROOT="$(mktemp -d /tmp/umbra-e2e.XXXXXX)"
  CREATED_ROOT=1
fi
export UMBRA_ROOT
echo "UMBRA_ROOT=$UMBRA_ROOT"
make build

./bin/umbrad &
DAEMON_PID=$!
cleanup() {
  kill "$DAEMON_PID" 2>/dev/null || true
  wait "$DAEMON_PID" 2>/dev/null || true   # let StopAll finish before any rm
  [ "$CREATED_ROOT" = 1 ] && rm -rf "$UMBRA_ROOT"
}
trap cleanup EXIT

./bin/umbra status            # exercises client retry until socket is up (P10)

./bin/umbra create e2e --cpus 2 --memory-gib 2 --disk-gib 20
./bin/umbra start e2e         # bounded readiness — fails loud with stage name (P6)

# guest is arm64 ubuntu
ARCH=$(./bin/umbra shell e2e -- uname -m)
[ "$ARCH" = "aarch64" ] || { echo "FAIL: arch=$ARCH"; exit 1; }

# virtiofs home mount visible
./bin/umbra shell e2e -- ls /mnt/mac >/dev/null || { echo "FAIL: /mnt/mac not mounted"; exit 1; }

# stop is verified, not fire-and-forget (P8/P9)
./bin/umbra stop e2e
STATE=$(./bin/umbra status --json | python3 -c 'import json,sys; print(next(m for m in json.load(sys.stdin)["machines"] if m["name"]=="e2e")["state"])')
[ "$STATE" = "stopped" ] || { echo "FAIL: state=$STATE"; exit 1; }

./bin/umbra rm e2e
echo "E2E SMOKE: PASS"
