package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var shellCmd = &cobra.Command{
	Use:   "shell <name> [-- command...]",
	Short: "Open a shell (or run a command) in a machine",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runShell,
}

// sshArgs builds the ssh argv (including argv[0] "ssh") used to reach a
// running machine's guest — the daemon forwards a loopback port to guest:22
// while the machine runs, since the guest lives on the userspace netstack
// the host can't route to directly. remoteCmd, if non-empty, is appended so
// ssh runs it instead of opening an interactive shell. Shared by shell/exec
// (interactive) and runner (streams a script over stdin into 'bash -s').
func sshArgs(mv *client.MachineView, remoteCmd []string) []string {
	args := []string{"ssh",
		"-i", filepath.Join(paths.SSH(), "id_ed25519"),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(paths.SSH(), "known_hosts"),
		"-p", strconv.Itoa(mv.SSHPort),
		"umbra@127.0.0.1",
	}
	if len(remoteCmd) > 0 {
		args = append(args, remoteCmd...)
	}
	return args
}

// sshProbeArgs is sshArgs hardened for NON-INTERACTIVE probes (umbra doctor).
//
// Two options are added here and deliberately NOT in sshArgs itself:
//
//   - BatchMode=yes — never prompt. sshArgs is shared with the interactive
//     shell/exec commands, where a passphrase or host-key prompt is a feature;
//     setting BatchMode there would break `umbra shell` for anyone whose key
//     is not already in the agent. A prompt inside doctor, by contrast, blocks
//     forever with nothing on the other end to answer it.
//   - ConnectTimeout + ServerAlive* — bound the wait. doctor's whole purpose
//     is diagnosing a guest that is up with a live forwarded port but a dead
//     netstack, which is exactly the case where a TCP connect hangs rather
//     than refusing. A diagnostic that hangs on the fault it diagnoses is
//     worse than one that honestly reports Unknown.
//
// runner/stats keep the plain sshArgs: they act on a machine the user has
// already asserted is healthy, and a longer wait there is not a misdiagnosis.
func sshProbeArgs(mv *client.MachineView, remoteCmd []string) []string {
	base := sshArgs(mv, nil)
	out := []string{base[0],
		"-o", "BatchMode=yes",
		"-o", "ConnectTimeout=" + strconv.Itoa(sshProbeConnectSeconds),
		"-o", "ServerAliveInterval=5",
		"-o", "ServerAliveCountMax=2",
	}
	out = append(out, base[1:]...)
	return append(out, remoteCmd...)
}

// sshProbeConnectSeconds bounds the TCP connect for doctor's probes. Short
// enough that a wedged guest is reported rather than waited on; long enough
// that a merely busy guest is not misreported as unreachable.
const sshProbeConnectSeconds = 5

func runShell(cmd *cobra.Command, args []string) error {
	mv, err := apiClient.GetMachine(cmd.Context(), args[0])
	if err != nil {
		return err
	}
	// The guest lives on the userspace netstack, which the host can't
	// route to directly — the daemon forwards a loopback port to guest:22
	// while the machine runs.
	if mv.SSHPort == 0 {
		return fmt.Errorf("machine %q is not reachable (state: %s) — start it first", mv.Name, mv.State)
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	return syscall.Exec(sshPath, sshArgs(mv, args[1:]), os.Environ())
}

// execCmd is sugar for `umbra shell <name> -- <command...>` — every
// automation script guesses `umbra exec` exists (docker/kubectl muscle
// memory), so make it exist.
var execCmd = &cobra.Command{
	Use:   "exec <name> <command...>",
	Short: "Run a command in a machine (alias for shell <name> -- ...)",
	Args:  cobra.MinimumNArgs(2),
	RunE:  runShell,
}

func init() {
	execCmd.Flags().SetInterspersed(false)
}
