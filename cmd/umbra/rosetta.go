package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var rosettaCmd = &cobra.Command{
	Use:   "rosetta",
	Short: "Rosetta (amd64-on-arm64) status",
}

var rosettaStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Report host Rosetta-for-Linux availability",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		avail, err := apiClient.Rosetta(cmd.Context())
		if err != nil {
			return err
		}
		switch avail {
		case "installed":
			fmt.Println("Rosetta: installed")
		case "notInstalled":
			fmt.Println("Rosetta: not installed")
			fmt.Println("hint: run a docker or ci-runner machine to auto-install, or `softwareupdate --install-rosetta`")
		case "notSupported":
			fmt.Println("Rosetta: not supported")
		default:
			fmt.Printf("Rosetta: %s\n", avail)
		}
		return nil
	},
}

func init() { rosettaCmd.AddCommand(rosettaStatusCmd) }
