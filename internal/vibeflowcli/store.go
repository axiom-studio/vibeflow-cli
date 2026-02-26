package vibeflowcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// SessionMeta holds metadata for a vibeflow-cli session that tmux alone
// cannot store (provider, worktree path, vibeflow session ID, etc.).
type SessionMeta struct {
	Name              string    `json:"name"`
	TmuxSession       string    `json:"tmux_session"`
	Provider          string    `json:"provider"`
	Project           string    `json:"project"`
	Persona           string    `json:"persona,omitempty"`
	Branch            string    `json:"branch"`
	WorktreePath      string    `json:"worktree_path,omitempty"`
	WorkingDir        string    `json:"working_dir"`
	VibeFlowSessionID string    `json:"vibeflow_session_id,omitempty"`
	CreatedAt         time.Time `json:"created_at"`
}

// Store persists session metadata to a JSON file with file-level locking
// for concurrency safety.
type Store struct {
	path string
}

// DefaultStorePath returns the default sessions.json path under ~/.vibeflow-cli/.
func DefaultStorePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibeflow-cli", "sessions.json")
}

// NewStore creates a Store that reads/writes the default sessions file.
func NewStore() *Store {
	return &Store{path: DefaultStorePath()}
}

// NewStoreWithPath creates a Store backed by a custom file path.
func NewStoreWithPath(path string) *Store {
	return &Store{path: path}
}

// List returns all stored session metadata entries.
func (s *Store) List() ([]SessionMeta, error) {
	sessions, err := s.withLock(func(sessions []SessionMeta) ([]SessionMeta, error) {
		return sessions, nil
	})
	if err != nil {
		return nil, err
	}
	return sessions, nil
}

// Get returns the session metadata for the given name and whether it was found.
func (s *Store) Get(name string) (SessionMeta, bool, error) {
	sessions, err := s.List()
	if err != nil {
		return SessionMeta{}, false, err
	}
	for _, m := range sessions {
		if m.Name == name {
			return m, true, nil
		}
	}
	return SessionMeta{}, false, nil
}

// Add appends a session to the store. If a session with the same name
// already exists it is replaced.
func (s *Store) Add(meta SessionMeta) error {
	_, err := s.withLock(func(sessions []SessionMeta) ([]SessionMeta, error) {
		// Replace existing entry with the same name.
		out := make([]SessionMeta, 0, len(sessions)+1)
		for _, m := range sessions {
			if m.Name != meta.Name {
				out = append(out, m)
			}
		}
		out = append(out, meta)
		return out, nil
	})
	return err
}

// Remove deletes the session with the given name from the store.
func (s *Store) Remove(name string) error {
	_, err := s.withLock(func(sessions []SessionMeta) ([]SessionMeta, error) {
		out := make([]SessionMeta, 0, len(sessions))
		for _, m := range sessions {
			if m.Name != name {
				out = append(out, m)
			}
		}
		return out, nil
	})
	return err
}

// Sync removes entries whose TmuxSession is not in the activeTmux list.
// Call this on TUI refresh to clean up sessions that died unexpectedly.
func (s *Store) Sync(activeTmux []string) error {
	active := make(map[string]bool, len(activeTmux))
	for _, name := range activeTmux {
		active[name] = true
	}

	_, err := s.withLock(func(sessions []SessionMeta) ([]SessionMeta, error) {
		out := make([]SessionMeta, 0, len(sessions))
		for _, m := range sessions {
			if active[m.TmuxSession] {
				out = append(out, m)
			}
		}
		return out, nil
	})
	return err
}

// Discover cross-references live tmux session names against the store and
// returns sessions that are live but have no store entry (orphaned/recovered).
// For each discovered session, it creates a minimal SessionMeta from
// the tmux name. The caller is responsible for enriching with workdir/branch.
func (s *Store) Discover(liveTmuxNames []string) []string {
	sessions, err := s.List()
	if err != nil {
		return nil
	}
	known := make(map[string]bool, len(sessions))
	for _, m := range sessions {
		known[m.TmuxSession] = true
	}
	var discovered []string
	for _, name := range liveTmuxNames {
		if !known[name] {
			discovered = append(discovered, name)
		}
	}
	return discovered
}

// withLock acquires an exclusive file lock, reads the current sessions,
// calls fn with them, and writes the result back. The lock is released
// when the function returns.
func (s *Store) withLock(fn func([]SessionMeta) ([]SessionMeta, error)) ([]SessionMeta, error) {
	// Ensure parent directory exists.
	if err := os.MkdirAll(filepath.Dir(s.path), 0755); err != nil {
		return nil, fmt.Errorf("create store dir: %w", err)
	}

	// Open (or create) the lock file alongside the data file.
	lockPath := s.path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open lock file: %w", err)
	}
	defer lf.Close()

	// Acquire exclusive lock with a 5-second timeout.
	if err := flockWithTimeout(lf, 5*time.Second); err != nil {
		return nil, fmt.Errorf("acquire lock: %w", err)
	}
	defer syscall.Flock(int(lf.Fd()), syscall.LOCK_UN) //nolint:errcheck

	// Read current data.
	sessions, err := s.readFile()
	if err != nil {
		return nil, err
	}

	// Apply the mutation.
	result, err := fn(sessions)
	if err != nil {
		return nil, err
	}

	// Write back.
	if err := s.writeFile(result); err != nil {
		return nil, err
	}
	return result, nil
}

// readFile reads and parses the JSON sessions file. Returns an empty slice
// if the file does not exist.
func (s *Store) readFile() ([]SessionMeta, error) {
	data, err := os.ReadFile(s.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read store: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var sessions []SessionMeta
	if err := json.Unmarshal(data, &sessions); err != nil {
		return nil, fmt.Errorf("parse store: %w", err)
	}
	return sessions, nil
}

// writeFile serialises sessions to JSON and writes to disk atomically.
func (s *Store) writeFile(sessions []SessionMeta) error {
	data, err := json.MarshalIndent(sessions, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal store: %w", err)
	}
	return os.WriteFile(s.path, data, 0600)
}

// flockWithTimeout tries to acquire an exclusive flock, retrying until
// the timeout elapses.
func flockWithTimeout(f *os.File, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("flock timeout after %s", timeout)
		}
		time.Sleep(50 * time.Millisecond)
	}
}
