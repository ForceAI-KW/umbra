// Package runner generates the bash scripts that install and harden GitHub
// Actions self-hosted runners inside an Umbra guest. It is pure string
// templating — no I/O, no ssh, no exec — so it's fully unit-testable; the
// CLI (cmd/umbra/runner.go) does the token fetch and ssh streaming around it.
package runner

import "fmt"

// DefaultVersion is the pinned actions/runner release installed when
// InstallParams.Version is empty. Matches scripts/install-runner.sh — bump
// both together after checking https://github.com/actions/runner/releases.
const DefaultVersion = "2.328.0"

// watchdogDropIn is the systemd override that survives a guest reboot or a
// crashed runner process: without it a dead runner just stays dead until
// someone notices the CI queue backing up. This is a raw (backtick) string
// so the \n sequences stay literal two-character escapes for bash's printf
// to interpret at runtime, rather than being turned into real newlines by
// the Go compiler.
const watchdogDropIn = `[Service]\nRestart=always\nRestartSec=10\n`

// InstallParams configures a single runner instance.
type InstallParams struct {
	RepoURL    string // e.g. "https://github.com/ForceAI-KW/force-website-builder"
	Token      string // repo (or org) registration token — expires in 1 hour, never logged
	RunnerName string // GitHub-side runner name, must be unique per repo
	DirName    string // directory under $HOME the runner is installed into
	Labels     string // comma-separated labels, e.g. "wsl2,umbra-ci"
	Version    string // actions/runner release; defaults to DefaultVersion when empty
}

// InstallScript returns the full bash script to run inside the guest (as
// the umbra user, with passwordless sudo) that downloads the pinned runner
// tarball if not already present, registers it against RepoURL with Token,
// installs it as a systemd service, layers on the Restart=always watchdog
// drop-in, and starts it. Idempotent via --replace, so re-running against
// an already-configured instance just reconfigures it in place.
//
// The token is inlined directly into the generated script rather than read
// from the caller's environment — the script is self-contained and must not
// depend on $REG_TOKEN (or any other env var) being set on the remote side.
func InstallScript(p InstallParams) string {
	version := p.Version
	if version == "" {
		version = DefaultVersion
	}
	return fmt.Sprintf(`set -euo pipefail

INSTANCE_DIR="$HOME/%s"
TARBALL="actions-runner-linux-arm64-%s.tar.gz"
DOWNLOAD_URL="https://github.com/actions/runner/releases/download/v%s/${TARBALL}"

mkdir -p "$INSTANCE_DIR"
cd "$INSTANCE_DIR"

if [ ! -f "./config.sh" ]; then
  curl -o actions-runner.tar.gz -L "$DOWNLOAD_URL"
  tar xzf actions-runner.tar.gz
  rm -f actions-runner.tar.gz
fi

./config.sh --url "%s" \
  --token "%s" \
  --name "%s" \
  --labels "%s" \
  --unattended --replace

sudo ./svc.sh install

SVC=$(sudo ./svc.sh status | grep -o 'actions\.runner\.[^ ]*\.service' | head -1)
sudo mkdir -p "/etc/systemd/system/${SVC}.d"
printf '%s' | sudo tee "/etc/systemd/system/${SVC}.d/override.conf" >/dev/null
sudo systemctl daemon-reload

sudo ./svc.sh start
`, p.DirName, version, version, p.RepoURL, p.Token, p.RunnerName, p.Labels, watchdogDropIn)
}

// HardenScript returns bash that idempotently applies the Restart=always
// watchdog drop-in to every actions.runner.* systemd unit already installed
// in the guest (covering runners set up before this drop-in existed, or by
// hand), daemon-reloads, and restarts any unit currently in the "failed"
// state so the fix takes effect immediately instead of waiting for the next
// crash to trigger the new restart policy.
func HardenScript() string {
	return fmt.Sprintf(`set -euo pipefail

for unit in $(systemctl list-units --all 'actions.runner.*' --no-legend | awk '{print $1}'); do
  sudo mkdir -p "/etc/systemd/system/${unit}.d"
  printf '%s' | sudo tee "/etc/systemd/system/${unit}.d/override.conf" >/dev/null
done

sudo systemctl daemon-reload

for unit in $(systemctl list-units --all 'actions.runner.*' --no-legend --state=failed | awk '{print $1}'); do
  sudo systemctl restart "$unit"
done
`, watchdogDropIn)
}
