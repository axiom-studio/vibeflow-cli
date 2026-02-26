//go:build windows

package vibeflowcli

// processAlive is a stub on Windows. vibeflow-cli requires tmux which is
// not available on Windows, so PID lock checking is not meaningful.
func processAlive(pid int) bool {
	return false
}
