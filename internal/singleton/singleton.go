// Package singleton provides a file-lock based single-instance guard for umbrad.
package singleton

import (
	"fmt"
	"os"
	"syscall"
)

// Lock represents an acquired lock on the singleton lock file.
type Lock struct {
	file *os.File
}

// Acquire attempts to acquire an exclusive lock on the file at path.
// Returns a Lock on success, or an error if the lock is already held.
func Acquire(path string) (*Lock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("failed to open lock file: %w", err)
	}

	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err != nil {
		f.Close()
		if err == syscall.EWOULDBLOCK {
			return nil, fmt.Errorf("another umbrad is already running (lock held on %s) — check `pgrep umbrad`", path)
		}
		return nil, fmt.Errorf("failed to acquire lock: %w", err)
	}

	return &Lock{file: f}, nil
}

// Close releases the lock.
func (l *Lock) Close() error {
	if l.file == nil {
		return nil
	}
	_ = syscall.Flock(int(l.file.Fd()), syscall.LOCK_UN)
	return l.file.Close()
}
