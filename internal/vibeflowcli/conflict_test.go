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
)

func TestSessionFileForPersona(t *testing.T) {
	tests := []struct {
		persona  string
		expected string
	}{
		{"", ".vibeflow-session"},
		{"developer", ".vibeflow-session-developer"},
		{"architect", ".vibeflow-session-architect"},
		{"product_manager", ".vibeflow-session-product_manager"},
	}
	for _, tt := range tests {
		t.Run(tt.persona, func(t *testing.T) {
			got := sessionFileForPersona(tt.persona)
			if got != tt.expected {
				t.Errorf("sessionFileForPersona(%q) = %q, want %q", tt.persona, got, tt.expected)
			}
		})
	}
}

func TestWriteSessionFile_WithPersona(t *testing.T) {
	dir := t.TempDir()

	// Write a session file for "developer" persona.
	if err := WriteSessionFile(dir, "developer", "session-123"); err != nil {
		t.Fatal(err)
	}

	// Verify the correct file was created.
	data, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session-developer"))
	if err != nil {
		t.Fatalf("expected .vibeflow-session-developer to exist: %v", err)
	}
	if string(data) != "session-123\n" {
		t.Errorf("unexpected content: %q", string(data))
	}

	// Verify the legacy file was NOT created.
	if _, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session")); !os.IsNotExist(err) {
		t.Error("legacy .vibeflow-session should not exist when persona is specified")
	}
}

func TestWriteSessionFile_EmptyPersona(t *testing.T) {
	dir := t.TempDir()

	// Empty persona should use legacy filename.
	if err := WriteSessionFile(dir, "", "session-456"); err != nil {
		t.Fatal(err)
	}

	data, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session"))
	if err != nil {
		t.Fatalf("expected .vibeflow-session to exist: %v", err)
	}
	if string(data) != "session-456\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestReadSessionFileID_WithPersona(t *testing.T) {
	dir := t.TempDir()

	// Write persona-specific file.
	_ = os.WriteFile(filepath.Join(dir, ".vibeflow-session-architect"), []byte("session-abc\nprovider=codex\n"), 0600)

	// Read with matching persona.
	sid, prov, _ := readSessionFileID(dir, "architect")
	if sid != "session-abc" {
		t.Errorf("expected session-abc, got %q", sid)
	}
	if prov != "codex" {
		t.Errorf("expected codex, got %q", prov)
	}

	// Read with different persona — should find nothing.
	sid2, _, _ := readSessionFileID(dir, "developer")
	if sid2 != "" {
		t.Errorf("expected empty for developer persona, got %q", sid2)
	}
}

func TestWriteSessionFileIfNeeded_Idempotent(t *testing.T) {
	dir := t.TempDir()

	// First write.
	if err := WriteSessionFileIfNeeded(dir, "developer", "session-xyz"); err != nil {
		t.Fatal(err)
	}

	// Second write with same ID — should be a no-op.
	if err := WriteSessionFileIfNeeded(dir, "developer", "session-xyz"); err != nil {
		t.Fatal(err)
	}

	data, _ := os.ReadFile(filepath.Join(dir, ".vibeflow-session-developer"))
	if string(data) != "session-xyz\n" {
		t.Errorf("unexpected content: %q", string(data))
	}
}

func TestRemoveSessionFile_WithPersona(t *testing.T) {
	dir := t.TempDir()

	// Write files for two personas.
	_ = WriteSessionFile(dir, "developer", "session-1")
	_ = WriteSessionFile(dir, "architect", "session-2")

	// Remove only the developer file.
	RemoveSessionFile(dir, "developer")

	// Developer file should be gone.
	if _, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session-developer")); !os.IsNotExist(err) {
		t.Error("developer session file should have been removed")
	}

	// Architect file should still exist.
	if _, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session-architect")); err != nil {
		t.Error("architect session file should still exist")
	}
}

func TestCheckConflict_PersonaIsolation(t *testing.T) {
	dir := t.TempDir()

	// Write a session file for "developer" persona.
	_ = WriteSessionFile(dir, "developer", "session-dev-123")

	// Check conflict for "developer" — should find it (stale since no tmux).
	result := CheckConflict(dir, "developer", nil)
	if result.Status == NoConflict {
		t.Error("expected conflict for developer persona")
	}
	if result.SessionID != "session-dev-123" {
		t.Errorf("expected session-dev-123, got %q", result.SessionID)
	}
	if result.Persona != "developer" {
		t.Errorf("expected persona developer, got %q", result.Persona)
	}

	// Check conflict for "architect" — should find nothing.
	result2 := CheckConflict(dir, "architect", nil)
	if result2.Status != NoConflict {
		t.Errorf("expected no conflict for architect, got %s", result2.Status)
	}
}

func TestCheckAllSessions(t *testing.T) {
	dir := t.TempDir()

	// Write files for multiple personas.
	_ = WriteSessionFile(dir, "developer", "session-dev-1")
	_ = WriteSessionFile(dir, "architect", "session-arch-2")
	_ = WriteSessionFile(dir, "", "session-legacy-3") // legacy

	results := CheckAllSessions(dir, nil)
	if len(results) != 3 {
		t.Fatalf("expected 3 results, got %d", len(results))
	}

	// Verify all sessions are found (order may vary based on readdir).
	personas := map[string]bool{}
	for _, r := range results {
		personas[r.Persona] = true
	}
	if !personas["developer"] {
		t.Error("missing developer persona")
	}
	if !personas["architect"] {
		t.Error("missing architect persona")
	}
	if !personas[""] {
		t.Error("missing legacy (empty) persona")
	}
}

func TestCleanupStaleSession_WithPersona(t *testing.T) {
	dir := t.TempDir()

	_ = WriteSessionFile(dir, "qa_lead", "session-qa-1")

	if err := CleanupStaleSession(dir, "qa_lead"); err != nil {
		t.Fatal(err)
	}

	if _, err := os.ReadFile(filepath.Join(dir, ".vibeflow-session-qa_lead")); !os.IsNotExist(err) {
		t.Error("session file should have been removed")
	}
}

func TestParseSessionFile_WithPersonaLine(t *testing.T) {
	content := "session-20260226-143000-abc12345\nprovider=gemini\npersona=developer\n"
	sid, prov, _ := parseSessionFile(content)
	if sid != "session-20260226-143000-abc12345" {
		t.Errorf("unexpected session ID: %q", sid)
	}
	if prov != "gemini" {
		t.Errorf("unexpected provider: %q", prov)
	}
	// persona= is in the content but not returned by parseSessionFile
	// (persona is determined by filename suffix)
}
