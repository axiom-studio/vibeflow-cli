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
	"os/exec"
	"path/filepath"
	"testing"
)

// TestRefreshSessions_RetainsDeadStoredSessions is the core guarantee after the
// orphan-purge (y/n) prompt was removed (issue #3495): a refresh that finds
// stored sessions absent from tmux MUST leave sessions.json fully intact. Dead
// entries are retained (still listed and restartable) and are never silently
// pruned — this is also the #3470 regression guard, since an empty live list
// must not wipe the store.
func TestRefreshSessions_RetainsDeadStoredSessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-3495-retain")
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))
	// Two stored sessions whose tmux sessions are NOT live on the socket.
	if err := store.Add(SessionMeta{Name: "dead1", TmuxSession: "vibeflow_dead1"}); err != nil {
		t.Fatal(err)
	}
	if err := store.Add(SessionMeta{Name: "dead2", TmuxSession: "vibeflow_dead2"}); err != nil {
		t.Fatal(err)
	}

	m := Model{
		tmux:   tm,
		store:  store,
		cache:  NewSessionCacheWithPath(filepath.Join(dir, "cache.json")),
		logger: NewLogger(),
		config: &Config{},
	}
	msg := m.refreshSessions()
	sm, ok := msg.(sessionsMsg)
	if !ok {
		t.Fatalf("refreshSessions returned %T, want sessionsMsg", msg)
	}
	if sm.err != nil {
		t.Fatalf("refreshSessions err: %v", sm.err)
	}

	// Both dead entries must survive the refresh — never purged, no prompt.
	sessions, err := store.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 2 {
		t.Fatalf("dead stored sessions must be retained across a refresh: got %d, want 2", len(sessions))
	}
}

// TestRefreshSessions_DeadRetainedAlongsideLive proves a live session is listed
// as a row while a dead stored session is retained (not purged) in the same
// refresh: exactly the live session renders, and the dead entry stays in the
// store for a later restart.
func TestRefreshSessions_DeadRetainedAlongsideLive(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-3495-mixed")
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))

	// One real live session, recorded in the store.
	if err := tm.CreateSessionWithOpts(SessionOpts{
		Name: "live", Provider: "claude", WorkDir: dir, Command: "sleep 300",
	}); err != nil {
		t.Fatalf("create live session: %v", err)
	}
	if err := store.Add(SessionMeta{
		Name: "live", TmuxSession: tm.FullSessionName("claude", "live"), Provider: "claude", WorkingDir: dir,
	}); err != nil {
		t.Fatal(err)
	}
	// One dead stored session with no live tmux counterpart.
	if err := store.Add(SessionMeta{Name: "dead", TmuxSession: "vibeflow_dead"}); err != nil {
		t.Fatal(err)
	}

	m := Model{
		tmux:   tm,
		store:  store,
		cache:  NewSessionCacheWithPath(filepath.Join(dir, "cache.json")),
		logger: NewLogger(),
		config: &Config{},
	}
	msg := m.refreshSessions()
	sm, ok := msg.(sessionsMsg)
	if !ok {
		t.Fatalf("refreshSessions returned %T, want sessionsMsg", msg)
	}
	if sm.err != nil {
		t.Fatalf("refreshSessions err: %v", sm.err)
	}

	// Only the live session renders as a row — dead sessions are not listed as
	// rows (they live in the store, not in tmux), but they are NOT purged.
	if len(sm.sessions) != 1 {
		t.Fatalf("expected exactly the 1 live session as a row, got %d: %+v", len(sm.sessions), sm.sessions)
	}
	if _, ok, _ := store.Get("dead"); !ok {
		t.Error("dead stored session must be retained after refresh, but it was purged")
	}
	if _, ok, _ := store.Get("live"); !ok {
		t.Error("live stored session must remain in the store after refresh")
	}
}
