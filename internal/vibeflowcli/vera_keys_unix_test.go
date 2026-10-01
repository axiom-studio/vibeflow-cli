//go:build darwin || linux

package vibeflowcli

import (
	"testing"

	"golang.org/x/sys/unix"
)

// While Vera listens its pane neither echoes nor line-edits keys, and Ctrl-C
// still interrupts.
func TestVeraQuietTermios(t *testing.T) {
	var cooked unix.Termios
	cooked.Lflag = unix.ECHO | unix.ICANON | unix.ISIG | unix.IEXTEN
	cooked.Oflag = unix.OPOST
	quiet := veraQuietTermios(cooked)
	if quiet.Lflag&(unix.ECHO|unix.ICANON) != 0 || quiet.Lflag&unix.ISIG == 0 || quiet.Oflag&unix.OPOST == 0 {
		t.Fatalf("quiet lflag %#x oflag %#x", quiet.Lflag, quiet.Oflag)
	}
	if quiet.Cc[unix.VMIN] != 1 || quiet.Cc[unix.VTIME] != 0 {
		t.Fatalf("quiet VMIN %d VTIME %d", quiet.Cc[unix.VMIN], quiet.Cc[unix.VTIME])
	}
}
