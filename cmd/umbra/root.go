package main

import (
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
	rootCmd.AddCommand(createCmd, listCmd, startCmd, stopCmd, rmCmd, shellCmd, execCmd, statusCmd, forwardCmd, dockerCmd, daemonCmd, rosettaCmd, setCmd)
	if err := rootCmd.Execute(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		return 1
	}
	return 0
}
