//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

type reviewTUIOutput struct {
	sync.Mutex
	text strings.Builder
}

func (b *reviewTUIOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.text.Write(p)
}

func (b *reviewTUIOutput) String() string {
	return ansi.Strip(b.RawString())
}

func (b *reviewTUIOutput) RawString() string {
	b.Lock()
	defer b.Unlock()
	return b.text.String()
}

type reviewTUITerminal struct {
	input  io.WriteCloser
	output reviewTUIOutput
	done   chan struct{}
	err    error
}

func startReviewTUITerminal(t *testing.T, binary, repo, root, binDir string, env ...string) *reviewTUITerminal {
	t.Helper()
	command := "stty rows 30 cols 100; exec " + shellQuote(binary) + " --root " + shellQuote(root)
	args := []string{"-q", "/dev/null", "/bin/sh", "-c", command}
	if runtime.GOOS == "linux" {
		args = []string{"-q", "-e", "-c", command, "/dev/null"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, "script", args...)
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "TERM=xterm-256color", "LANG=en_US.UTF-8"}
	if len(env) == 0 {
		env = []string{"NO_COLOR=1"}
	}
	cmd.Env = append(cmd.Env, env...)
	terminal := &reviewTUITerminal{done: make(chan struct{})}
	var err error
	terminal.input, err = cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = &terminal.output, &terminal.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		terminal.err = cmd.Wait()
		close(terminal.done)
	}()
	t.Cleanup(func() {
		defer cancel()
		defer terminal.input.Close()
		select {
		case <-terminal.done:
			return
		default:
		}
		_, _ = io.WriteString(terminal.input, "\x03")
		select {
		case <-terminal.done:
		case <-time.After(5 * time.Second):
			cancel()
			<-terminal.done
		}
	})
	return terminal
}

func (terminal *reviewTUITerminal) send(t *testing.T, input string) {
	t.Helper()
	if _, err := io.WriteString(terminal.input, input); err != nil {
		t.Fatal(err)
	}
}

// Bubble Tea's renderer scrolls part of the screen with a DECSTBM region;
// a replay that ignores it leaves rows where the terminal no longer has them.
func TestTerminalScreenReplaysScrollRegion(t *testing.T) {
	raw := "\x1b[1;1Hhead\x1b[2;1Hone\x1b[3;1Htwo\x1b[4;1Hfoot" +
		"\x1b[2;3r\x1b[3;1H\n" + // Scroll rows 2-3 up one line.
		"\x1b[r\x1b[3;1Hthree"
	got := terminalScreen(raw, 10, 4)
	if want := "head\ntwo\nthree\nfoot"; got != want {
		t.Fatalf("screen\n%q\nwant\n%q", got, want)
	}
}
