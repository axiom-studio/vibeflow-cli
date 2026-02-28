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
	"embed"
	"os"
	"path/filepath"
	"strings"
)

// agentDocsFS embeds the agent markdown templates from agentdocs/.
// Source of truth: vibecoding-agent-docs/ at the repo root.
//
//go:embed agentdocs/*
var agentDocsFS embed.FS

// providerDocFile maps provider keys to the agent doc filename that each
// provider reads on startup.
var providerDocFile = map[string]string{
	"claude": "CLAUDE.md",
	"codex":  "AGENTS.md",
	"gemini": "GEMINI.md",
}

// vibeflowSectionMarker is the heading used to identify the vibeflow rules
// section in agent instruction files. All embedded templates use this heading.
const vibeflowSectionMarker = "## vibeflow Agent Session Rules"

// EnsureAllAgentDocs ensures all agent-specific markdown files (CLAUDE.md,
// AGENTS.md, GEMINI.md) exist in workDir with the vibeflow session rules
// section. This guarantees that any provider session started in the directory
// will find its instruction file, regardless of which provider was launched
// first.
//
// Returns the list of filenames that were created or updated.
func EnsureAllAgentDocs(workDir string) []string {
	var updated []string
	for providerKey := range providerDocFile {
		if docFile := EnsureAgentDoc(workDir, providerKey); docFile != "" {
			updated = append(updated, docFile)
		}
	}
	return updated
}

// EnsureAgentDoc ensures the agent-specific markdown file in workDir contains
// the vibeflow session rules section.
//
// If the file does not exist, the full bundled template is written.
// If the file exists but lacks the vibeflow section, the section is appended
// (preserving existing user content).
// If the file exists and already contains the vibeflow section, no changes
// are made.
//
// Returns the filename written/updated (empty if no changes or no mapping).
func EnsureAgentDoc(workDir, providerKey string) string {
	docFile, ok := providerDocFile[providerKey]
	if !ok {
		return ""
	}

	destPath := filepath.Join(workDir, docFile)
	template, err := agentDocsFS.ReadFile("agentdocs/" + docFile)
	if err != nil {
		return ""
	}

	existing, readErr := os.ReadFile(destPath)
	if readErr != nil {
		// File doesn't exist — write the full template.
		if err := os.WriteFile(destPath, template, 0644); err != nil {
			return ""
		}
		return docFile
	}

	// File exists — check if vibeflow section is already present.
	content := string(existing)
	bundledSection := extractVibeflowSection(string(template))
	if strings.Contains(content, vibeflowSectionMarker) {
		// Section exists — check if it matches the bundled version.
		installedSection := extractVibeflowSection(content)
		if installedSection == bundledSection {
			return "" // already up to date
		}
		// Stale section — replace it while preserving user content above.
		if bundledSection == "" {
			return ""
		}
		markerIdx := strings.Index(content, vibeflowSectionMarker)
		userContent := strings.TrimRight(content[:markerIdx], "\n")
		content = userContent + "\n\n" + bundledSection + "\n"
		if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
			return ""
		}
		return docFile
	}

	// Extract the vibeflow section from the template and append it.
	if bundledSection == "" {
		return "" // template has no vibeflow section (shouldn't happen)
	}

	content = strings.TrimRight(content, "\n") + "\n\n" + bundledSection + "\n"
	if err := os.WriteFile(destPath, []byte(content), 0644); err != nil {
		return ""
	}
	return docFile
}

// extractVibeflowSection returns the vibeflow rules section from a template
// string, starting from the vibeflowSectionMarker heading to the end.
func extractVibeflowSection(template string) string {
	idx := strings.Index(template, vibeflowSectionMarker)
	if idx < 0 {
		return ""
	}
	return strings.TrimRight(template[idx:], "\n")
}
