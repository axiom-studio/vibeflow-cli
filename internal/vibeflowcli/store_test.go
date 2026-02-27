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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestNewStore(t *testing.T) {
	s := NewStore()
	if s.path == "" {
		t.Error("NewStore should set a non-empty path")
	}
}

func TestNewStoreWithPath(t *testing.T) {
	s := NewStoreWithPath("/custom/path.json")
	if s.path != "/custom/path.json" {
		t.Errorf("path = %q, want /custom/path.json", s.path)
	}
}

func testStore(t *testing.T) *Store {
	t.Helper()
	return NewStoreWithPath(filepath.Join(t.TempDir(), "sessions.json"))
}

func TestStore_ListEmpty(t *testing.T) {
	s := testStore(t)
	sessions, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list, got %d entries", len(sessions))
	}
}

func TestStore_AddAndList(t *testing.T) {
	s := testStore(t)

	meta := SessionMeta{
		Name:        "test-session",
		TmuxSession: "vibeflow_test-session",
		Provider:    "claude",
		Project:     "my-project",
		Branch:      "main",
		WorkingDir:  "/tmp/work",
		CreatedAt:   time.Now(),
	}

	if err := s.Add(meta); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].Name != "test-session" {
		t.Errorf("Name = %q, want test-session", sessions[0].Name)
	}
	if sessions[0].Provider != "claude" {
		t.Errorf("Provider = %q, want claude", sessions[0].Provider)
	}
}

func TestStore_AddReplacesExisting(t *testing.T) {
	s := testStore(t)

	meta1 := SessionMeta{Name: "session-a", Provider: "claude", TmuxSession: "vibeflow_a"}
	meta2 := SessionMeta{Name: "session-a", Provider: "codex", TmuxSession: "vibeflow_a_new"}

	if err := s.Add(meta1); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(meta2); err != nil {
		t.Fatal(err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session (replaced), got %d", len(sessions))
	}
	if sessions[0].Provider != "codex" {
		t.Errorf("expected replaced provider codex, got %q", sessions[0].Provider)
	}
}

func TestStore_Get(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "alpha", Provider: "claude", TmuxSession: "vibeflow_alpha"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(SessionMeta{Name: "beta", Provider: "codex", TmuxSession: "vibeflow_beta"}); err != nil {
		t.Fatal(err)
	}

	t.Run("found", func(t *testing.T) {
		meta, found, err := s.Get("alpha")
		if err != nil {
			t.Fatal(err)
		}
		if !found {
			t.Fatal("expected alpha to be found")
		}
		if meta.Provider != "claude" {
			t.Errorf("Provider = %q, want claude", meta.Provider)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, found, err := s.Get("nonexistent")
		if err != nil {
			t.Fatal(err)
		}
		if found {
			t.Error("expected not found")
		}
	})
}

func TestStore_Remove(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "to-remove", TmuxSession: "vibeflow_remove"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(SessionMeta{Name: "to-keep", TmuxSession: "vibeflow_keep"}); err != nil {
		t.Fatal(err)
	}

	if err := s.Remove("to-remove"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after remove, got %d", len(sessions))
	}
	if sessions[0].Name != "to-keep" {
		t.Errorf("expected to-keep, got %q", sessions[0].Name)
	}
}

func TestStore_RemoveNonexistent(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "exists", TmuxSession: "vibeflow_exists"}); err != nil {
		t.Fatal(err)
	}

	// Remove a name that doesn't exist — should be a no-op.
	if err := s.Remove("nope"); err != nil {
		t.Fatalf("Remove of nonexistent should not error: %v", err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Errorf("expected 1 session unchanged, got %d", len(sessions))
	}
}

func TestStore_Sync(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "alive", TmuxSession: "vibeflow_alive"}); err != nil {
		t.Fatal(err)
	}
	if err := s.Add(SessionMeta{Name: "dead", TmuxSession: "vibeflow_dead"}); err != nil {
		t.Fatal(err)
	}

	// Only vibeflow_alive is still active in tmux.
	if err := s.Sync([]string{"vibeflow_alive", "vibeflow_other"}); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session after sync, got %d", len(sessions))
	}
	if sessions[0].Name != "alive" {
		t.Errorf("expected alive, got %q", sessions[0].Name)
	}
}

func TestStore_SyncEmptyActive(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_a"}); err != nil {
		t.Fatal(err)
	}

	// No active tmux sessions — should remove all.
	if err := s.Sync(nil); err != nil {
		t.Fatalf("Sync failed: %v", err)
	}

	sessions, err := s.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected 0 sessions after sync with empty active, got %d", len(sessions))
	}
}

func TestStore_Discover(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "known", TmuxSession: "vibeflow_known"}); err != nil {
		t.Fatal(err)
	}

	discovered := s.Discover([]string{"vibeflow_known", "vibeflow_orphan", "vibeflow_new"})
	if len(discovered) != 2 {
		t.Fatalf("expected 2 discovered, got %d: %v", len(discovered), discovered)
	}

	// Should include orphan and new but not known.
	hasOrphan, hasNew := false, false
	for _, d := range discovered {
		if d == "vibeflow_orphan" {
			hasOrphan = true
		}
		if d == "vibeflow_new" {
			hasNew = true
		}
	}
	if !hasOrphan || !hasNew {
		t.Errorf("expected orphan and new in discovered, got %v", discovered)
	}
}

func TestStore_DiscoverEmptyStore(t *testing.T) {
	s := testStore(t)

	discovered := s.Discover([]string{"vibeflow_a", "vibeflow_b"})
	if len(discovered) != 2 {
		t.Errorf("expected 2 discovered from empty store, got %d", len(discovered))
	}
}

func TestStore_DiscoverNoLive(t *testing.T) {
	s := testStore(t)

	if err := s.Add(SessionMeta{Name: "known", TmuxSession: "vibeflow_known"}); err != nil {
		t.Fatal(err)
	}

	discovered := s.Discover(nil)
	if len(discovered) != 0 {
		t.Errorf("expected 0 discovered with no live sessions, got %d", len(discovered))
	}
}

func TestStore_ReadFileEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithPath(path)

	sessions, err := s.List()
	if err != nil {
		t.Fatalf("unexpected error on empty file: %v", err)
	}
	if len(sessions) != 0 {
		t.Errorf("expected empty list from empty file, got %d", len(sessions))
	}
}

func TestStore_ReadFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sessions.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	s := NewStoreWithPath(path)

	_, err := s.List()
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestStore_WriteFileJSON(t *testing.T) {
	s := testStore(t)

	meta := SessionMeta{
		Name:        "json-test",
		TmuxSession: "vibeflow_json-test",
		Provider:    "gemini",
		Persona:     "architect",
	}
	if err := s.Add(meta); err != nil {
		t.Fatal(err)
	}

	// Read the raw file and verify it's valid indented JSON.
	data, err := os.ReadFile(s.path)
	if err != nil {
		t.Fatal(err)
	}
	var parsed []SessionMeta
	if err := json.Unmarshal(data, &parsed); err != nil {
		t.Fatalf("stored file is not valid JSON: %v", err)
	}
	if len(parsed) != 1 {
		t.Fatalf("expected 1 entry in JSON, got %d", len(parsed))
	}
	if parsed[0].Persona != "architect" {
		t.Errorf("Persona = %q, want architect", parsed[0].Persona)
	}
}

func TestStore_SessionMetaFields(t *testing.T) {
	s := testStore(t)

	now := time.Now().Truncate(time.Millisecond)
	meta := SessionMeta{
		Name:              "full-meta",
		TmuxSession:       "vibeflow_full-meta",
		Provider:          "codex",
		Project:           "my-project",
		Persona:           "developer",
		Branch:            "feature-branch",
		WorktreePath:      "/worktree/path",
		WorkingDir:        "/work/dir",
		VibeFlowSessionID: "session-123",
		CreatedAt:         now,
	}
	if err := s.Add(meta); err != nil {
		t.Fatal(err)
	}

	got, found, err := s.Get("full-meta")
	if err != nil {
		t.Fatal(err)
	}
	if !found {
		t.Fatal("expected to find full-meta")
	}
	if got.TmuxSession != "vibeflow_full-meta" {
		t.Errorf("TmuxSession = %q", got.TmuxSession)
	}
	if got.Provider != "codex" {
		t.Errorf("Provider = %q", got.Provider)
	}
	if got.Project != "my-project" {
		t.Errorf("Project = %q", got.Project)
	}
	if got.Persona != "developer" {
		t.Errorf("Persona = %q", got.Persona)
	}
	if got.Branch != "feature-branch" {
		t.Errorf("Branch = %q", got.Branch)
	}
	if got.WorktreePath != "/worktree/path" {
		t.Errorf("WorktreePath = %q", got.WorktreePath)
	}
	if got.WorkingDir != "/work/dir" {
		t.Errorf("WorkingDir = %q", got.WorkingDir)
	}
	if got.VibeFlowSessionID != "session-123" {
		t.Errorf("VibeFlowSessionID = %q", got.VibeFlowSessionID)
	}
}
