package main

import (
	"fmt"

	"github.com/spf13/cobra"
)

var dockerCmd = &cobra.Command{
	Use:   "docker",
	Short: "Manage the Umbra docker VM",
}

var dockerInstallCmd = &cobra.Command{
	Use:   "install",
	Short: "Create the reserved docker VM",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		fmt.Println("installing docker VM (first run downloads the Ubuntu image, ~600MB)...")
		mv, err := apiClient.DockerInstall(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Printf("installed docker VM (%d cpu, %d GiB mem, %d GiB disk) — run 'umbra docker start' next\n", mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB)
		return nil
	},
}

var dockerStartCmd = &cobra.Command{
	Use:   "start",
	Short: "Start the docker VM and register the 'umbra' docker context",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		mv, err := apiClient.DockerStart(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Printf("docker running at %s (docker context 'umbra' active)\n", mv.IP)
		return nil
	},
}

var dockerStopCmd = &cobra.Command{
	Use:   "stop",
	Short: "Stop the docker VM",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.DockerStop(cmd.Context()); err != nil {
			return err
		}
		fmt.Println("docker stopped")
		return nil
	},
}

var dockerStatusCmd = &cobra.Command{
	Use:   "status",
	Short: "Docker VM status",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		st, err := apiClient.DockerStatus(cmd.Context())
		if err != nil {
			return err
		}
		fmt.Printf("installed:       %v\n", st.Installed)
		fmt.Printf("running:         %v\n", st.Running)
		fmt.Printf("ip:              %s\n", st.IP)
		fmt.Printf("socket:          %s\n", st.Socket)
		fmt.Printf("context current: %v\n", st.ContextCurrent)
		return nil
	},
}

var dockerUninstallCmd = &cobra.Command{
	Use:   "uninstall",
	Short: "Stop the docker VM, deregister its context, and delete it",
	Args:  cobra.NoArgs,
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.DockerUninstall(cmd.Context()); err != nil {
			return err
		}
		fmt.Println("docker uninstalled")
		return nil
	},
}

func init() {
	dockerCmd.AddCommand(dockerInstallCmd, dockerStartCmd, dockerStopCmd, dockerStatusCmd, dockerUninstallCmd)
}
