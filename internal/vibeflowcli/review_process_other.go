//go:build !darwin && !linux

package vibeflowcli

import (
	"context"
	"fmt"
	"os"
	"os/exec"
)

func lockReviewFile(string) (*os.File, error) {
	return nil, fmt.Errorf("isolated review runners currently require macOS or Linux")
}
func runReviewProcess(context.Context, *exec.Cmd) error {
	return fmt.Errorf("isolated review runners currently require macOS or Linux")
}
