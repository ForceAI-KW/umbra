package main

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"text/tabwriter"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/paths"
	"github.com/ForceAI-KW/umbra/internal/vm"
)

// guestStatsScript is the one ssh exec that gathers everything stats needs:
// /proc/loadavg for CPU load, free -b lines 2-3 (Mem/Swap, in bytes so no
// unit-suffix parsing), and df -B1 for guest-/ usage in bytes. Kept to a
// single round trip since stats can fan out over every running machine.
const guestStatsScript = `cat /proc/loadavg; free -b | sed -n '2p;3p'; df -B1 --output=used,size / | tail -1`

// GuestStats is the parsed result of guestStatsScript's output. Load is kept
// as the raw /proc/loadavg 1-minute string (not parsed to float) since it's
// only ever displayed, never computed on.
type GuestStats struct {
	Load      string
	MemUsed   uint64
	MemTotal  uint64
	SwapUsed  uint64
	SwapTotal uint64
	DiskUsed  uint64
	DiskTotal uint64
}

// parseGuestStats is the pure parser for guestStatsScript's combined output.
// Expected shape (order matters, guestStatsScript prints in this order):
//
//	<loadavg line>
//	Mem:    <total> <used> ...
//	Swap:   <total> <used> ...
//	<disk used> <disk size>
//
// It locates the Mem:/Swap: lines by prefix rather than assuming fixed line
// indices, so a stray banner line (motd, ssh warning) ahead of the loadavg
// line doesn't corrupt the parse — but the loadavg line must immediately
// precede "Mem:" and the disk line must be the last non-empty line, which is
// guaranteed by guestStatsScript's fixed command order.
func parseGuestStats(out string) (GuestStats, error) {
	var lines []string
	for _, l := range strings.Split(out, "\n") {
		l = strings.TrimSpace(l)
		if l != "" {
			lines = append(lines, l)
		}
	}
	if len(lines) < 4 {
		return GuestStats{}, fmt.Errorf("parseGuestStats: expected at least 4 lines of output, got %d: %q", len(lines), out)
	}

	memIdx, swapIdx := -1, -1
	for i, l := range lines {
		if strings.HasPrefix(l, "Mem:") {
			memIdx = i
		}
		if strings.HasPrefix(l, "Swap:") {
			swapIdx = i
		}
	}
	if memIdx <= 0 || swapIdx <= memIdx {
		return GuestStats{}, fmt.Errorf("parseGuestStats: could not locate Mem:/Swap: lines in output: %q", out)
	}

	loadFields := strings.Fields(lines[memIdx-1])
	if len(loadFields) == 0 {
		return GuestStats{}, fmt.Errorf("parseGuestStats: empty loadavg line: %q", lines[memIdx-1])
	}
	stats := GuestStats{Load: loadFields[0]}

	memTotal, memUsed, err := parseFreeLine(lines[memIdx])
	if err != nil {
		return GuestStats{}, fmt.Errorf("parseGuestStats: Mem line: %w", err)
	}
	stats.MemTotal, stats.MemUsed = memTotal, memUsed

	swapTotal, swapUsed, err := parseFreeLine(lines[swapIdx])
	if err != nil {
		return GuestStats{}, fmt.Errorf("parseGuestStats: Swap line: %w", err)
	}
	stats.SwapTotal, stats.SwapUsed = swapTotal, swapUsed

	diskFields := strings.Fields(lines[len(lines)-1])
	if len(diskFields) < 2 {
		return GuestStats{}, fmt.Errorf("parseGuestStats: malformed disk line: %q", lines[len(lines)-1])
	}
	diskUsed, err := strconv.ParseUint(diskFields[0], 10, 64)
	if err != nil {
		return GuestStats{}, fmt.Errorf("parseGuestStats: disk used %q: %w", diskFields[0], err)
	}
	diskTotal, err := strconv.ParseUint(diskFields[1], 10, 64)
	if err != nil {
		return GuestStats{}, fmt.Errorf("parseGuestStats: disk size %q: %w", diskFields[1], err)
	}
	stats.DiskUsed, stats.DiskTotal = diskUsed, diskTotal

	return stats, nil
}

// parseFreeLine reads the total/used columns off a "Mem:"/"Swap:" line as
// printed by `free -b` (label, total, used, free, ...).
func parseFreeLine(line string) (total, used uint64, err error) {
	fields := strings.Fields(line)
	if len(fields) < 3 {
		return 0, 0, fmt.Errorf("malformed %q", line)
	}
	total, err = strconv.ParseUint(fields[1], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("total %q: %w", fields[1], err)
	}
	used, err = strconv.ParseUint(fields[2], 10, 64)
	if err != nil {
		return 0, 0, fmt.Errorf("used %q: %w", fields[2], err)
	}
	return total, used, nil
}

var statsCmd = &cobra.Command{
	Use:   "stats [machine...]",
	Short: "Live guest cpu/mem/swap/disk table",
	RunE:  runStats,
}

func runStats(cmd *cobra.Command, args []string) error {
	targets, err := statsTargets(cmd, args)
	if err != nil {
		return err
	}
	if len(targets) == 0 {
		fmt.Println("no machines")
		return nil
	}

	w := tabwriter.NewWriter(os.Stdout, 2, 4, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tSTATE\tLOAD\tMEM\tSWAP\tDISK\tIMG")

	var failed []string
	for _, mv := range targets {
		img := "-"
		if st, err := os.Stat(filepath.Join(paths.MachineDir(mv.Name), "disk.img")); err == nil {
			img = fmt.Sprintf("%.1fGiB", gib(uint64(st.Size())))
		}

		state := string(mv.State)
		load, mem, swap, disk := "-", "-", "-", "-"
		if mv.State == vm.StateRunning && mv.SSHPort != 0 {
			out, err := sshExec(cmd, mv, guestStatsScript)
			if err != nil {
				fmt.Fprintf(os.Stderr, "%s: stats probe failed: %v\n", mv.Name, err)
				failed = append(failed, mv.Name)
			} else {
				gs, err := parseGuestStats(out)
				if err != nil {
					fmt.Fprintf(os.Stderr, "%s: stats probe failed: %v\n", mv.Name, err)
					failed = append(failed, mv.Name)
				} else {
					load = gs.Load
					mem = fmt.Sprintf("%.1f/%.1fGiB", gib(gs.MemUsed), gib(gs.MemTotal))
					swap = fmt.Sprintf("%.1f/%.1fGiB", gib(gs.SwapUsed), gib(gs.SwapTotal))
					disk = fmt.Sprintf("%.1f/%.1fGiB", gib(gs.DiskUsed), gib(gs.DiskTotal))
				}
			}
		}

		fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\t%s\t%s\n", mv.Name, state, load, mem, swap, disk, img)
	}
	if err := w.Flush(); err != nil {
		return err
	}
	if len(failed) > 0 {
		return fmt.Errorf("stats probe failed on: %s", strings.Join(failed, ", "))
	}
	return nil
}

// gib converts a byte count to GiB for display.
func gib(b uint64) float64 { return float64(b) / (1 << 30) }

// statsTargets resolves which machines to display: the named machines
// (looked up as-is, regardless of state — stats shows stopped machines
// rather than erroring on them, unlike shell/exec/runner/prune), or — with
// no args — every machine.
func statsTargets(cmd *cobra.Command, args []string) ([]*client.MachineView, error) {
	if len(args) > 0 {
		targets := make([]*client.MachineView, 0, len(args))
		for _, name := range args {
			mv, err := apiClient.GetMachine(cmd.Context(), name)
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
	targets := make([]*client.MachineView, 0, len(machines))
	for i := range machines {
		mv := machines[i]
		targets = append(targets, &mv)
	}
	return targets, nil
}

// sshExec runs script on mv's guest over ssh and returns combined
// stdout+stderr, the same connection shell/runner use (via sshArgs).
// Unlike streamScript (runner.go), this doesn't send the script over stdin
// into 'bash -s' — guestStatsScript is a fixed, argument-free read-only
// probe, so it's passed straight as the remote command.
func sshExec(cmd *cobra.Command, mv *client.MachineView, remoteCmd string) (string, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", err
	}
	args := sshArgs(mv, []string{remoteCmd})
	out, err := exec.CommandContext(cmd.Context(), sshPath, args[1:]...).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("running stats probe on %s: %w", mv.Name, err)
	}
	return string(out), nil
}
