package main

import (
	"bytes"
	"context"
	"fmt"
	"os/exec"
	"path"
	"strings"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/client"
	"github.com/ForceAI-KW/umbra/internal/runner"
)

var runnerCmd = &cobra.Command{
	Use:   "runner",
	Short: "Manage GitHub Actions self-hosted runners inside a machine",
}

var (
	runnerAddRepo   string
	runnerAddLabels string
	runnerAddName   string
	runnerAddCount  int
	runnerListRepo  string
)

var runnerAddCmd = &cobra.Command{
	Use:   "add <machine> --repo <org/repo>",
	Short: "Install (and start) one or more self-hosted runner instances in a machine",
	Args:  cobra.ExactArgs(1),
	RunE:  runRunnerAdd,
}

var runnerListCmd = &cobra.Command{
	Use:   "list <machine>",
	Short: "List runner units installed in a machine (and their GitHub-side status with --repo)",
	Args:  cobra.ExactArgs(1),
	RunE:  runRunnerList,
}

var runnerHardenCmd = &cobra.Command{
	Use:   "harden <machine>",
	Short: "Apply the Restart=always watchdog to every runner unit already installed in a machine",
	Args:  cobra.ExactArgs(1),
	RunE:  runRunnerHarden,
}

func init() {
	runnerAddCmd.Flags().StringVar(&runnerAddRepo, "repo", "", "GitHub repo the runner registers against, org/repo (required)")
	runnerAddCmd.Flags().StringVar(&runnerAddLabels, "labels", "wsl2,umbra-ci", "comma-separated runner labels")
	runnerAddCmd.Flags().StringVar(&runnerAddName, "name", "", "runner name prefix (default: <machine>-<repo-basename>)")
	runnerAddCmd.Flags().IntVar(&runnerAddCount, "count", 1, "number of runner instances to install")
	_ = runnerAddCmd.MarkFlagRequired("repo")

	runnerListCmd.Flags().StringVar(&runnerListRepo, "repo", "", "also show GitHub-side runner status for org/repo")

	runnerCmd.AddCommand(runnerAddCmd, runnerListCmd, runnerHardenCmd)
}

// ghRegistrationToken fetches a fresh repo registration token via the host
// gh CLI. Tokens expire in 1 hour and are never logged — the caller must
// pass them straight into runner.InstallScript, not print or persist them.
func ghRegistrationToken(repo string) (string, error) {
	if _, err := exec.LookPath("gh"); err != nil {
		return "", fmt.Errorf("runner add needs the GitHub CLI (brew install gh) authenticated with repo admin")
	}
	out, err := exec.Command("gh", "api", "--method", "POST",
		"repos/"+repo+"/actions/runners/registration-token", "--jq", ".token").Output()
	if err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return "", fmt.Errorf("fetching registration token for %s: %w\n%s", repo, err, strings.TrimSpace(string(exitErr.Stderr)))
		}
		return "", fmt.Errorf("fetching registration token for %s: %w", repo, err)
	}
	token := strings.TrimSpace(string(out))
	if token == "" {
		return "", fmt.Errorf("gh returned an empty registration token for %s — is gh authenticated with repo admin?", repo)
	}
	return token, nil
}

// streamScript ssh's into the machine and pipes script into 'bash -s' on
// stdin, the same connection shell/exec use (via the shared sshArgs
// helper), returning the combined stdout+stderr for diagnostics.
func streamScript(ctx context.Context, mv *client.MachineView, script string) (string, error) {
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return "", err
	}
	args := sshArgs(mv, []string{"bash", "-s"})
	c := exec.CommandContext(ctx, sshPath, args[1:]...)
	c.Stdin = strings.NewReader(script)
	var out bytes.Buffer
	c.Stdout = &out
	c.Stderr = &out
	if err := c.Run(); err != nil {
		return out.String(), fmt.Errorf("running script on %s: %w", mv.Name, err)
	}
	return out.String(), nil
}

func getReachableMachine(cmd *cobra.Command, name string) (*client.MachineView, error) {
	mv, err := apiClient.GetMachine(cmd.Context(), name)
	if err != nil {
		return nil, err
	}
	if mv.SSHPort == 0 {
		return nil, fmt.Errorf("machine %q is not reachable (state: %s) — start it first", mv.Name, mv.State)
	}
	return mv, nil
}

func runRunnerAdd(cmd *cobra.Command, args []string) error {
	machine := args[0]
	mv, err := getReachableMachine(cmd, machine)
	if err != nil {
		return err
	}
	if runnerAddCount < 1 {
		return fmt.Errorf("--count must be >= 1")
	}

	token, err := ghRegistrationToken(runnerAddRepo)
	if err != nil {
		return err
	}

	repoURL := runnerAddRepo
	if !strings.Contains(repoURL, "://") {
		repoURL = "https://github.com/" + repoURL
	}
	repoBase := path.Base(runnerAddRepo)
	namePrefix := runnerAddName
	if namePrefix == "" {
		namePrefix = machine + "-" + repoBase
	}

	for i := 1; i <= runnerAddCount; i++ {
		name := fmt.Sprintf("%s-%d", namePrefix, i)
		dir := fmt.Sprintf("actions-runner-%s-%d", repoBase, i)
		script := runner.InstallScript(runner.InstallParams{
			RepoURL:    repoURL,
			Token:      token,
			RunnerName: name,
			DirName:    dir,
			Labels:     runnerAddLabels,
			Version:    runner.DefaultVersion,
		})
		fmt.Printf("installing runner %q in %s (%s)...\n", name, machine, dir)
		out, err := streamScript(cmd.Context(), mv, script)
		if out != "" {
			fmt.Println(out)
		}
		if err != nil {
			return err
		}
		fmt.Printf("installed runner %q\n", name)
	}
	return nil
}

func runRunnerList(cmd *cobra.Command, args []string) error {
	machine := args[0]
	mv, err := getReachableMachine(cmd, machine)
	if err != nil {
		return err
	}
	sshPath, err := exec.LookPath("ssh")
	if err != nil {
		return err
	}
	sArgs := sshArgs(mv, []string{"systemctl", "list-units", `'actions.runner.*'`, "--no-legend"})
	out, err := exec.CommandContext(cmd.Context(), sshPath, sArgs[1:]...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("listing runner units on %s: %w\n%s", machine, err, strings.TrimSpace(string(out)))
	}
	units := strings.TrimSpace(string(out))
	if units == "" {
		fmt.Printf("%s: no runner units installed\n", machine)
	} else {
		fmt.Println(units)
	}

	if runnerListRepo != "" {
		if _, err := exec.LookPath("gh"); err != nil {
			return fmt.Errorf("runner list --repo needs the GitHub CLI (brew install gh) authenticated with repo admin")
		}
		ghOut, err := exec.Command("gh", "api", "repos/"+runnerListRepo+"/actions/runners").Output()
		if err != nil {
			if exitErr, ok := err.(*exec.ExitError); ok {
				return fmt.Errorf("fetching GitHub-side runners for %s: %w\n%s", runnerListRepo, err, strings.TrimSpace(string(exitErr.Stderr)))
			}
			return fmt.Errorf("fetching GitHub-side runners for %s: %w", runnerListRepo, err)
		}
		fmt.Printf("\ngithub-side runners for %s:\n%s\n", runnerListRepo, strings.TrimSpace(string(ghOut)))
	}
	return nil
}

func runRunnerHarden(cmd *cobra.Command, args []string) error {
	machine := args[0]
	mv, err := getReachableMachine(cmd, machine)
	if err != nil {
		return err
	}
	out, err := streamScript(cmd.Context(), mv, runner.HardenScript())
	if out != "" {
		fmt.Println(out)
	}
	if err != nil {
		return err
	}
	fmt.Printf("hardened runner units on %s\n", machine)
	return nil
}
