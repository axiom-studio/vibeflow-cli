//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/charmbracelet/x/term"
	"golang.org/x/sys/unix"
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
	if cmd.SysProcAttr == nil {
		cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	}
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
		// SIGINT first lets an interactive harness close its UI cleanly.
		waited := false
		for _, stop := range []struct {
			sig   syscall.Signal
			grace time.Duration
		}{{syscall.SIGINT, time.Second}, {syscall.SIGTERM, 2 * time.Second}} {
			if waited {
				break
			}
			syscall.Kill(-cmd.Process.Pid, stop.sig)
			select {
			case <-done:
				waited = true
			case <-time.After(stop.grace):
			}
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

// reviewForegroundAttr starts an interactive harness in its own process group
// as tty's foreground group, so keys such as Ctrl-C reach the harness and not
// the runner. For Foreground, Ctty is this (parent) process's descriptor.
func reviewForegroundAttr(tty *os.File) *syscall.SysProcAttr {
	return &syscall.SysProcAttr{Setpgid: true, Foreground: true, Ctty: int(tty.Fd())}
}

// reviewTerminal is the runner's terminal while an interactive harness runs:
// its own stdin, stdout and stderr, handed to the harness as they are. The
// /dev/tty alias would also reach the pane, but macOS kqueue rejects it, which
// crashes Bun (claude, kiro) and crossterm (codex) harnesses at start.
type reviewTerminal struct {
	files [3]*os.File
	state *term.State
}

// openReviewTerminal returns the runner's terminal when stdin and out are
// one, which is what makes a foreground runner (a Vera tmux pane) interactive.
func openReviewTerminal(out any) *reviewTerminal {
	f, ok := out.(*os.File)
	if !ok || !term.IsTerminal(f.Fd()) || !term.IsTerminal(os.Stdin.Fd()) {
		return nil
	}
	stderr := os.Stderr
	if !term.IsTerminal(stderr.Fd()) {
		stderr = f
	}
	return &reviewTerminal{files: [3]*os.File{os.Stdin, f, stderr}}
}

// reviewTerminalFiles adopts the runner's terminal descriptors fd, fd+1 and
// fd+2 in the review child guard.
func reviewTerminalFiles(fd int) ([3]*os.File, error) {
	var files [3]*os.File
	for i := range files {
		if fd < 3 || !term.IsTerminal(uintptr(fd+i)) {
			return files, fmt.Errorf("interactive review needs the runner's terminal")
		}
		syscall.CloseOnExec(fd + i)
		files[i] = os.NewFile(uintptr(fd+i), "review-terminal")
	}
	return files, nil
}

func (t *reviewTerminal) save() { t.state, _ = term.GetState(t.files[0].Fd()) }

// reclaim takes the terminal back from a stopped harness: this process group
// is the foreground again, with the saved modes, no leftover input (such as
// late replies to the harness's terminal queries) and a fresh line. forced
// means the harness was killed and may still be in its alternate screen.
func (t *reviewTerminal) reclaim(forced bool) {
	fd := t.files[0].Fd()
	signal.Ignore(syscall.SIGTTOU) // Changing the terminal from the background.
	defer signal.Reset(syscall.SIGTTOU)
	_ = unix.IoctlSetPointerInt(int(fd), unix.TIOCSPGRP, syscall.Getpgrp())
	if t.state != nil {
		_ = term.Restore(fd, t.state)
	}
	time.Sleep(50 * time.Millisecond) // Let replies already in flight arrive.
	flushTerminalInput(int(fd))
	// Show the cursor; stop mouse, focus and bracketed-paste reporting; reset
	// colors. Leaving the alternate screen also restores an old cursor
	// position, which would overwrite a crashed harness's last words, so it
	// is sent only when the harness could not leave it itself.
	reset := "\x1b[?25h\x1b[?1000l\x1b[?1002l\x1b[?1003l\x1b[?1006l\x1b[?1004l\x1b[?2004l\x1b[0m\r\n"
	if forced {
		reset = "\x1b[?1049l" + reset
	}
	_, _ = t.files[1].WriteString(reset)
}

// veraQuietTermios is the listener's idle mode: keys are neither echoed nor
// line-edited, one at a time, while Ctrl-C still raises SIGINT and output
// keeps its newline handling.
func veraQuietTermios(t unix.Termios) unix.Termios {
	t.Lflag &^= unix.ECHO | unix.ICANON
	t.Cc[unix.VMIN], t.Cc[unix.VTIME] = 1, 0
	return t
}

// browse puts the terminal in the idle mode and hands every key read to
// forward until the returned stop, which restores the previous mode.
func (t *reviewTerminal) browse(forward func([]byte)) (stop func()) {
	fd := int(t.files[0].Fd())
	saved, err := unix.IoctlGetTermios(fd, reviewGetTermios)
	if err != nil {
		return func() {}
	}
	quiet := veraQuietTermios(*saved)
	if unix.IoctlSetTermios(fd, reviewSetTermios, &quiet) != nil {
		return func() {}
	}
	done, exited := make(chan struct{}), make(chan struct{})
	go func() {
		defer close(exited)
		buf := make([]byte, 256)
		fds := []unix.PollFd{{Fd: int32(fd), Events: unix.POLLIN}}
		for {
			// Poll briefly so stop never waits on a blocked read.
			n, err := unix.Poll(fds, 100)
			select {
			case <-done:
				return
			default:
			}
			if err != nil && err != unix.EINTR {
				return
			}
			if n <= 0 {
				continue
			}
			if n, err = unix.Read(fd, buf); err != nil || n == 0 {
				return
			}
			forward(buf[:n])
		}
	}()
	return func() {
		close(done)
		<-exited
		_ = unix.IoctlSetTermios(fd, reviewSetTermios, saved)
	}
}
