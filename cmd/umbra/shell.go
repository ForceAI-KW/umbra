package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"syscall"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/paths"
)

var shellCmd = &cobra.Command{
	Use:   "shell <name> [-- command...]",
	Short: "Open a shell (or run a command) in a machine",
	Args:  cobra.MinimumNArgs(1),
	RunE:  runShell,
}

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
	sshArgs := []string{"ssh",
		"-i", filepath.Join(paths.SSH(), "id_ed25519"),
		"-o", "StrictHostKeyChecking=accept-new",
		"-o", "UserKnownHostsFile=" + filepath.Join(paths.SSH(), "known_hosts"),
		"-p", strconv.Itoa(mv.SSHPort),
		"umbra@127.0.0.1",
	}
	if len(args) > 1 {
		sshArgs = append(sshArgs, args[1:]...)
	}
	return syscall.Exec(sshPath, sshArgs, os.Environ())
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
