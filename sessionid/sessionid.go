package sessionid

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"time"
)

// GenerateSessionID creates a unique session ID in the format session-YYYYMMDD-HHMMSS-XXXXXXXX.
func GenerateSessionID() string {
	b := make([]byte, 4)
	_, _ = rand.Read(b)
	return fmt.Sprintf("session-%s-%s", time.Now().UTC().Format("20060102-150405"), hex.EncodeToString(b))
}
