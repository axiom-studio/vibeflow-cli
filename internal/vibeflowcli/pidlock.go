package vibeflowcli

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
)

// PIDLockPath returns the default PID lock file path (~/.vibeflow-cli/vibeflow.pid).
func PIDLockPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibeflow-cli", "vibeflow.pid")
}

// AcquirePIDLock checks for an existing vibeflow-cli process and writes the
// current PID if no other instance is running. Returns the running PID and an
// error if another instance is alive.
func AcquirePIDLock() error {
	path := PIDLockPath()

	// Ensure directory exists.
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create pid dir: %w", err)
	}

	// Check existing lock.
	if pid, alive := readPIDLock(path); alive {
		return fmt.Errorf("vibeflow is already running (PID: %d)", pid)
	}

	// Write current PID.
	data := []byte(strconv.Itoa(os.Getpid()))
	if err := os.WriteFile(path, data, 0600); err != nil {
		return fmt.Errorf("write pid lock: %w", err)
	}
	return nil
}

// ReleasePIDLock removes the PID lock file. Safe to call even if the file
// does not exist.
func ReleasePIDLock() {
	_ = os.Remove(PIDLockPath())
}

// IsVibeflowRunning reports whether another vibeflow-cli process holds the
// PID lock. Returns the PID if alive, 0 otherwise.
func IsVibeflowRunning() (int, bool) {
	return readPIDLock(PIDLockPath())
}

// readPIDLock reads the PID from the lock file and checks if the process is
// alive via kill -0. Returns (pid, true) if alive, (0, false) otherwise.
func readPIDLock(path string) (int, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return 0, false
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil || pid <= 0 {
		return 0, false
	}
	// Signal 0 checks process existence without actually sending a signal.
	if err := syscall.Kill(pid, 0); err != nil {
		return 0, false // Process is dead — stale PID file.
	}
	return pid, true
}
