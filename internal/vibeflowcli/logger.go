package vibeflowcli

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	maxLogSize = 1 << 20 // 1 MB
)

// Logger writes timestamped log entries to ~/.vibeflow-cli/vibeflow-cli.log.
type Logger struct {
	mu   sync.Mutex
	path string
	file *os.File
}

// NewLogger opens (or creates) the log file under ~/.vibeflow-cli/.
func NewLogger() *Logger {
	home, err := os.UserHomeDir()
	if err != nil {
		return &Logger{} // no-op logger
	}

	dir := filepath.Join(home, ".vibeflow-cli")
	_ = os.MkdirAll(dir, 0755)

	logPath := filepath.Join(dir, "vibeflow-cli.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return &Logger{} // no-op logger
	}

	return &Logger{path: logPath, file: f}
}

// Close closes the underlying file.
func (l *Logger) Close() {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.file != nil {
		l.file.Close()
		l.file = nil
	}
}

// Info writes an info-level message.
func (l *Logger) Info(format string, args ...interface{}) {
	l.write("INFO", format, args...)
}

// Warn writes a warning-level message.
func (l *Logger) Warn(format string, args ...interface{}) {
	l.write("WARN", format, args...)
}

// Error writes an error-level message.
func (l *Logger) Error(format string, args ...interface{}) {
	l.write("ERROR", format, args...)
}

func (l *Logger) write(level, format string, args ...interface{}) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if l.file == nil {
		return
	}

	l.rotateIfNeeded()

	msg := fmt.Sprintf(format, args...)
	ts := time.Now().Format("2006-01-02 15:04:05")
	fmt.Fprintf(l.file, "%s [%s] %s\n", ts, level, msg)
}

func (l *Logger) rotateIfNeeded() {
	info, err := l.file.Stat()
	if err != nil || info.Size() < maxLogSize {
		return
	}

	// Truncate by closing, recreating.
	l.file.Close()
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, 0600)
	if err != nil {
		l.file = nil
		return
	}
	l.file = f
	fmt.Fprintf(l.file, "%s [INFO] Log rotated (exceeded %d bytes)\n",
		time.Now().Format("2006-01-02 15:04:05"), maxLogSize)
}
