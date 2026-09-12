//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

func lockReviewFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("review runner or child is already active")
	}
	return f, nil
}

func runReviewProcess(ctx context.Context, cmd *exec.Cmd) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	// Descendants can inherit stdout after the provider itself exits. Bound the
	// copier wait so they cannot keep a completed review alive until its deadline.
	cmd.WaitDelay = 250 * time.Millisecond
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("could not start review provider")
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	select {
	case err := <-done:
		// The provider may leave descendants after returning its final result.
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if errors.Is(err, exec.ErrWaitDelay) && cmd.ProcessState.Success() {
			return nil
		}
		return err
	case <-ctx.Done():
		syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM)
		waited := false
		select {
		case <-done:
			waited = true
		case <-time.After(2 * time.Second):
		}
		syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		if !waited {
			// Reap before the guard releases its lock. WaitDelay bounds inherited
			// output pipes; returning on a timer would falsely release ownership.
			<-done
		}
		return ctx.Err()
	}
}
