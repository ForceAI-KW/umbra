package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/doctor"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var (
	doctorJSON bool
	doctorDeep bool
)

// canaryScript is the bounded native-binary load canary. curl and openssl are
// correct-arch system binaries with zero Rosetta ambiguity, so a CPU-level
// signal from either means the guest is miscomputing — a host fault, not a
// config problem. Bounded on purpose: never leave stress running on a suspect host.
const canaryScript = `set +e
for i in $(seq 1 150); do
  curl --version >/dev/null 2>&1; RC=$?
  [ $RC -ne 0 ] && echo "FAULT rc=$RC"
done
for j in 1 2 3 4; do
  ( for i in $(seq 1 800); do openssl sha256 /usr/bin/curl >/dev/null 2>&1; RC=$?
      [ $RC -ne 0 ] && echo "FAULT rc=$RC"
    done ) &
done
wait
echo CANARY_DONE
`

// canaryFaulted reports whether the canary saw a CPU-level signal. Exit codes
// 132 (SIGILL) and 139 (SIGSEGV) are the decisive host-hardware signature.
func canaryFaulted(out string) bool {
	return strings.Contains(out, "FAULT rc=132") || strings.Contains(out, "FAULT rc=139")
}

var doctorCmd = &cobra.Command{
	Use:   "doctor",
	Short: "Diagnose umbra/CI faults and print the next action",
	Long: "Classifies host, guest and CI faults into one rung of the umbra triage ladder.\n" +
		"Read-only by default. --deep additionally runs a bounded native-binary load\n" +
		"canary, which is the only way to detect a host-hardware fault.",
	RunE: func(cmd *cobra.Command, args []string) error {
		ev := doctor.Evidence{DeepRun: doctorDeep}

		if err := apiClient.Ping(cmd.Context()); err == nil {
			ev.DaemonUp = true
		}

		if f, err := os.Open(paths.Logs() + "/umbrad.err.log"); err == nil {
			defer f.Close()
			lines, start, err := doctor.ScanLog(f)
			if err == nil {
				ev.LogLines, ev.DaemonStart = lines, start
			}
		}

		if ev.DaemonUp {
			machines, err := apiClient.ListMachines(cmd.Context())
			if err != nil {
				return err
			}
			for i := range machines {
				ev.Guests = append(ev.Guests, probeGuest(cmd, &machines[i]))
			}
		}

		_, ghErr := exec.LookPath("gh")
		ev.GHAvailable = ghErr == nil

		verdicts := doctor.Classify(ev)

		if doctorJSON {
			if err := json.NewEncoder(os.Stdout).Encode(map[string]any{
				"deep":     doctorDeep,
				"verdicts": verdicts,
			}); err != nil {
				return err
			}
		} else {
			printVerdicts(verdicts)
		}

		for _, v := range verdicts {
			if v.Health == doctor.Fail {
				return errFaultsFound
			}
		}
		return nil
	},
}

// errFaultsFound signals "diagnosis succeeded and found faults" — distinct
// from "the command itself failed". main maps it to exit 1 without printing
// a spurious error, so deferred cleanup still runs and cobra stays in charge
// of the error path.
var errFaultsFound = errors.New("faults found")

func printVerdicts(vs []doctor.Verdict) {
	if len(vs) == 0 {
		fmt.Println("healthy: no faults detected")
		return
	}
	for _, v := range vs {
		subject := v.Subject
		if subject == "" {
			subject = "host"
		}
		fmt.Printf("[%s] %s (%s)\n  %s\n", v.Health, v.Rung, subject, v.Reason)
		for _, e := range v.Evidence {
			fmt.Printf("  evidence: %s\n", e)
		}
		if v.NextAction != "" {
			fmt.Printf("  next: %s\n", v.NextAction)
		}
	}
}

// probeGuest gathers per-guest evidence over the same ssh path shell/exec use.
// Every probe failure degrades that field rather than aborting the diagnosis —
// one unreachable guest must not blind us to the rest of the host.
func probeGuest(cmd *cobra.Command, mv *client.MachineView) doctor.GuestEvidence {
	g := doctor.GuestEvidence{Name: mv.Name, State: string(mv.State), IP: mv.IP}
	if mv.State != "running" || mv.SSHPort == 0 {
		return g
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return g
	}

	g.SSHProbed = true
	args := sshArgs(mv, []string{"true"})
	if err := exec.CommandContext(cmd.Context(), sshPath, args[1:]...).Run(); err == nil {
		g.SSHOK = true
	}
	if !g.SSHOK {
		return g
	}

	uArgs := sshArgs(mv, []string{"systemctl", "list-units", `'actions.runner.*'`, "--no-legend", "--plain"})
	if out, err := exec.CommandContext(cmd.Context(), sshPath, uArgs[1:]...).CombinedOutput(); err == nil {
		for _, line := range strings.Split(string(out), "\n") {
			f := strings.Fields(line)
			if len(f) < 4 || !strings.HasPrefix(f[0], "actions.runner.") {
				continue
			}
			g.Runners = append(g.Runners, doctor.RunnerEvidence{Unit: f[0], Active: f[2] == "active"})
		}
	}

	if doctorDeep {
		cArgs := sshArgs(mv, []string{"bash", "-s"})
		c := exec.CommandContext(cmd.Context(), sshPath, cArgs[1:]...)
		c.Stdin = strings.NewReader(canaryScript)
		out, _ := c.CombinedOutput()
		g.LoadCanary = doctor.CanaryResult{Ran: true, Faulted: canaryFaulted(string(out))}
		if g.LoadCanary.Faulted {
			g.LoadCanary.Detail = "native binary exited with SIGILL/SIGSEGV under load"
		}
	}
	return g
}

func init() {
	doctorCmd.Flags().BoolVar(&doctorJSON, "json", false, "JSON output (watchdog probe)")
	doctorCmd.Flags().BoolVar(&doctorDeep, "deep", false, "also run the bounded native-binary load canary (mutating, ~60s)")
}
