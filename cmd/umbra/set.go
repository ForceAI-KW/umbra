package main

import (
	"fmt"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
)

var (
	setCPUs      uint
	setMemGiB    uint64
	setDiskGiB   uint64
	setAutostart string // "", "true", "false" — tri-state
)

var setCmd = &cobra.Command{
	Use:   "set <name>",
	Short: "Change a machine's cpus/memory/disk/autostart (resize requires it stopped; disk only grows)",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		var req client.UpdateRequest
		if cmd.Flags().Changed("cpus") {
			req.CPUs = &setCPUs
		}
		if cmd.Flags().Changed("memory-gib") {
			mib := setMemGiB * 1024
			req.MemoryMiB = &mib
		}
		if cmd.Flags().Changed("disk-gib") {
			req.DiskGiB = &setDiskGiB
		}
		if cmd.Flags().Changed("autostart") {
			if setAutostart != "true" && setAutostart != "false" {
				return fmt.Errorf("--autostart must be true or false")
			}
			v := setAutostart == "true"
			req.Autostart = &v
		}
		// Read the pre-update disk size so the growpart/resize2fs hint only
		// fires when --disk-gib actually grew the disk, not whenever the
		// flag was merely passed (e.g. re-running `set --disk-gib N` with
		// the machine already at N would otherwise print a no-op hint).
		var beforeDiskGiB uint64
		if req.DiskGiB != nil {
			before, err := apiClient.GetMachine(cmd.Context(), args[0])
			if err != nil {
				return err
			}
			beforeDiskGiB = before.DiskGiB
		}
		mv, err := apiClient.UpdateMachine(cmd.Context(), args[0], req)
		if err != nil {
			return err
		}
		fmt.Printf("%s: cpus=%d mem=%dGiB disk=%dGiB autostart=%v\n",
			mv.Name, mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB, mv.Autostart)
		if req.DiskGiB != nil && mv.DiskGiB > beforeDiskGiB {
			fmt.Println("disk grown on the host — inside the guest run: sudo growpart /dev/vda 1 && sudo resize2fs /dev/vda1")
		}
		return nil
	},
}

func init() {
	setCmd.Flags().UintVar(&setCPUs, "cpus", 0, "vCPU count")
	setCmd.Flags().Uint64Var(&setMemGiB, "memory-gib", 0, "memory in GiB")
	setCmd.Flags().Uint64Var(&setDiskGiB, "disk-gib", 0, "disk size in GiB (grow only)")
	setCmd.Flags().StringVar(&setAutostart, "autostart", "", "true|false — start with the daemon")
}
