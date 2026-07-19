package main

import (
	"errors"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var apiClient *client.Client

var rootCmd = &cobra.Command{
	Use:   "umbra",
	Short: "Umbra — Linux machines and Docker on Apple Silicon, invisibly",
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		apiClient = client.New(paths.APISocket())
	},
	SilenceUsage:  true,
	SilenceErrors: true, // execute() prints the single error line
}

func execute() int {
	rootCmd.AddCommand(createCmd, listCmd, startCmd, stopCmd, rmCmd, shellCmd, execCmd, statusCmd, forwardCmd, dockerCmd, daemonCmd, rosettaCmd, setCmd, snapshotCmd, snapshotsCmd, restoreCmd, exportCmd, importCmd, runnerCmd, pruneCmd, statsCmd, doctorCmd)
	if err := rootCmd.Execute(); err != nil {
		// doctor reports faults through the exit code; the findings are
		// already on stdout, so an "error:" line would be noise.
		if errors.Is(err, errFaultsFound) {
			return 1
		}
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
