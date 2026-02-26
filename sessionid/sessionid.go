package sessionid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// GenerateSessionID returns a session ID for the given working directory.
// If a .vibeflow-session file exists in dir and contains a valid session ID
// (starts with "session-"), that ID is returned. Otherwise a new unique
// session ID is generated in the format session-YYYYMMDD-HHMMSS-XXXXXXXX.
func GenerateSessionID(dir string) string {
	if id := readSessionFile(dir); id != "" {
		return id
	}
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("session-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b))
}

// readSessionFile reads .vibeflow-session from dir and returns the session ID
// if valid. Returns empty string if the file is missing or contains no valid ID.
func readSessionFile(dir string) string {
	data, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session"))
	if err != nil {
		return ""
	}
	// The first line is the session ID.
	line := strings.SplitN(strings.TrimSpace(string(data)), "\n", 2)[0]
	line = strings.TrimSpace(line)
	if strings.HasPrefix(line, "session-") {
		return line
	}
	return ""
}
