//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"time"
)

func reviewProcessSignal(state *os.ProcessState) int {
	if status, ok := state.Sys().(syscall.WaitStatus); ok && status.Signaled() {
		return int(status.Signal())
	}
	return 0
}

func lockReviewFile(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	if err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, errReviewLockBusy
		}
		return nil, err
	}
	return f, nil
}

func retainReviewCapacityFD(fd int, cleanup *reviewProviderCleanup) (*os.File, error) {
	if fd == 0 && cleanup == nil {
		return nil, nil
	}
	if fd != 3 || cleanup == nil || cleanup.JobID == "" || cleanup.AttemptID == "" || len(cleanup.RequestID) != 36 {
		return nil, fmt.Errorf("invalid review capacity descriptor")
	}
	syscall.CloseOnExec(fd)
	file := os.NewFile(uintptr(fd), "review-capacity")
	if file == nil {
		return nil, fmt.Errorf("missing review capacity descriptor")
	}
	if err := cleanup.Reservation.validate(filepath.Dir(cleanup.Reservation.Directory)); err != nil {
		file.Close()
		return nil, err
	}
	actual, err := file.Stat()
	expected, pathErr := os.Lstat(filepath.Join(cleanup.Reservation.Directory, cleanup.Reservation.Slot))
	if err != nil || pathErr != nil || !expected.Mode().IsRegular() || !os.SameFile(actual, expected) {
		file.Close()
		return nil, fmt.Errorf("review capacity descriptor does not match its reservation")
	}
	return file, nil
}

func verifyReviewProcessGroupStopped(pid int) error {
	deadline := time.Now().Add(5 * time.Second)
	for {
		err := syscall.Kill(-pid, 0)
		if errors.Is(err, syscall.ESRCH) {
			return nil
		}
		if err != nil || time.Now().After(deadline) {
			return errReviewCleanupUnverified
		}
		time.Sleep(10 * time.Millisecond)
	}
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
		if cleanupErr := verifyReviewProcessGroupStopped(cmd.Process.Pid); cleanupErr != nil {
			return cleanupErr
		}
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
		if err := verifyReviewProcessGroupStopped(cmd.Process.Pid); err != nil {
			return err
		}
		return ctx.Err()
	}
}
