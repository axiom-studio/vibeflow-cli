package sessionid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateSessionID returns a fresh unique session ID in the format
// session-YYYYMMDD-HHMMSS-XXXXXXXX. Each call produces a new ID so that
// multiple sessions in the same working directory get distinct tmux names.
// The .vibeflow-session file is NOT consulted here — API session reuse is
// handled separately via readSessionFileID in the session_init flow.
func GenerateSessionID(dir string) string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("session-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b))
}
