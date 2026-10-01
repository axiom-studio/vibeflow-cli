package vibeflowcli

import "golang.org/x/sys/unix"

// flushTerminalInput discards unread terminal input (tcflush TCIFLUSH).
func flushTerminalInput(fd int) { _ = unix.IoctlSetPointerInt(fd, unix.TIOCFLUSH, unix.TCIFLUSH) }

const reviewGetTermios, reviewSetTermios = unix.TIOCGETA, unix.TIOCSETA
