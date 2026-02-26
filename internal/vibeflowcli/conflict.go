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
)

// ConflictStatus indicates whether a session conflict exists in a directory.
type ConflictStatus int

const (
	// NoConflict means no session file was found for the given persona.
	NoConflict ConflictStatus = iota
	// ActiveConflict means a session file exists and the corresponding
	// tmux session is still running.
	ActiveConflict
	// StaleConflict means a session file exists but the tmux session
	// is no longer running.
	StaleConflict
	// ExternalConflict means a session file exists but has no tmux_session
	// info — it was likely written by a vanilla agent session (e.g. claude
	// or codex run directly in a terminal without vibeflow-cli). The session
	// may still be running outside of tmux management.
	ExternalConflict
)

// String returns a human-readable label for a ConflictStatus.
func (cs ConflictStatus) String() string {
	switch cs {
	case NoConflict:
		return "none"
	case ActiveConflict:
		return "active"
	case StaleConflict:
		return "stale"
	case ExternalConflict:
		return "external"
	default:
		return "unknown"
	}
}

// ConflictResult holds the outcome of a conflict check.
type ConflictResult struct {
	Status      ConflictStatus
	SessionID   string // Vibeflow session ID from the file.
	Persona     string // Persona from filename suffix or file content.
	Provider    string // Parsed from extended format, defaults to "claude".
	TmuxSession string // Full tmux session name (e.g. "vibeflow_claude-session-xxx").
	FilePath    string // Full path to the session file.
}

// sessionFileForPersona returns the session filename for the given persona.
// Empty persona uses the legacy ".vibeflow-session" name (for vanilla sessions).
// Non-empty persona uses ".vibeflow-session-{persona}" (for vibeflow sessions).
func sessionFileForPersona(persona string) string {
	if persona == "" {
		return ".vibeflow-session"
	}
	return ".vibeflow-session-" + persona
}

// CheckConflict reads the persona-specific session file in dir and determines
// whether another session is actively using the directory for this persona.
//
// The function is side-effect-free — the caller decides how to handle
// the result (e.g., show a modal, auto-cleanup, etc.).
func CheckConflict(dir, persona string, tmux *TmuxManager) ConflictResult {
	fp := filepath.Join(dir, sessionFileForPersona(persona))

	data, err := os.ReadFile(fp)
	if err != nil {
		return ConflictResult{Status: NoConflict}
	}

	content := strings.TrimSpace(string(data))
	if content == "" {
		return ConflictResult{Status: NoConflict}
	}

	sessionID, provider, tmuxSession := parseSessionFile(content)
	if sessionID == "" {
		return ConflictResult{Status: NoConflict}
	}

	result := ConflictResult{
		SessionID:   sessionID,
		Persona:     persona,
		Provider:    provider,
		TmuxSession: tmuxSession,
		FilePath:    fp,
	}

	// Determine conflict type.
	//
	// If the file contains an explicit tmux_session= line (old format), use
	// it directly. Otherwise search running vibeflow tmux sessions for one
	// whose name contains this session ID.
	if tmuxSession == "" && tmux != nil {
		if found := tmux.FindSessionBySessionID(sessionID); found != "" {
			tmuxSession = found
			result.TmuxSession = found
			// Extract provider from tmux name (vibeflow_{provider}-{name}).
			if after, ok := strings.CutPrefix(found, sessionPrefix); ok {
				if idx := strings.Index(after, "-"); idx > 0 {
					result.Provider = after[:idx]
				}
			}
		}
	}

	if tmuxSession != "" && tmux != nil && tmux.HasSession(tmuxSession) {
		result.Status = ActiveConflict
	} else {
		result.Status = StaleConflict
	}

	return result
}

// CheckAllSessions scans a directory for all .vibeflow-session* files and
// returns a ConflictResult for each one. This is used by the TUI to show
// coexisting sessions from different personas as informational.
func CheckAllSessions(dir string, tmux *TmuxManager) []ConflictResult {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}

	const prefix = ".vibeflow-session"
	var results []ConflictResult
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, prefix) || e.IsDir() {
			continue
		}

		// Extract persona from suffix: ".vibeflow-session-developer" → "developer"
		persona := ""
		if len(name) > len(prefix) && name[len(prefix)] == '-' {
			persona = name[len(prefix)+1:]
		}

		result := CheckConflict(dir, persona, tmux)
		if result.Status != NoConflict {
			results = append(results, result)
		}
	}
	return results
}

// CleanupStaleSession removes the persona-specific session file from dir.
// Call this after confirming the session is stale (no active tmux session).
func CleanupStaleSession(dir, persona string) error {
	return os.Remove(filepath.Join(dir, sessionFileForPersona(persona)))
}

// WriteSessionFile writes a persona-specific session file to dir containing
// only the bare session ID. Coding agents read this file to obtain their
// session ID, so no additional metadata (provider, tmux name) is stored here.
func WriteSessionFile(dir, persona, sessionID string) error {
	return os.WriteFile(filepath.Join(dir, sessionFileForPersona(persona)), []byte(sessionID+"\n"), 0600)
}

// WriteSessionFileIfNeeded writes the session file only when the file does not
// already contain the given session ID. This prevents unnecessary overwrites
// that could race with a coding agent reading the file.
func WriteSessionFileIfNeeded(dir, persona, sessionID string) error {
	existing, _, _ := readSessionFileID(dir, persona)
	if existing == sessionID {
		return nil // file already contains the correct session ID
	}
	return WriteSessionFile(dir, persona, sessionID)
}

// RemoveSessionFile removes the persona-specific session file from dir.
// This is a no-op if the file doesn't exist.
func RemoveSessionFile(dir, persona string) {
	_ = os.Remove(filepath.Join(dir, sessionFileForPersona(persona)))
}

// readSessionFileID reads the persona-specific session file from dir and
// returns the session ID, provider, and tmux session name if the file exists
// and contains a valid session ID. Returns empty strings if the file is missing
// or invalid.
func readSessionFileID(dir, persona string) (sessionID, provider, tmuxSession string) {
	data, err := os.ReadFile(filepath.Join(dir, sessionFileForPersona(persona)))
	if err != nil {
		return "", "", ""
	}
	content := strings.TrimSpace(string(data))
	if content == "" {
		return "", "", ""
	}
	return parseSessionFile(content)
}

// parseSessionFile parses the .vibeflow-session content.
//
// Supported formats:
//
//	session-20260224-052842-a35d47a1                                    (single line — provider defaults to "claude")
//	session-20260224-052842-a35d47a1\nprovider=codex                   (extended with provider)
//	session-20260224-052842-a35d47a1\ntmux_session=vibeflow_claude-... (extended with full tmux name)
//	session-20260224-052842-a35d47a1\npersona=developer                (extended with persona)
func parseSessionFile(content string) (sessionID, provider, tmuxSession string) {
	provider = "claude" // default for backwards compatibility

	lines := strings.Split(content, "\n")
	if len(lines) == 0 {
		return "", provider, ""
	}

	sessionID = strings.TrimSpace(lines[0])
	if !strings.HasPrefix(sessionID, "session-") {
		return "", provider, ""
	}

	for _, line := range lines[1:] {
		kv := strings.TrimSpace(line)
		if strings.HasPrefix(kv, "provider=") {
			if v := strings.TrimPrefix(kv, "provider="); v != "" {
				provider = v
			}
		} else if strings.HasPrefix(kv, "tmux_session=") {
			if v := strings.TrimPrefix(kv, "tmux_session="); v != "" {
				tmuxSession = v
			}
		}
		// persona= is parsed but not returned here — the persona is
		// determined by the filename suffix, not the file content.
	}

	return sessionID, provider, tmuxSession
}
