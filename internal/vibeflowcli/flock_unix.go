//go:build !windows

package vibeflowcli

import (
	"fmt"
	"os"
	"syscall"
	"time"
)

// flockWithTimeout tries to acquire an exclusive flock, retrying until
// the timeout elapses.
func flockWithTimeout(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("flock timeout after %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

// flockRelease releases the exclusive flock on the file.
func flockRelease(f *os.File) error {
	return syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
}
