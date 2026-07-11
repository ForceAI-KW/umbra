//go:build darwin

package image

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

// cloneFile uses APFS copy-on-write clonefile(2) so multi-GiB base images
// clone instantly; falls back to a byte copy on non-APFS volumes or if the
// destination filesystem rejects cloning.
func cloneFile(rawBase, dst string) error {
	_ = os.Remove(dst) // clonefile fails if dst exists
	err := unix.Clonefile(rawBase, dst, 0)
	if err == nil {
		return nil
	}
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EXDEV) {
		return copyFile(rawBase, dst)
	}
	return err
}
