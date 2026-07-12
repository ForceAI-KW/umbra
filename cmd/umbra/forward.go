package main

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"
)

var forwardCmd = &cobra.Command{
	Use:   "forward",
	Short: "Manage host<->guest port forwards",
}

var (
	forwardAddUDP bool
	forwardRmUDP  bool
)

var forwardAddCmd = &cobra.Command{
	Use:   "add <name> <localPort>:<guestPort>",
	Short: "Forward a host port to a machine's guest port",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPort, guestPort, err := parsePortPair(args[1])
		if err != nil {
			return err
		}
		proto := "tcp"
		if forwardAddUDP {
			proto = "udp"
		}
		fv, err := apiClient.AddForward(cmd.Context(), args[0], localPort, guestPort, proto)
		if err != nil {
			return err
		}
		fmt.Printf("%s -> %s (%s)\n", fv.Local, fv.Remote, fv.Protocol)
		return nil
	},
}

var forwardListCmd = &cobra.Command{
	Use:   "list <name>",
	Short: "List a machine's port forwards",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		forwards, err := apiClient.ListForwards(cmd.Context(), args[0])
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
		fmt.Fprintln(w, "LOCAL\tREMOTE\tPROTOCOL")
		for _, f := range forwards {
			fmt.Fprintf(w, "%s\t%s\t%s\n", f.Local, f.Remote, f.Protocol)
		}
		return w.Flush()
	},
}

var forwardRmCmd = &cobra.Command{
	Use:   "rm <name> <localPort>",
	Short: "Remove a port forward",
	Args:  cobra.ExactArgs(2),
	RunE: func(cmd *cobra.Command, args []string) error {
		localPort, err := strconv.Atoi(args[1])
		if err != nil {
			return fmt.Errorf("invalid local port %q: %w", args[1], err)
		}
		proto := "tcp"
		if forwardRmUDP {
			proto = "udp"
		}
		if err := apiClient.RemoveForward(cmd.Context(), args[0], localPort, proto); err != nil {
			return err
		}
		fmt.Printf("removed forward on port %d\n", localPort)
		return nil
	},
}

// parsePortPair splits "<localPort>:<guestPort>" into its two integers.
func parsePortPair(s string) (local, guest int, err error) {
	parts := strings.SplitN(s, ":", 2)
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid port mapping %q, want <localPort>:<guestPort>", s)
	}
	if local, err = strconv.Atoi(parts[0]); err != nil {
		return 0, 0, fmt.Errorf("invalid local port %q: %w", parts[0], err)
	}
	if guest, err = strconv.Atoi(parts[1]); err != nil {
		return 0, 0, fmt.Errorf("invalid guest port %q: %w", parts[1], err)
	}
	return local, guest, nil
}

func init() {
	forwardCmd.AddCommand(forwardAddCmd, forwardListCmd, forwardRmCmd)
	forwardAddCmd.Flags().BoolVar(&forwardAddUDP, "udp", false, "use UDP instead of TCP")
	forwardRmCmd.Flags().BoolVar(&forwardRmUDP, "udp", false, "use UDP instead of TCP")
}
