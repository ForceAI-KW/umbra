package main

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
)

var statusJSON bool

var statusCmd = &cobra.Command{
	Use:   "status",
	Short: "Daemon + machines status",
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.Ping(cmd.Context()); err != nil {
			if statusJSON {
				return json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "down", "error": err.Error()})
			}
			return fmt.Errorf("daemon: DOWN (%w)", err)
		}
		machines, err := apiClient.ListMachines(cmd.Context())
		if err != nil {
			return err
		}
		// Docker status is best-effort: a machine-less/docker-less host is a
		// valid state, not an error worth failing `status` over.
		docker, dockerErr := apiClient.DockerStatus(cmd.Context())
		if dockerErr != nil {
			docker = &client.DockerStatus{}
		}
		if statusJSON {
			return json.NewEncoder(os.Stdout).Encode(map[string]any{"daemon": "up", "machines": machines, "docker": docker})
		}
		fmt.Println("daemon: up")
		for _, m := range machines {
			fmt.Printf("  %s: %s %s\n", m.Name, m.State, m.IP)
		}
		fmt.Printf("docker: %s\n", dockerStateString(docker))
		return nil
	},
}

func init() { statusCmd.Flags().BoolVar(&statusJSON, "json", false, "JSON output (watchdog probe)") }

func dockerStateString(d *client.DockerStatus) string {
	switch {
	case !d.Installed:
		return "not installed"
	case !d.Running:
		return "stopped"
	case d.IP != "":
		return fmt.Sprintf("running (%s)", d.IP)
	default:
		return "running"
	}
}
