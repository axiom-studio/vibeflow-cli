package repos

import (
	"errors"
	"sort"
	"sync"
)

// ErrNotFound means no linked repository has the ID.
var ErrNotFound = errors.New("linked repository not found")

// Store keeps linked repositories in memory.
type Store struct {
	mu    sync.Mutex
	repos map[int64]LinkedRepo
}

// NewStore returns an empty store.
func NewStore() *Store { return &Store{repos: map[int64]LinkedRepo{}} }

// Get returns the repository with id.
func (s *Store) Get(id int64) (LinkedRepo, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[id]
	if !ok {
		return LinkedRepo{}, ErrNotFound
	}
	return r, nil
}

// List returns every linked repository, by ID.
func (s *Store) List() []LinkedRepo {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]LinkedRepo, 0, len(s.repos))
	for _, r := range s.repos {
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out
}

// Put stores r whole, replacing the previous record.
func (s *Store) Put(r LinkedRepo) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.repos[r.ID] = r
}
