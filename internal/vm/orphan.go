package vm

import (
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Orphaned-VM reaping (2026-07-13 incident): a SIGKILLed daemon (launchctl
// kickstart -k, crash, or a careless reinstall) orphans each running VM's
// com.apple.Virtualization.VirtualMachine XPC process. The fresh daemon's
// instance map says StateStopped, so Start() happily launches a SECOND VM
// against the same disk.img — the two fight (boot loops at 400%+ CPU, guest
// network never comes up) and the guest fs accumulates corruption. Two
// machines were lost to this before the guard existed.
//
// The guard: before launching, list processes that hold the machine's
// disk.img open. Any holder is by definition not ours (vz owns the fd inside
// the XPC, not umbrad). Reap holders that are verifiably Virtualization XPC
// processes; refuse to launch if anything else holds the disk or a holder
// survives the reap.

// diskHoldersFn lists pids of processes holding disk open (excluding this
// process). Seam for tests; real implementation shells out to lsof.
var diskHoldersFn = diskHolders

// reapHolderFn kills one orphan holder after verifying it is a vz XPC
// process. Seam for tests.
var reapHolderFn = reapHolder

// reapOrphanHolders makes disk safe to launch against: probe → reap vz
// orphans → re-probe. Returns nil when no process holds the disk.
func reapOrphanHolders(disk string) error {
	pids := diskHoldersFn(disk)
	if len(pids) == 0 {
		return nil
	}
	log.Printf("vm: disk %s is held by orphaned process(es) %v — reaping before launch (P: orphaned vz XPC after daemon SIGKILL)", filepath.Base(filepath.Dir(disk)), pids)
	for _, pid := range pids {
		if err := reapHolderFn(pid); err != nil {
			return fmt.Errorf("disk %s is held by pid %d and cannot be reaped: %w", disk, pid, err)
		}
	}
	// Bounded wait for the kernel to release the fds after SIGKILL.
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if left := diskHoldersFn(disk); len(left) == 0 {
			return nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	return fmt.Errorf("disk %s still held by %v after reap — refusing to double-launch", disk, diskHoldersFn(disk))
}

// diskHolders shells out to lsof (present on every macOS) for pids with disk
// open, excluding our own pid. lsof exits 1 when nothing matches — treat any
// error as "no holders" (the guard is best-effort; a launch proceeding is the
// pre-guard status quo, and vz itself errors if the image is truly locked).
func diskHolders(disk string) []int {
	out, err := exec.Command("lsof", "-t", "--", disk).Output()
	if err != nil {
		return nil
	}
	var pids []int
	self := os.Getpid()
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}
		pid, err := strconv.Atoi(line)
		if err != nil || pid == self {
			continue
		}
		pids = append(pids, pid)
	}
	return pids
}

// reapHolder SIGKILLs pid after verifying it is a Virtualization XPC process
// — never kill an arbitrary disk holder (backup tools, forensics, a second
// daemon's healthy VM are all possible and must be surfaced, not shot).
func reapHolder(pid int) error {
	out, err := exec.Command("ps", "-o", "comm=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		return fmt.Errorf("cannot identify pid %d: %w", pid, err)
	}
	comm := strings.TrimSpace(string(out))
	if !strings.Contains(comm, "Virtualization") {
		return fmt.Errorf("pid %d (%s) is not a Virtualization XPC process; refusing to kill it", pid, comm)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		return fmt.Errorf("kill orphan vz pid %d: %w", pid, err)
	}
	log.Printf("vm: reaped orphaned vz XPC pid %d (%s)", pid, comm)
	return nil
}
