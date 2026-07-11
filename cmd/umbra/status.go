package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Daemon + machines status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.Ping(cmd.Context()); err != nil {
			if statusJSON {
				json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "down", "error": err.Error()})
				return nil
			}
			return fmt.Errorf("daemon: DOWN (%w)", err)
		}
		machines, err := apiClient.ListMachines(cmd.Context())
		if err != nil {
			return err
		}
		if statusJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "up", "machines": machines})
		}
		fmt.Println("daemon: up")
		for _, m := range machines {
			fmt.Printf("  %s: %s %s\n", m.Name, m.State, m.IP)
		}
		return nil
	},
}

func init() { statusCmd.Flags().BoolVar(&statusJSON, "json", false, "JSON output (watchdog probe)") }
