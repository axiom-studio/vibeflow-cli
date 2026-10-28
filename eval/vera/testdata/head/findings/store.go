package findings

import (
	"sort"
	"sync"
	"time"
)

// Session is one completed review of a pull request.
type Session struct {
	ID        int64
	Repo      string
	CreatedAt time.Time
	Findings  []Finding
}

// Finding is a defect a review reported.
type Finding struct {
	Fingerprint string // the same for the same defect in every review of the repository
	Title       string
}

// Store keeps review sessions and the dismissals people made.
type Store struct {
	mu         sync.Mutex
	sessions   map[int64]Session
	dismissals map[string]string // fingerprint -> who dismissed it
}

// NewStore returns an empty store.
func NewStore() *Store {
	return &Store{sessions: map[int64]Session{}, dismissals: map[string]string{}}
}

// AddSession records a completed review.
func (s *Store) AddSession(sess Session) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.sessions[sess.ID] = sess
}

// Dismiss records that a person dismissed the finding with fingerprint.
func (s *Store) Dismiss(fingerprint, by string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.dismissals[fingerprint] = by
}

// Sessions returns every session, oldest first.
func (s *Store) Sessions() []Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Session, 0, len(s.sessions))
	for _, sess := range s.sessions {
		out = append(out, sess)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].CreatedAt.Before(out[j].CreatedAt) })
	return out
}

// DeleteSession removes one session and everything recorded about it.
func (s *Store) DeleteSession(id int64) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, f := range s.sessions[id].Findings {
		delete(s.dismissals, f.Fingerprint)
	}
	delete(s.sessions, id)
}
