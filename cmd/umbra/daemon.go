package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/launchagent"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var daemonBin string

var daemonCmd = &cobra.Command{
	Use:   "daemon",
	Short: "Manage the umbrad LaunchAgent (auto-start at login)",
}

var daemonInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Write + load the umbrad LaunchAgent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		binPath, err := resolveUmbradBin()
		if err != nil {
			return err
		}
		if _, err := os.Stat(binPath); err != nil {
			return fmt.Errorf("umbrad binary not found at %s — run `make build`", binPath)
		}
		if err := launchagent.Install(binPath, paths.Logs()); err != nil {
			return err
		}
		fmt.Printf("installed LaunchAgent %s (%s)\n", launchagent.Label, launchagent.PlistPath())
		fmt.Println("umbrad will now start at login.")
		fmt.Println("Note: run `make run-daemon` once interactively first so the macOS VirtioFS home-share permission prompt (TCC) can be granted with a UI present (P24).")
		return nil
	},
}

var daemonUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop and unload the umbrad LaunchAgent",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := launchagent.Uninstall(); err != nil {
			return err
		}
		fmt.Println("uninstalled LaunchAgent", launchagent.Label)
		return nil
	},
}

var daemonStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "LaunchAgent + daemon reachability status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if launchagent.Installed() {
			fmt.Println("launchagent: installed", "("+launchagent.PlistPath()+")")
		} else {
			fmt.Println("launchagent: not installed")
		}

		if err := apiClient.Ping(cmd.Context()); err != nil {
			fmt.Println("api:         not reachable")
		} else {
			fmt.Println("api:         reachable")
		}

		fmt.Println("note: after `make build` rebuilds umbrad, re-run `umbra daemon install` to pick up the new signed binary (P23).")
		return nil
	},
}

// resolveUmbradBin locates the umbrad binary to install: --bin flag, then
// $UMBRA_BIN, then bin/umbrad next to the running umbra executable.
func resolveUmbradBin() (string, error) {
	if daemonBin != "" {
		return daemonBin, nil
	}
	if v := os.Getenv("UMBRA_BIN"); v != "" {
		return v, nil
	}
	exe, err := os.Executable()
	if err != nil {
		return "", fmt.Errorf("locate running umbra executable: %w", err)
	}
	return filepath.Join(filepath.Dir(exe), "umbrad"), nil
}

func init() {
	daemonInstallCmd.Flags().StringVar(&daemonBin, "bin", "", "path to the umbrad binary (default: bin/umbrad next to the running umbra binary, or $UMBRA_BIN)")
	daemonCmd.AddCommand(daemonInstallCmd, daemonUninstallCmd, daemonStatusCmd)
}
