// Package paths defines the ~/.umbra state-directory layout.
package paths

import (
	"os"
	"path/filepath"
)

func Root() string {
	if v := os.Getenv("UMBRA_ROOT"); v != "" {
		return v
	}
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".umbra")
}

func Machines() string              { return filepath.Join(Root(), "machines") }
func MachineDir(name string) string { return filepath.Join(Machines(), name) }
func Images() string                { return filepath.Join(Root(), "images") }
func Run() string                   { return filepath.Join(Root(), "run") }
func Logs() string                  { return filepath.Join(Root(), "log") }
func SSH() string                   { return filepath.Join(Root(), "ssh") }
func APISocket() string             { return filepath.Join(Run(), "api.sock") }
func LockFile() string              { return filepath.Join(Run(), "umbrad.lock") }

func EnsureTree() error {
	for _, d := range []string{Machines(), Images(), Run(), Logs(), SSH()} {
		if err := os.MkdirAll(d, 0o700); err != nil {
			return err
		}
	}
	return nil
}
