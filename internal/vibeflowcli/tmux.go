package vibeflowcli

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"text/template"
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
	Name      string            // Short session name (without prefix).
	Provider  string            // Provider key (e.g. "claude", "codex").
	WorkDir   string            // Working directory for the session.
	Command   string            // Resolved launch command.
	Env       map[string]string // Provider-specific environment variables.
	Branch    string            // Git branch for status bar display.
	Project   string            // Project name for status bar display.
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
	Binary          string // Resolved binary path (absolute or bare name).
}

// RenderLaunchCommand renders a provider's LaunchTemplate with the given vars.
// If the template is empty, the provider's binary name is returned as-is.
func RenderLaunchCommand(tmpl string, vars LaunchTemplateVars) (string, error) {
	if tmpl == "" {
		return "", nil
	}
	t, err := template.New("launch").Parse(tmpl)
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
	// Keep dead panes alive so the user can see why the agent command
	// exited. Without this, sessions whose command exits immediately
	// are destroyed and disappear from the session list.
	_, _ = tm.run("set", "-g", "remain-on-exit", "on")
	return nil
}

// ListSessions returns all vibeflow-prefixed tmux sessions.
func (tm *TmuxManager) ListSessions() ([]TmuxSession, error) {
	out, err := tm.run("list-sessions", "-F", "#{session_name}\t#{session_id}\t#{session_windows}\t#{session_attached}\t#{session_created_string}\t#{pane_dead}")
	if err != nil {
		// tmux writes error messages to combined output; err.Error() is just "exit status 1".
		combined := out + " " + err.Error()
		if strings.Contains(combined, "no server running") || strings.Contains(combined, "no sessions") {
			return nil, nil
		}
		return nil, fmt.Errorf("list sessions: %w", err)
	}

	var sessions []TmuxSession
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if line == "" {
			continue
		}
		parts := strings.SplitN(line, "\t", 6)
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
	return sessions, nil
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

// CreateSessionWithOpts creates a tmux session with provider-specific
// options including environment variables and provider-prefixed naming.
func (tm *TmuxManager) CreateSessionWithOpts(opts SessionOpts) error {
	fullName := tm.FullSessionName(opts.Provider, opts.Name)

	// If a tmux session with the same name already exists (e.g. stale session
	// from a previous attempt that reused the same .vibeflow-session ID), kill
	// it before creating a fresh one.
	if tm.HasSession(fullName) {
		_, _ = tm.run("kill-session", "-t", fullName)
	}

	args := []string{"new-session", "-d", "-s", fullName, "-c", opts.WorkDir}

	// Set environment variables via tmux -e flags.
	for k, v := range opts.Env {
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
		// Redact env var values to avoid leaking secrets.
		var redacted []string
		for _, a := range fullArgs {
			if strings.HasPrefix(a, "GEMINI_API_KEY=") || strings.HasPrefix(a, "MCP_TOKEN=") || strings.HasPrefix(a, "VIBEFLOW_TOKEN=") {
				parts := strings.SplitN(a, "=", 2)
				redacted = append(redacted, parts[0]+"=<redacted>")
			} else {
				redacted = append(redacted, a)
			}
		}
		tm.logger.Debug("spawn %s: %s", opts.Provider, strings.Join(redacted, " "))
		tm.logger.Info("spawn session %q provider=%s workdir=%s command=%q", fullName, opts.Provider, opts.WorkDir, opts.Command)
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

// ConfigureStatusBar sets up a vibeflow-themed tmux status bar for a session.
// All settings are scoped per-session via set-option -t so they don't leak
// to other tmux sessions on the same server.
func (tm *TmuxManager) ConfigureStatusBar(sessionName string, opts StatusBarOpts) error {
	provider := opts.Provider
	if provider == "" {
		provider = "agent"
	}
	branch := opts.Branch
	if branch == "" {
		branch = "main"
	}

	// Build status-left: [vibeflow] provider | branch
	statusLeft := fmt.Sprintf(
		"#[fg=#1a1b26,bg=#00d4aa,bold] vibeflow #[fg=#00d4aa,bg=#2a2e3f,nobold] %s #[fg=#555555]|#[fg=#a9b1d6] %s ",
		provider, branch,
	)

	// Build status-right: shortcuts + project
	project := opts.Project
	if project == "" {
		project = "default"
	}
	statusRight := fmt.Sprintf(
		"#[fg=#555555]C-q:#[fg=#a9b1d6]Menu #[fg=#555555]|#[fg=#555555] C-\\:#[fg=#a9b1d6]Menu #[fg=#555555]| #[fg=#00d4aa]%s ",
		project,
	)

	settings := map[string]string{
		"status":              "on",
		"status-style":        "fg=#a9b1d6,bg=#1a1b26",
		"status-left":         statusLeft,
		"status-right":        statusRight,
		"status-left-length":  "60",
		"status-right-length": "60",
	}

	for key, val := range settings {
		if _, err := tm.run("set-option", "-t", sessionName, key, val); err != nil {
			return fmt.Errorf("set %s for session %q: %w", key, sessionName, err)
		}
	}
	return nil
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
