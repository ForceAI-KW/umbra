package main

import (
	"fmt"
	"os"

	"github.com/spf13/cobra"

	"github.com/ForceAI-KW/umbra/internal/export"
	"github.com/ForceAI-KW/umbra/internal/paths"
)

var exportOut string

// exportCmd is read-only, like `shell` reading paths.SSH() directly — no
// need to round-trip the daemon just to tar up two files it isn't
// currently touching.
var exportCmd = &cobra.Command{
	Use:   "export <machine> [-o file.tar.gz]",
	Short: "Export a stopped machine's config + disk to a tarball for migration",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		name := args[0]
		mv, err := apiClient.GetMachine(cmd.Context(), name)
		if err != nil {
			return err
		}
		if mv.State != "stopped" {
			return fmt.Errorf("machine %q must be stopped to export (state: %s)", name, mv.State)
		}
		out := exportOut
		if out == "" {
			out = name + ".tar.gz"
		}
		if err := export.Write(paths.MachineDir(name), out); err != nil {
			return err
		}
		fmt.Printf("exported %s to %s\n", name, out)
		return nil
	},
}

var importName string

var importCmd = &cobra.Command{
	Use:   "import <file.tar.gz> [--name newname]",
	Short: "Import a machine tarball produced by 'umbra export'",
	Args:  cobra.ExactArgs(1),
	RunE: func(cmd *cobra.Command, args []string) error {
		file := args[0]
		if err := paths.EnsureTree(); err != nil {
			return err
		}
		staging, err := os.MkdirTemp(paths.Run(), "import-*")
		if err != nil {
			return err
		}
		m, err := export.Read(file, staging)
		if err != nil {
			os.RemoveAll(staging)
			return err
		}
		name := importName
		if name == "" {
			name = m.Name
		}
		mv, err := apiClient.ImportMachine(cmd.Context(), name, staging)
		if err != nil {
			os.RemoveAll(staging) // daemon didn't take ownership of it; clean up
			return err
		}
		fmt.Printf("imported %s (%d cpu, %d GiB mem, %d GiB disk)\n", mv.Name, mv.CPUs, mv.MemoryMiB/1024, mv.DiskGiB)
		return nil
	},
}

func init() {
	exportCmd.Flags().StringVarP(&exportOut, "output", "o", "", "output tarball path (default <machine>.tar.gz)")
	importCmd.Flags().StringVar(&importName, "name", "", "name for the imported machine (default: name stored in the tarball)")
}
