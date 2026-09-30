//go:build !darwin && !linux

package vibeflowcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
)

func reviewProcessSignal(*os.ProcessState) int { return 0 }

func retainReviewCapacityFD(fd int, cleanup *reviewProviderCleanup) (*os.File, error) {
	if fd == 0 && cleanup == nil {
		return nil, nil
	}
	return nil, fmt.Errorf("review runners currently require macOS or Linux")
}

func lockReviewFile(string) (*os.File, error) {
	return nil, fmt.Errorf("review runners currently require macOS or Linux")
}
func runReviewProcess(context.Context, *exec.Cmd) error {
	return fmt.Errorf("review runners currently require macOS or Linux")
}

func reviewForegroundAttr(*os.File) *syscall.SysProcAttr { return nil }

type reviewTerminal struct{ files [3]*os.File }

func openReviewTerminal(any) *reviewTerminal { return nil }
func (*reviewTerminal) save()                {}
func (*reviewTerminal) reclaim(bool)         {}

func reviewTerminalFiles(int) ([3]*os.File, error) {
	return [3]*os.File{}, fmt.Errorf("review runners currently require macOS or Linux")
}
