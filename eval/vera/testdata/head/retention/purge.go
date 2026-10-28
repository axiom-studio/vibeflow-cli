package retention

import (
	"time"

	"example.com/reviewfixture/findings"
)

// PurgeExpired deletes the review sessions older than keep, for the
// project's data retention setting, and returns how many it deleted.
func PurgeExpired(store *findings.Store, now time.Time, keep time.Duration) int {
	n := 0
	for _, sess := range store.Sessions() {
		if now.Sub(sess.CreatedAt) > keep {
			store.DeleteSession(sess.ID)
			n++
		}
	}
	return n
}
