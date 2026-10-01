package vibeflowcli

import "golang.org/x/sys/unix"

// flushTerminalInput discards unread terminal input (tcflush TCIFLUSH).
func flushTerminalInput(fd int) { _ = unix.IoctlSetInt(fd, unix.TCFLSH, unix.TCIFLUSH) }

const reviewGetTermios, reviewSetTermios = unix.TCGETS, unix.TCSETS
