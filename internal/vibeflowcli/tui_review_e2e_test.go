//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"io"
	"os/exec"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/charmbracelet/x/ansi"
)

type reviewTUIOutput struct {
	sync.Mutex
	text strings.Builder
}

// Replays the PTY's cursor writes so assertions inspect the current display,
// not text retained in earlier terminal frames.
func reviewVisibleScreen(raw string, width, height int) string {
	cells := make([][]rune, height)
	for i := range cells {
		cells[i] = make([]rune, width)
		for j := range cells[i] {
			cells[i][j] = ' '
		}
	}
	x, y := 0, 0
	for i := 0; i < len(raw); {
		if raw[i] == '\x1b' && i+1 < len(raw) {
			if raw[i+1] == '[' {
				start := i + 2
				i = start
				for i < len(raw) && (raw[i] < '@' || raw[i] > '~') {
					i++
				}
				if i == len(raw) {
					break
				}
				command, params := raw[i], strings.Split(raw[start:i], ";")
				i++
				number := func(index, fallback int) int {
					if index >= len(params) {
						return fallback
					}
					v, err := strconv.Atoi(params[index])
					if err != nil || v == 0 {
						return fallback
					}
					return v
				}
				switch command {
				case 'H', 'f':
					y, x = number(0, 1)-1, number(1, 1)-1
				case 'A':
					y -= number(0, 1)
				case 'B':
					y += number(0, 1)
				case 'C':
					x += number(0, 1)
				case 'D':
					x -= number(0, 1)
				case 'G':
					x = number(0, 1) - 1
				case 'd':
					y = number(0, 1) - 1
				case 'J':
					from := max(0, y)
					if number(0, 0) == 2 {
						from = 0
					}
					for row := from; row < height; row++ {
						startCol := 0
						if row == y && number(0, 0) != 2 {
							startCol = max(0, x)
						}
						for col := startCol; col < width; col++ {
							cells[row][col] = ' '
						}
					}
				case 'K':
					if y >= 0 && y < height {
						for col := max(0, x); col < width; col++ {
							cells[y][col] = ' '
						}
					}
				case 'X':
					if y >= 0 && y < height {
						for col := max(0, x); col < min(width, x+number(0, 1)); col++ {
							cells[y][col] = ' '
						}
					}
				case 'P':
					if y >= 0 && y < height && x >= 0 && x < width {
						count := min(number(0, 1), width-x)
						copy(cells[y][x:], cells[y][x+count:])
						for col := width - count; col < width; col++ {
							cells[y][col] = ' '
						}
					}
				}
				continue
			}
			if raw[i+1] == ']' {
				i += 2
				for i < len(raw) && raw[i] != '\a' && !(raw[i] == '\x1b' && i+1 < len(raw) && raw[i+1] == '\\') {
					i++
				}
				if i < len(raw) && raw[i] == '\x1b' {
					i += 2
				} else if i < len(raw) {
					i++
				}
				continue
			}
			i += 2
			continue
		}
		switch raw[i] {
		case '\r':
			x, i = 0, i+1
			continue
		case '\n':
			y, i = y+1, i+1
			if y == height {
				copy(cells, cells[1:])
				cells[height-1] = make([]rune, width)
				for col := range cells[height-1] {
					cells[height-1][col] = ' '
				}
				y--
			}
			continue
		}
		r, size := utf8.DecodeRuneInString(raw[i:])
		i += size
		if r < 32 {
			continue
		}
		if y >= 0 && y < height && x >= 0 && x < width {
			cells[y][x] = r
		}
		x += ansi.StringWidth(string(r))
	}
	lines := make([]string, height)
	for i := range cells {
		lines[i] = strings.TrimRight(string(cells[i]), " ")
	}
	return strings.Join(lines, "\n")
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
	if !slices.Contains(env, "TEST_CRA_DISABLED=1") {
		command += " --cra"
	}
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
