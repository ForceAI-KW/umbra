package main

import (
	"fmt"
	"os"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var listCmd = &cobra.Command{
	Use:   "list",
	Short: "List machines",
	RunE: func(cmd *cobra.Command, args []string) error {
		machines, err := apiClient.ListMachines(cmd.Context())
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "NAME\tSTATE\tIP\tCPUS\tMEM(GiB)\tDISK(GiB)\tAUTOSTART")
		hasZombie := false
		for _, m := range machines {
			state := string(m.State)
			if m.Zombie {
				state = "crashed*"
				hasZombie = true
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%d\t%d\t%d\t%v\n",
				m.Name, state, m.IP, m.CPUs, m.MemoryMiB/1024, m.DiskGiB, m.Autostart)
		}
		if err := w.Flush(); err != nil {
			return err
		}
		if hasZombie {
			fmt.Println("* unconfirmed stop — VM may still be alive; run 'umbra stop <name>' again")
		}
		return nil
	},
}

var startCmd = &cobra.Command{
	Use: "start <name>", Short: "Start a machine", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		info, err := apiClient.StartMachine(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		fmt.Printf("%s running at %s\n", info.Name, info.IP)
		return nil
	},
}

var stopCmd = &cobra.Command{
	Use: "stop <name>", Short: "Stop a machine", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.StopMachine(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Printf("%s stopped\n", args[0])
		return nil
	},
}

var rmCmd = &cobra.Command{
	Use: "rm <name>", Short: "Delete a machine (must be stopped)", Args: cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		if err := apiClient.DeleteMachine(cmd.Context(), args[0]); err != nil {
			return err
		}
		fmt.Printf("%s deleted\n", args[0])
		return nil
	},
}
