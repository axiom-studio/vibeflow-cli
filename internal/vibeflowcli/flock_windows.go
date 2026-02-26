//go:build windows

package vibeflowcli

import (
	"os"
	"time"
)

// flockWithTimeout is a no-op on Windows. vibeflow-cli requires tmux which
// is not available on Windows, so file locking is not meaningful.
func flockWithTimeout(f *os.File, timeout time.Duration) error {
	return nil
}

// flockRelease is a no-op on Windows.
func flockRelease(f *os.File) error {
	return nil
}
