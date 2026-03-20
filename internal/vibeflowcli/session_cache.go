/*
 * Copyright (c) 2026. AXIOM STUDIO AI Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package vibeflowcli

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

// SessionCache persists session launch parameters for restart-without-intervention.
// It uses a separate file from the active session Store so that dead sessions
// remain available for restart even after Store.Sync() cleans them up.
type SessionCache struct {
	path string
}

// DefaultCachePath returns the default session_cache.json path under ~/.vibeflow-cli/.
func DefaultCachePath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibeflow-cli", "session_cache.json")
}

// NewSessionCache creates a SessionCache backed by the default file path.
func NewSessionCache() *SessionCache {
	return &SessionCache{path: DefaultCachePath()}
}

// NewSessionCacheWithPath creates a SessionCache backed by a custom file path.
func NewSessionCacheWithPath(path string) *SessionCache {
	return &SessionCache{path: path}
}

// Add inserts or replaces a session entry in the cache.
func (c *SessionCache) Add(meta SessionMeta) error {
	_, err := c.withLock(func(entries []SessionMeta) ([]SessionMeta, error) {
		out := make([]SessionMeta, 0, len(entries)+1)
		for _, e := range entries {
			if e.Name != meta.Name {
				out = append(out, e)
			}
		}
		out = append(out, meta)
		return out, nil
	})
	return err
}

// Remove deletes the entry with the given name from the cache.
func (c *SessionCache) Remove(name string) error {
	_, err := c.withLock(func(entries []SessionMeta) ([]SessionMeta, error) {
		out := make([]SessionMeta, 0, len(entries))
		for _, e := range entries {
			if e.Name != name {
				out = append(out, e)
			}
		}
		return out, nil
	})
	return err
}

// List returns all cached session entries.
func (c *SessionCache) List() ([]SessionMeta, error) {
	entries, err := c.withLock(func(entries []SessionMeta) ([]SessionMeta, error) {
		return entries, nil
	})
	if err != nil {
		return nil, err
	}
	return entries, nil
}

// GC removes entries whose TmuxSession is not in the activeTmux list.
func (c *SessionCache) GC(activeTmux []string) error {
	active := make(map[string]bool, len(activeTmux))
	for _, name := range activeTmux {
		active[name] = true
	}

	_, err := c.withLock(func(entries []SessionMeta) ([]SessionMeta, error) {
		out := make([]SessionMeta, 0, len(entries))
		for _, e := range entries {
			if active[e.TmuxSession] {
				out = append(out, e)
			}
		}
		return out, nil
	})
	return err
}

// DeadSessions returns entries that are in the cache but not in the active
// tmux session list — these are sessions that died and can be restarted.
func (c *SessionCache) DeadSessions(activeTmux []string) ([]SessionMeta, error) {
	active := make(map[string]bool, len(activeTmux))
	for _, name := range activeTmux {
		active[name] = true
	}

	entries, err := c.List()
	if err != nil {
		return nil, err
	}

	var dead []SessionMeta
	for _, e := range entries {
		if !active[e.TmuxSession] {
			dead = append(dead, e)
		}
	}
	return dead, nil
}

// withLock acquires an exclusive file lock, reads the current entries,
// calls fn with them, and writes the result back.
func (c *SessionCache) withLock(fn func([]SessionMeta) ([]SessionMeta, error)) ([]SessionMeta, error) {
	if err := os.MkdirAll(filepath.Dir(c.path), 0755); err != nil {
		return nil, fmt.Errorf("create cache dir: %w", err)
	}

	lockPath := c.path + ".lock"
	lf, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open cache lock file: %w", err)
	}
	defer lf.Close()

	if err := flockWithTimeout(lf, 5*time.Second); err != nil {
		return nil, fmt.Errorf("acquire cache lock: %w", err)
	}
	defer flockRelease(lf) //nolint:errcheck

	entries, err := c.readFile()
	if err != nil {
		return nil, err
	}

	result, err := fn(entries)
	if err != nil {
		return nil, err
	}

	if err := c.writeFile(result); err != nil {
		return nil, err
	}
	return result, nil
}

func (c *SessionCache) readFile() ([]SessionMeta, error) {
	data, err := os.ReadFile(c.path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("read cache: %w", err)
	}
	if len(data) == 0 {
		return nil, nil
	}

	var entries []SessionMeta
	if err := json.Unmarshal(data, &entries); err != nil {
		return nil, fmt.Errorf("parse cache: %w", err)
	}
	return entries, nil
}

func (c *SessionCache) writeFile(entries []SessionMeta) error {
	data, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal cache: %w", err)
	}
	return os.WriteFile(c.path, data, 0600)
}
