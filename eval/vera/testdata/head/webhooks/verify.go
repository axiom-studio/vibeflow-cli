package webhooks

import (
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"strings"
)

// Verify reports whether signature, a provider's "sha256=<hex>" header, is
// the HMAC-SHA256 of body under secret.
func Verify(body []byte, signature, secret string) bool {
	if secret == "" {
		return false
	}
	sig, ok := strings.CutPrefix(signature, "sha256=")
	if !ok {
		return false
	}
	got, err := hex.DecodeString(sig)
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return hmac.Equal(got, mac.Sum(nil))
}
