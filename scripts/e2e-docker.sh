#!/usr/bin/env bash
# Umbra M3 Docker E2E. Run on the Mac (never CI): boots the docker VM, installs
# dockerd, and drives the host `docker` CLI against the umbra context. Slow
# (~5-8 min cold — dockerd install pulls packages). Needs the host docker CLI.
set -euo pipefail
cd "$(dirname "$0")/.."

command -v docker >/dev/null 2>&1 || { echo "SKIP: host docker CLI not installed (brew install docker)"; exit 0; }

CREATED_ROOT=0
if [ -z "${UMBRA_ROOT:-}" ]; then
  UMBRA_ROOT="$(mktemp -d /tmp/umbra-dk.XXXXXX)"
  CREATED_ROOT=1
fi
export UMBRA_ROOT
echo "UMBRA_ROOT=$UMBRA_ROOT"
make build

./bin/umbrad &
DAEMON_PID=$!
cleanup() {
  ./bin/umbra docker uninstall >/dev/null 2>&1 || true
  kill "$DAEMON_PID" 2>/dev/null || true
  wait "$DAEMON_PID" 2>/dev/null || true
  [ "$CREATED_ROOT" = 1 ] && rm -rf "$UMBRA_ROOT"
}
trap cleanup EXIT

./bin/umbra docker install
./bin/umbra docker start   # blocks until dockerd is ready (bounded), wires context umbra

# `docker` now targets the umbra context — run a container end to end
OUT=$(docker run --rm hello-world)
echo "$OUT" | grep -q "Hello from Docker" || { echo "FAIL: hello-world banner missing"; exit 1; }

# compose plugin resolves the same context
docker compose version >/dev/null || { echo "FAIL: docker compose unavailable"; exit 1; }

# context is registered + current
docker context inspect umbra >/dev/null || { echo "FAIL: umbra context not registered"; exit 1; }

./bin/umbra docker stop
./bin/umbra docker uninstall
docker context inspect umbra >/dev/null 2>&1 && { echo "FAIL: umbra context not removed on uninstall"; exit 1; } || true

echo "E2E DOCKER: PASS"
