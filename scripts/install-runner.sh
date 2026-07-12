#!/usr/bin/env bash
# Installs N GitHub Actions self-hosted runner instances inside an Umbra
# ci-runner guest (e.g. fwb-ci2). Runs INSIDE the guest as the `umbra` user
# (passwordless sudo), pushed and executed over the umbra shell channel:
#
#   REG_TOKEN=$(gh api --method POST /orgs/ForceAI-KW/actions/runners/registration-token | jq -r .token)
#   umbra shell fwb-ci2 -- REG_TOKEN="$REG_TOKEN" RUNNER_NAME=fwb-ci2 RUNNER_COUNT=2 bash -s < scripts/install-runner.sh
#
# See docs/runbooks/ci-cutover.md for the full procedure.
#
# Env vars:
#   REG_TOKEN      required. Org registration token from
#                  POST /orgs/ForceAI-KW/actions/runners/registration-token.
#                  Expires in 1 HOUR (P20, docs/research/launchd-and-ci-cutover.md
#                  §4/§8) — fetch it immediately before running this script,
#                  never bake it into a cloud-init template or reuse a stale
#                  one. If RUNNER_COUNT > 1, the SAME token is reused across
#                  all instances below for simplicity; each config.sh call
#                  consumes only a little of the token's 1-hour window, but
#                  on a slow/large RUNNER_COUNT batch that window can still
#                  be exceeded — if config.sh starts failing partway through,
#                  fetch a fresh REG_TOKEN and re-run (idempotent via
#                  --replace, so already-configured instances are just
#                  reconfigured in place).
#   RUNNER_NAME    optional, default "fwb-ci2". Instances are named
#                  "$RUNNER_NAME-1", "$RUNNER_NAME-2", ...
#   RUNNER_COUNT   optional, default 1. Number of runner instances to install.
#   RUNNER_VERSION optional, default a pinned recent actions/runner release.
#                  Check https://github.com/actions/runner/releases for the
#                  current version before bumping.
set -euo pipefail

: "${REG_TOKEN:?REG_TOKEN is required (org registration token, expires in 1 hour)}"
RUNNER_NAME="${RUNNER_NAME:-fwb-ci2}"
RUNNER_COUNT="${RUNNER_COUNT:-1}"
RUNNER_VERSION="${RUNNER_VERSION:-2.328.0}"
GH_ORG_URL="https://github.com/ForceAI-KW"

# Umbra machines are Apple-Silicon-hosted Ubuntu guests — arm64, not x64.
ARCH="arm64"
TARBALL="actions-runner-linux-${ARCH}-${RUNNER_VERSION}.tar.gz"
DOWNLOAD_URL="https://github.com/actions/runner/releases/download/v${RUNNER_VERSION}/${TARBALL}"

echo "installing ${RUNNER_COUNT} runner instance(s) as ${RUNNER_NAME}-N (version ${RUNNER_VERSION}, arch ${ARCH})"

for i in $(seq 1 "$RUNNER_COUNT"); do
  INSTANCE_DIR="$HOME/actions-runner-${i}"
  INSTANCE_NAME="${RUNNER_NAME}-${i}"
  echo "== instance ${i}: ${INSTANCE_NAME} (${INSTANCE_DIR}) =="

  mkdir -p "$INSTANCE_DIR"
  cd "$INSTANCE_DIR"

  if [ ! -f "./config.sh" ]; then
    curl -o actions-runner.tar.gz -L "$DOWNLOAD_URL"
    tar xzf actions-runner.tar.gz
    rm -f actions-runner.tar.gz
  fi

  ./config.sh --url "$GH_ORG_URL" \
    --token "$REG_TOKEN" \
    --name "$INSTANCE_NAME" \
    --labels umbra-ci \
    --unattended --replace

  sudo ./svc.sh install
  sudo ./svc.sh start

  cd - >/dev/null
done

echo "done. verify with: sudo systemctl status 'actions.runner.*' or gh api /orgs/ForceAI-KW/actions/runners"
