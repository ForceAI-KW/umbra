// Package snapshot takes and restores point-in-time copies of a machine's
// disk image. On APFS the copy is clonefile(2) — instant and space-shared —
// with a plain copy fallback for non-APFS filesystems (or cross-volume).
// Snapshot layout: <machineDir>/snapshots/<snapName>/{disk.img,config.json}.
// Callers (the daemon) must ensure the machine is STOPPED first.
package snapshot

import (
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"golang.org/x/sys/unix"
)

type Info struct {
	Name      string    `json:"name"`
	CreatedAt time.Time `json:"created_at"`
	SizeBytes int64     `json:"size_bytes"`
}

// cloneOrCopy clonefiles src to dst, falling back to a streamed copy when
// the filesystem refuses (ENOTSUP: non-APFS; EXDEV: cross-volume).
func cloneOrCopy(src, dst string) error {
	err := unix.Clonefile(src, dst, 0)
	if err == nil {
		return nil
	}
	if !errors.Is(err, unix.ENOTSUP) && !errors.Is(err, unix.EXDEV) {
		return fmt.Errorf("clonefile %s: %w", src, err)
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o600)
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

func Take(machineDir, snapDir, snapName string) error {
	dir := filepath.Join(snapDir, snapName)
	if _, err := os.Stat(dir); err == nil {
		return fmt.Errorf("snapshot %q already exists", snapName)
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return err
	}
	if err := cloneOrCopy(filepath.Join(machineDir, "disk.img"), filepath.Join(dir, "disk.img")); err != nil {
		os.RemoveAll(dir) // don't leave a half-snapshot behind
		return err
	}
	if err := cloneOrCopy(filepath.Join(machineDir, "config.json"), filepath.Join(dir, "config.json")); err != nil {
		os.RemoveAll(dir)
		return err
	}
	return nil
}

func List(snapDir string) ([]Info, error) {
	entries, err := os.ReadDir(snapDir)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var out []Info
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		st, err := os.Stat(filepath.Join(snapDir, e.Name(), "disk.img"))
		if err != nil {
			continue // half-snapshot; Take cleans these up, ignore
		}
		out = append(out, Info{Name: e.Name(), CreatedAt: st.ModTime(), SizeBytes: st.Size()})
	}
	return out, nil
}

// Restore replaces machineDir/disk.img with the snapshot's copy. The
// current image is cloned aside to disk.img.pre-restore first so a failed
// restore never destroys the only copy; it is removed on success.
func Restore(machineDir, snapDir, snapName string) error {
	src := filepath.Join(snapDir, snapName, "disk.img")
	if _, err := os.Stat(src); err != nil {
		return fmt.Errorf("snapshot %q not found", snapName)
	}
	live := filepath.Join(machineDir, "disk.img")
	backup := live + ".pre-restore"
	os.Remove(backup)
	if err := cloneOrCopy(live, backup); err != nil {
		return err
	}
	if err := os.Remove(live); err != nil {
		return err
	}
	if err := cloneOrCopy(src, live); err != nil {
		// bring the original back — never leave the machine diskless
		if renameErr := os.Rename(backup, live); renameErr != nil {
			return fmt.Errorf("restore failed (%v) AND recovery failed — machine %s may be diskless: %w", err, live, renameErr)
		}
		return err
	}
	os.Remove(backup)
	return nil
}
