package vibeflowcli

import (
	"embed"
	"os"
	"path/filepath"
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

// EnsureAgentDoc checks whether the agent-specific markdown file exists in
// workDir. If missing, it writes the bundled template so the agent picks it
// up on startup. Returns the filename written (empty if already present or
// no mapping exists for the provider).
func EnsureAgentDoc(workDir, providerKey string) string {
	docFile, ok := providerDocFile[providerKey]
	if !ok {
		return ""
	}

	destPath := filepath.Join(workDir, docFile)
	if _, err := os.Stat(destPath); err == nil {
		return "" // already exists
	}

	data, err := agentDocsFS.ReadFile("agentdocs/" + docFile)
	if err != nil {
		return ""
	}

	if err := os.WriteFile(destPath, data, 0644); err != nil {
		return ""
	}

	return docFile
}
