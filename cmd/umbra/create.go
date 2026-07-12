package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/registry"
)

var (
	flagCPUs      uint
	flagMemoryGiB uint64
	flagDiskGiB   uint64
	flagImage     string
	flagAutostart bool
)

var createCmd = &cobra.Command{
	Use:   "create <name>",
	Short: "Create a new Linux machine",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if registry.IsReserved(args[0]) {
			return fmt.Errorf("%q is reserved — use 'umbra docker install'", args[0])
		}
		fmt.Printf("creating %s (first run downloads the Ubuntu image, ~600MB)...\n", args[0])
		mv, err := apiClient.CreateMachine(cmd.Context(), client.CreateRequest{
			Name: args[0], CPUs: flagCPUs, MemoryMiB: flagMemoryGiB * 1024,
			DiskGiB: flagDiskGiB, Image: flagImage, Autostart: flagAutostart,
		})
		if err != nil {
			return err
		}
		fmt.Printf("created %s (%d cpu, %d GiB mem, %d GiB disk)\n", mv.Name, mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB)
		return nil
	},
}

func init() {
	createCmd.Flags().UintVar(&flagCPUs, "cpus", 4, "vCPUs")
	createCmd.Flags().Uint64Var(&flagMemoryGiB, "memory-gib", 8, "memory (GiB)")
	createCmd.Flags().Uint64Var(&flagDiskGiB, "disk-gib", 60, "disk size (GiB)")
	createCmd.Flags().StringVar(&flagImage, "image", "ubuntu:noble", "guest image")
	createCmd.Flags().BoolVar(&flagAutostart, "autostart", false, "start with the daemon")
}
