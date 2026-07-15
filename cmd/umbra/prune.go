package main

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// pruneScript reclaims guest disk: apt cache, docker system prune (images,
// containers, build cache — NEVER --volumes, that would delete a
// containerized service's data), journal vacuum, /tmp, and fstrim. Guests
// without docker installed just no-op that step (`|| true`). The final line
// echoes "PRUNE_FREED <bytes>" (df avail before/after, in bytes) so the CLI
// can parse it and print a human-readable freed amount.
const pruneScript = `BEFORE=$(df -B1 --output=avail / | tail -1)
sudo apt-get clean 2>/dev/null || true
docker system prune -af 2>/dev/null || true   # images/containers/build cache; NEVER volumes
sudo journalctl --vacuum-size=100M 2>/dev/null || true
sudo rm -rf /tmp/* /var/tmp/* 2>/dev/null || true
sudo fstrim -av 2>/dev/null || true
AFTER=$(df -B1 --output=avail / | tail -1)
echo "PRUNE_FREED $((AFTER - BEFORE))"
`

var pruneCmd = &cobra.Command{
	Use:   "prune [machine...]",
	Short: "Reclaim guest disk (apt cache, docker prune, journal vacuum, fstrim)",
	RunE:  runPrune,
}

func runPrune(cmd *cobra.Command, args []string) error {
	targets, err := pruneTargets(cmd, args)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Println("no running machines to prune")
		return nil
	}

	var failed []string
	for _, mv := range targets {
		out, err := streamScript(cmd.Context(), mv, pruneScript)
		if out != "" {
			fmt.Println(out)
		}
		if err != nil {
			fmt.Printf("%s: prune failed: %v\n", mv.Name, err)
			failed = append(failed, mv.Name)
			continue
		}
		freed, ok := parsePruneFreed(out)
		if !ok {
			fmt.Printf("%s: prune ran but freed amount could not be parsed\n", mv.Name)
			continue
		}
		fmt.Printf("%s: freed %.1f GiB\n", mv.Name, float64(freed)/(1<<30))
	}
	if len(failed) > 0 {
		return fmt.Errorf("prune failed on: %s", strings.Join(failed, ", "))
	}
	return nil
}

// pruneTargets resolves which machines to prune: the named machines
// (validated reachable, same rule as shell/exec/runner), or — with no
// args — every RUNNING machine.
func pruneTargets(cmd *cobra.Command, args []string) ([]*client.MachineView, error) {
	if len(args) > 0 {
		targets := make([]*client.MachineView, 0, len(args))
		for _, name := range args {
			mv, err := getReachableMachine(cmd, name)
			if err != nil {
				return nil, err
			}
			targets = append(targets, mv)
		}
		return targets, nil
	}

	machines, err := apiClient.ListMachines(cmd.Context())
	if err != nil {
		return nil, err
	}
	var targets []*client.MachineView
	for i := range machines {
		if machines[i].State == vm.StateRunning && machines[i].SSHPort != 0 {
			mv := machines[i]
			targets = append(targets, &mv)
		}
	}
	return targets, nil
}

// parsePruneFreed extracts the byte count from the guest script's
// "PRUNE_FREED <bytes>" line, scanning combined stdout+stderr since
// streamScript merges both.
func parsePruneFreed(out string) (int64, bool) {
	for _, line := range strings.Split(out, "\n") {
		rest, ok := strings.CutPrefix(strings.TrimSpace(line), "PRUNE_FREED ")
		if !ok {
			continue
		}
		n, err := strconv.ParseInt(strings.TrimSpace(rest), 10, 64)
		if err != nil {
			continue
		}
		return n, true
	}
	return 0, false
}
