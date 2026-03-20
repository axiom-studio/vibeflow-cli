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
	"os"
	"path/filepath"
	"testing"
	"time"
)

func testCache(t *testing.T) *SessionCache {
	t.Helper()
	return NewSessionCacheWithPath(filepath.Join(t.TempDir(), "session_cache.json"))
}

func TestSessionCache_ListEmpty(t *testing.T) {
	c := testCache(t)
	entries, err := c.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty list, got %d entries", len(entries))
	}
}

func TestSessionCache_AddAndList(t *testing.T) {
	c := testCache(t)

	meta := SessionMeta{
		Name:            "test-session",
		TmuxSession:     "vibeflow_claude-test-session",
		Provider:        "claude",
		Project:         "my-project",
		Persona:         "developer",
		Branch:          "main",
		WorkingDir:      "/tmp/work",
		SessionType:     "vibeflow",
		SkipPermissions: true,
		CreatedAt:       time.Now(),
	}

	if err := c.Add(meta); err != nil {
		t.Fatalf("Add failed: %v", err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatalf("List failed: %v", err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	if entries[0].Name != "test-session" {
		t.Errorf("Name = %q, want test-session", entries[0].Name)
	}
	if entries[0].SessionType != "vibeflow" {
		t.Errorf("SessionType = %q, want vibeflow", entries[0].SessionType)
	}
	if !entries[0].SkipPermissions {
		t.Error("SkipPermissions should be true")
	}
}

func TestSessionCache_AddReplacesExisting(t *testing.T) {
	c := testCache(t)

	meta1 := SessionMeta{Name: "session-a", Provider: "claude", TmuxSession: "vibeflow_claude-a"}
	meta2 := SessionMeta{Name: "session-a", Provider: "codex", TmuxSession: "vibeflow_codex-a"}

	if err := c.Add(meta1); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(meta2); err != nil {
		t.Fatal(err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry (replaced), got %d", len(entries))
	}
	if entries[0].Provider != "codex" {
		t.Errorf("expected replaced provider codex, got %q", entries[0].Provider)
	}
}

func TestSessionCache_Remove(t *testing.T) {
	c := testCache(t)

	if err := c.Add(SessionMeta{Name: "keep", TmuxSession: "vibeflow_keep"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(SessionMeta{Name: "remove", TmuxSession: "vibeflow_remove"}); err != nil {
		t.Fatal(err)
	}

	if err := c.Remove("remove"); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after remove, got %d", len(entries))
	}
	if entries[0].Name != "keep" {
		t.Errorf("expected keep, got %q", entries[0].Name)
	}
}

func TestSessionCache_GC(t *testing.T) {
	c := testCache(t)

	if err := c.Add(SessionMeta{Name: "alive", TmuxSession: "vibeflow_alive"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(SessionMeta{Name: "dead", TmuxSession: "vibeflow_dead"}); err != nil {
		t.Fatal(err)
	}

	if err := c.GC([]string{"vibeflow_alive"}); err != nil {
		t.Fatalf("GC failed: %v", err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry after GC, got %d", len(entries))
	}
	if entries[0].Name != "alive" {
		t.Errorf("expected alive, got %q", entries[0].Name)
	}
}

func TestSessionCache_GCEmptyActive(t *testing.T) {
	c := testCache(t)

	if err := c.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_a"}); err != nil {
		t.Fatal(err)
	}

	if err := c.GC(nil); err != nil {
		t.Fatalf("GC failed: %v", err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("expected 0 entries after GC with no active, got %d", len(entries))
	}
}

func TestSessionCache_DeadSessions(t *testing.T) {
	c := testCache(t)

	if err := c.Add(SessionMeta{Name: "alive", TmuxSession: "vibeflow_alive", Provider: "claude"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(SessionMeta{Name: "dead1", TmuxSession: "vibeflow_dead1", Provider: "codex"}); err != nil {
		t.Fatal(err)
	}
	if err := c.Add(SessionMeta{Name: "dead2", TmuxSession: "vibeflow_dead2", Provider: "gemini"}); err != nil {
		t.Fatal(err)
	}

	dead, err := c.DeadSessions([]string{"vibeflow_alive"})
	if err != nil {
		t.Fatalf("DeadSessions failed: %v", err)
	}
	if len(dead) != 2 {
		t.Fatalf("expected 2 dead sessions, got %d", len(dead))
	}

	names := map[string]bool{}
	for _, d := range dead {
		names[d.Name] = true
	}
	if !names["dead1"] || !names["dead2"] {
		t.Errorf("expected dead1 and dead2, got %v", dead)
	}
}

func TestSessionCache_DeadSessionsAllAlive(t *testing.T) {
	c := testCache(t)

	if err := c.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_a"}); err != nil {
		t.Fatal(err)
	}

	dead, err := c.DeadSessions([]string{"vibeflow_a"})
	if err != nil {
		t.Fatal(err)
	}
	if len(dead) != 0 {
		t.Errorf("expected 0 dead sessions, got %d", len(dead))
	}
}

func TestSessionCache_ReadFileEmptyFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_cache.json")
	if err := os.WriteFile(path, []byte(""), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewSessionCacheWithPath(path)

	entries, err := c.List()
	if err != nil {
		t.Fatalf("unexpected error on empty file: %v", err)
	}
	if len(entries) != 0 {
		t.Errorf("expected empty list from empty file, got %d", len(entries))
	}
}

func TestSessionCache_ReadFileInvalidJSON(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "session_cache.json")
	if err := os.WriteFile(path, []byte("{invalid json"), 0600); err != nil {
		t.Fatal(err)
	}
	c := NewSessionCacheWithPath(path)

	_, err := c.List()
	if err == nil {
		t.Fatal("expected error on invalid JSON")
	}
}

func TestSessionCache_PersistsNewFields(t *testing.T) {
	c := testCache(t)

	meta := SessionMeta{
		Name:              "full",
		TmuxSession:       "vibeflow_claude-full",
		Provider:          "claude",
		Project:           "project",
		Persona:           "architect",
		Branch:            "feature",
		WorkingDir:        "/work",
		SessionType:       "vibeflow",
		SkipPermissions:   true,
		LLMGatewayEnabled: true,
		VibeFlowSessionID: "session-123",
		CreatedAt:         time.Now(),
	}
	if err := c.Add(meta); err != nil {
		t.Fatal(err)
	}

	entries, err := c.List()
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(entries))
	}
	got := entries[0]
	if got.SessionType != "vibeflow" {
		t.Errorf("SessionType = %q", got.SessionType)
	}
	if !got.SkipPermissions {
		t.Error("SkipPermissions should be true")
	}
	if !got.LLMGatewayEnabled {
		t.Error("LLMGatewayEnabled should be true")
	}
	if got.Persona != "architect" {
		t.Errorf("Persona = %q", got.Persona)
	}
}
