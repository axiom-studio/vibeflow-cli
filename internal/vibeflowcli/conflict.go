package vibeflowcli

import (
	"os"
	"path/filepath"
	"strings"
)

// ConflictStatus indicates whether a session conflict exists in a directory.
type ConflictStatus int

const (
	// NoConflict means no .vibeflow-session file was found.
	NoConflict ConflictStatus = iota
	// ActiveConflict means a .vibeflow-session file exists and the
	// corresponding tmux session is still running.
	ActiveConflict
	// StaleConflict means a .vibeflow-session file exists but the
	// tmux session is no longer running.
	StaleConflict
	// ExternalConflict means a .vibeflow-session file exists but has no
	// tmux_session info — it was likely written by a vanilla agent session
	// (e.g. claude or codex run directly in a terminal without vibeflow-cli).
	// The session may still be running outside of tmux management.
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
	Provider    string // Parsed from extended format, defaults to "claude".
	TmuxSession string // Full tmux session name (e.g. "vibeflow_claude-session-xxx").
	FilePath    string // Full path to the .vibeflow-session file.
}

// sessionFileName is the well-known file used to mark an active session.
const sessionFileName = ".vibeflow-session"

// CheckConflict reads the .vibeflow-session file in dir and determines
// whether another session is actively using the directory.
//
// The function is side-effect-free — the caller decides how to handle
// the result (e.g., show a modal, auto-cleanup, etc.).
func CheckConflict(dir string, tmux *TmuxManager) ConflictResult {
	fp := filepath.Join(dir, sessionFileName)

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
		Provider:    provider,
		TmuxSession: tmuxSession,
		FilePath:    fp,
	}

	// Determine conflict type based on whether we have tmux session info.
	if tmuxSession == "" {
		// No tmux_session line — written by a vanilla agent session (not
		// managed by vibeflow-cli). The session may still be running.
		result.Status = ExternalConflict
	} else if tmux != nil && tmux.HasSession(tmuxSession) {
		result.Status = ActiveConflict
	} else {
		result.Status = StaleConflict
	}

	return result
}

// CleanupStaleSession removes the .vibeflow-session file from dir.
// Call this after confirming the session is stale (no active tmux session).
func CleanupStaleSession(dir string) error {
	return os.Remove(filepath.Join(dir, sessionFileName))
}

// WriteSessionFile writes a .vibeflow-session file to dir with the given
// session ID, provider, and full tmux session name.
func WriteSessionFile(dir, sessionID, provider, tmuxSession string) error {
	content := sessionID
	if provider != "" && provider != "claude" {
		content += "\nprovider=" + provider
	}
	if tmuxSession != "" {
		content += "\ntmux_session=" + tmuxSession
	}
	return os.WriteFile(filepath.Join(dir, sessionFileName), []byte(content+"\n"), 0600)
}

// RemoveSessionFile removes the .vibeflow-session file from dir.
// Unlike CleanupStaleSession this is a no-op if the file doesn't exist.
func RemoveSessionFile(dir string) {
	_ = os.Remove(filepath.Join(dir, sessionFileName))
}

// parseSessionFile parses the .vibeflow-session content.
//
// Supported formats:
//
//	session-20260224-052842-a35d47a1                                    (single line — provider defaults to "claude")
//	session-20260224-052842-a35d47a1\nprovider=codex                   (extended with provider)
//	session-20260224-052842-a35d47a1\ntmux_session=vibeflow_claude-... (extended with full tmux name)
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
	}

	return sessionID, provider, tmuxSession
}
