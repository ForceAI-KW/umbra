package main

import (
	"fmt"
	"time"

	"github.com/spf13/cobra"
)

var snapshotCmd = &cobra.Command{
	Use:   "snapshot <machine> <snapshot-name>",
	Short: "Take an instant point-in-time snapshot of a stopped machine (APFS clone)",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.TakeSnapshot(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("snapshot %q taken for %s\n", args[1], args[0])
		return nil
	},
}

var snapshotsCmd = &cobra.Command{
	Use:   "snapshots <machine>",
	Short: "List a machine's snapshots",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		infos, err := apiClient.ListSnapshots(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		if len(infos) == 0 {
			fmt.Println("no snapshots")
			return nil
		}
		for _, i := range infos {
			fmt.Printf("%-24s %s  %.1f GiB\n", i.Name, i.CreatedAt.Format(time.DateTime), float64(i.SizeBytes)/(1<<30))
		}
		return nil
	},
}

var restoreCmd = &cobra.Command{
	Use:   "restore <machine> <snapshot-name>",
	Short: "Restore a stopped machine's disk from a snapshot",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.RestoreSnapshot(cmd.Context(), args[0], args[1]); err != nil {
			return err
		}
		fmt.Printf("%s restored from %q\n", args[0], args[1])
		return nil
	},
}
