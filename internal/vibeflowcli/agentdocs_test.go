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
	"strings"
	"testing"
)

func TestExtractVibeflowSection(t *testing.T) {
	t.Run("extracts section from template with preceding content", func(t *testing.T) {
		template := "# Header\n\nSome content.\n\n## vibeflow Agent Session Rules\n\nRule 1.\nRule 2.\n"
		got := extractVibeflowSection(template)
		if !strings.HasPrefix(got, vibeflowSectionMarker) {
			t.Errorf("expected section to start with marker, got: %q", got)
		}
		if !strings.Contains(got, "Rule 1.") {
			t.Error("expected section to contain rules")
		}
		// Should not include content before the marker.
		if strings.Contains(got, "# Header") {
			t.Error("section should not include content before the marker")
		}
	})

	t.Run("returns empty for template without marker", func(t *testing.T) {
		got := extractVibeflowSection("# Just a header\n\nNo vibeflow here.\n")
		if got != "" {
			t.Errorf("expected empty string, got: %q", got)
		}
	})

	t.Run("trims trailing newlines", func(t *testing.T) {
		template := "## vibeflow Agent Session Rules\n\nContent.\n\n\n"
		got := extractVibeflowSection(template)
		if strings.HasSuffix(got, "\n") {
			t.Errorf("expected trailing newlines to be trimmed, got: %q", got)
		}
	})
}

func TestEnsureAgentDoc_UnknownProvider(t *testing.T) {
	dir := t.TempDir()
	got := EnsureAgentDoc(dir, "unknown-provider")
	if got != "" {
		t.Errorf("expected empty for unknown provider, got: %q", got)
	}
}

func TestEnsureAgentDoc_FileDoesNotExist(t *testing.T) {
	dir := t.TempDir()

	got := EnsureAgentDoc(dir, "claude")
	if got != "CLAUDE.md" {
		t.Fatalf("expected CLAUDE.md, got: %q", got)
	}

	// Verify the file was created with the full template content.
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatalf("failed to read created file: %v", err)
	}
	content := string(data)
	if !strings.Contains(content, vibeflowSectionMarker) {
		t.Error("created file should contain vibeflow section marker")
	}
}

func TestEnsureAgentDoc_FileExistsWithVibeflowSection(t *testing.T) {
	dir := t.TempDir()

	// Write a file with user content + the current bundled vibeflow section
	// (matching content = no update needed).
	template, _ := agentDocsFS.ReadFile("agentdocs/CLAUDE.md")
	bundledSection := extractVibeflowSection(string(template))
	existing := "# My Project\n\n" + bundledSection + "\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	got := EnsureAgentDoc(dir, "claude")
	if got != "" {
		t.Errorf("expected empty (no-op for matching section), got: %q", got)
	}

	// Verify file was NOT modified.
	data, _ := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if string(data) != existing {
		t.Error("file should not have been modified when section matches bundled")
	}
}

func TestEnsureAgentDoc_FileExistsWithoutVibeflowSection(t *testing.T) {
	dir := t.TempDir()

	// Write a file without the vibeflow section.
	existing := "# My Project Rules\n\nDo not delete production data.\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	got := EnsureAgentDoc(dir, "claude")
	if got != "CLAUDE.md" {
		t.Fatalf("expected CLAUDE.md (updated), got: %q", got)
	}

	// Verify the file now contains both the original content and the vibeflow section.
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if !strings.Contains(content, "# My Project Rules") {
		t.Error("original content should be preserved")
	}
	if !strings.Contains(content, "Do not delete production data.") {
		t.Error("original content should be preserved")
	}
	if !strings.Contains(content, vibeflowSectionMarker) {
		t.Error("vibeflow section should have been appended")
	}

	// Verify the vibeflow section appears AFTER the original content.
	markerIdx := strings.Index(content, vibeflowSectionMarker)
	originalIdx := strings.Index(content, "# My Project Rules")
	if markerIdx <= originalIdx {
		t.Error("vibeflow section should appear after original content")
	}
}

func TestEnsureAgentDoc_AllProviders(t *testing.T) {
	for provider, expectedFile := range providerDocFile {
		t.Run(provider, func(t *testing.T) {
			dir := t.TempDir()

			got := EnsureAgentDoc(dir, provider)
			if got != expectedFile {
				t.Errorf("expected %s, got: %q", expectedFile, got)
			}

			data, err := os.ReadFile(filepath.Join(dir, expectedFile))
			if err != nil {
				t.Fatalf("failed to read %s: %v", expectedFile, err)
			}
			if !strings.Contains(string(data), vibeflowSectionMarker) {
				t.Errorf("%s should contain vibeflow section marker", expectedFile)
			}
		})
	}
}

func TestEnsureAgentDoc_IdempotentOnSecondCall(t *testing.T) {
	dir := t.TempDir()

	// First call: creates the file.
	first := EnsureAgentDoc(dir, "codex")
	if first != "AGENTS.md" {
		t.Fatalf("expected AGENTS.md on first call, got: %q", first)
	}

	// Read content after first call.
	data1, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))

	// Second call: should be a no-op (file already has vibeflow section).
	second := EnsureAgentDoc(dir, "codex")
	if second != "" {
		t.Errorf("expected empty on second call (idempotent), got: %q", second)
	}

	// Content should be unchanged.
	data2, _ := os.ReadFile(filepath.Join(dir, "AGENTS.md"))
	if string(data1) != string(data2) {
		t.Error("file should not change on second call")
	}
}

func TestEnsureAgentDoc_UpdatesStaleSection(t *testing.T) {
	dir := t.TempDir()

	// Write a file with an outdated vibeflow section.
	stale := "# My Project\n\nCustom rules here.\n\n## vibeflow Agent Session Rules\n\nOld stale rules from v1.\n"
	if err := os.WriteFile(filepath.Join(dir, "CLAUDE.md"), []byte(stale), 0644); err != nil {
		t.Fatal(err)
	}

	got := EnsureAgentDoc(dir, "claude")
	if got != "CLAUDE.md" {
		t.Fatalf("expected CLAUDE.md (updated stale section), got: %q", got)
	}

	// Verify the file was updated.
	data, err := os.ReadFile(filepath.Join(dir, "CLAUDE.md"))
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)

	// User content before the marker should be preserved.
	if !strings.Contains(content, "# My Project") {
		t.Error("user content before marker should be preserved")
	}
	if !strings.Contains(content, "Custom rules here.") {
		t.Error("user content before marker should be preserved")
	}

	// Old stale content should be gone.
	if strings.Contains(content, "Old stale rules from v1.") {
		t.Error("stale vibeflow section should have been replaced")
	}

	// New bundled section should be present.
	if !strings.Contains(content, vibeflowSectionMarker) {
		t.Error("updated file should contain vibeflow section marker")
	}

	// Read the bundled template to compare the section.
	template, _ := agentDocsFS.ReadFile("agentdocs/CLAUDE.md")
	bundledSection := extractVibeflowSection(string(template))
	installedSection := extractVibeflowSection(content)
	if installedSection != bundledSection {
		t.Error("installed section should match bundled section after update")
	}
}

func TestEnsureAgentDoc_AppendThenIdempotent(t *testing.T) {
	dir := t.TempDir()

	// Write existing content without vibeflow section.
	existing := "# Custom Rules\n\nMy custom agent rules.\n"
	if err := os.WriteFile(filepath.Join(dir, "GEMINI.md"), []byte(existing), 0644); err != nil {
		t.Fatal(err)
	}

	// First call: appends vibeflow section.
	first := EnsureAgentDoc(dir, "gemini")
	if first != "GEMINI.md" {
		t.Fatalf("expected GEMINI.md on first call, got: %q", first)
	}

	data1, _ := os.ReadFile(filepath.Join(dir, "GEMINI.md"))

	// Second call: should be a no-op (vibeflow section now present).
	second := EnsureAgentDoc(dir, "gemini")
	if second != "" {
		t.Errorf("expected empty on second call, got: %q", second)
	}

	data2, _ := os.ReadFile(filepath.Join(dir, "GEMINI.md"))
	if string(data1) != string(data2) {
		t.Error("file should not change on second call after append")
	}
}
