//go:build !windows

package vibeflowcli

import "syscall"

// processAlive checks whether a process with the given PID is running
// by sending signal 0.
func processAlive(pid int) bool {
	return syscall.Kill(pid, 0) == nil
}
