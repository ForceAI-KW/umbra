// Package launchagent renders and manages the com.forceai.umbrad LaunchAgent
// that auto-starts umbrad at login. See docs/research/launchd-and-ci-cutover.md §1/§3.
package launchagent

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Label is the LaunchAgent's reverse-DNS identifier, matching the
// com.forceai.* convention used by the other ForceAI launchd jobs.
const Label = "com.forceai.umbrad"

// execCommand is overridden in tests so Install/Uninstall can be exercised
// without invoking real launchctl.
var execCommand = exec.Command

// PlistPath returns the LaunchAgent's install location under the user's
// home directory: ~/Library/LaunchAgents/com.forceai.umbrad.plist.
func PlistPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, "Library", "LaunchAgents", Label+".plist")
}

const plistTemplate = `<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
    <key>Label</key>
    <string>%[1]s</string>

    <key>ProgramArguments</key>
    <array>
        <string>%[2]s</string>
    </array>

    <key>RunAtLoad</key>
    <true/>

    <key>KeepAlive</key>
    <dict>
        <key>SuccessfulExit</key>
        <false/>
    </dict>

    <key>ThrottleInterval</key>
    <integer>5</integer>

    <key>StandardOutPath</key>
    <string>%[3]s/umbrad.out.log</string>
    <key>StandardErrorPath</key>
    <string>%[3]s/umbrad.err.log</string>

    <key>EnvironmentVariables</key>
    <dict>
        <key>PATH</key>
        <string>/opt/homebrew/bin:/usr/local/bin:/usr/bin:/bin:/usr/sbin:/sbin</string>
    </dict>

    <key>ProcessType</key>
    <string>Interactive</string>
</dict>
</plist>
`

// RenderPlist renders the com.forceai.umbrad LaunchAgent plist, pointing
// ProgramArguments at binPath (absolute, no shell wrapper — see research §1
// on codesign/entitlement survival) and logs at logDir.
func RenderPlist(binPath, logDir string) []byte {
	return []byte(fmt.Sprintf(plistTemplate, Label, binPath, logDir))
}

// guiTarget returns the launchctl gui/<uid> domain target for the current user.
func guiTarget() string {
	return fmt.Sprintf("gui/%d", os.Getuid())
}

// serviceTarget returns the launchctl gui/<uid>/<label> service target.
func serviceTarget() string {
	return guiTarget() + "/" + Label
}

// Install writes the plist to PlistPath and (re)loads it via launchctl:
// bootout any previously-loaded copy (ignoring "not found" — idempotency,
// research §3), bootstrap the fresh plist, enable it, then kickstart -k to
// start it immediately without waiting for the next login.
func Install(binPath, logDir string) error {
	plistPath := PlistPath()
	if err := os.MkdirAll(filepath.Dir(plistPath), 0o755); err != nil {
		return fmt.Errorf("create LaunchAgents dir: %w", err)
	}
	if err := os.WriteFile(plistPath, RenderPlist(binPath, logDir), 0o644); err != nil {
		return fmt.Errorf("write plist %s: %w", plistPath, err)
	}

	_ = execCommand("launchctl", "bootout", serviceTarget()).Run() // best-effort, may not be loaded

	if out, err := execCommand("launchctl", "bootstrap", guiTarget(), plistPath).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl bootstrap: %w: %s", err, out)
	}
	if out, err := execCommand("launchctl", "enable", serviceTarget()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl enable: %w: %s", err, out)
	}
	if out, err := execCommand("launchctl", "kickstart", "-k", serviceTarget()).CombinedOutput(); err != nil {
		return fmt.Errorf("launchctl kickstart: %w: %s", err, out)
	}
	return nil
}

// Uninstall stops and unregisters the LaunchAgent (swallowing "not found" so
// it's safe to run even when install never completed or ran twice, mirroring
// dockerctx.Remove's P15 pattern) and removes the plist file.
func Uninstall() error {
	out, err := execCommand("launchctl", "bootout", serviceTarget()).CombinedOutput()
	if err != nil && !isNotLoadedError(out) {
		return fmt.Errorf("launchctl bootout: %w: %s", err, out)
	}

	if err := os.Remove(PlistPath()); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove plist %s: %w", PlistPath(), err)
	}
	return nil
}

// isNotLoadedError reports whether launchctl bootout's output indicates the
// service simply wasn't loaded (not an error worth propagating) — mirrors
// dockerctx.Remove's "not found" swallow (P15).
func isNotLoadedError(out []byte) bool {
	s := strings.ToLower(string(out))
	return strings.Contains(s, "not found") || strings.Contains(s, "could not find")
}

// Installed reports whether the LaunchAgent plist file exists.
func Installed() bool {
	_, err := os.Stat(PlistPath())
	return err == nil
}
