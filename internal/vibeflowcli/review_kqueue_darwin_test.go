package vibeflowcli

import (
	"fmt"
	"os"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

// Bun (claude, kiro) and crossterm (codex) watch stdin with kqueue, which
// macOS refuses for the /dev/tty alias. This probe is such a harness.
func init() {
	if len(os.Args) == 2 && os.Args[1] == "kqueue-stdin-probe" {
		os.Exit(kqueueStdinProbe())
	}
}

func kqueueStdinProbe() int {
	kq, err := unix.Kqueue()
	if err != nil {
		fmt.Println("kqueue:", err)
		return 1
	}
	changes := make([]unix.Kevent_t, 1)
	unix.SetKevent(&changes[0], 0, unix.EVFILT_READ, unix.EV_ADD)
	if _, err = unix.Kevent(kq, changes, nil, nil); err != nil {
		fmt.Println("kevent on stdin:", err)
		return 1
	}
	return 0
}

// The harness must get the pane's real terminal device, which kqueue accepts.
func TestReviewWatchInteractiveHarnessCanKqueueStdin(t *testing.T) {
	probe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	v := startVeraPane(t, true, func(e *reviewExecution, mark func(string) string) string {
		return shellQuote(probe) + " kqueue-stdin-probe || exit 7\n" +
			"cat " + shellQuote(reviewWatchResult(t, e, 0)) + " > ../../result.json\n" +
			"exec sleep 300\n"
	})
	v.waitFor("a result from a harness that watches stdin with kqueue", func() bool { return v.results.Load() == 1 })
}

// Replies to a harness's terminal queries that arrive after it stopped must
// not stay queued as input for the next harness (or echo as ^[[8;1R).
func TestReviewWatchInteractiveFlushesLeftoverInput(t *testing.T) {
	v := startVeraPane(t, true, func(e *reviewExecution, mark func(string) string) string {
		return "stty raw -echo\n" +
			"printf '\\033[6n\\033[c'\n" + // Cursor position and device attributes.
			"sleep 0.3\n" +
			"cat " + shellQuote(reviewWatchResult(t, e, 0)) + " > ../../result.json\n" +
			"exec sleep 300\n"
	})
	v.waitFor("listening again", func() bool {
		text := v.capture()
		i := strings.Index(text, "Result: clean.")
		return i >= 0 && strings.Contains(text[i:], reviewListeningLine)
	})
	if strings.Contains(v.capture(), "^[[") {
		t.Fatalf("terminal replies echoed into the pane:\n%s", v.capture())
	}
	path, err := v.tm.run("display-message", "-p", "-t", v.session, "#{pane_tty}")
	if err != nil {
		t.Fatal(err)
	}
	tty, err := os.Open(strings.TrimSpace(path))
	if err != nil {
		t.Skipf("cannot open the pane's terminal: %v", err)
	}
	defer tty.Close()
	// Canonical mode hides a partial line from FIONREAD; the test is over, so
	// switch the pane to non-canonical input to count what is queued.
	termios, err := unix.IoctlGetTermios(int(tty.Fd()), unix.TIOCGETA)
	if err != nil {
		t.Fatal(err)
	}
	termios.Lflag &^= unix.ICANON
	if err = unix.IoctlSetTermios(int(tty.Fd()), unix.TIOCSETA, termios); err != nil {
		t.Fatal(err)
	}
	if n, err := unix.IoctlGetInt(int(tty.Fd()), 0x4004667f); err != nil || n != 0 { // FIONREAD
		t.Fatalf("%d bytes of leftover input queued for the listener (%v); pane:\n%s", n, err, v.capture())
	}
}
