package vibeflowcli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vibeflow-cli/sessionid"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Colors for the vibeflow theme.
var (
	accentColor  = lipgloss.Color("#00d4aa")
	dimColor     = lipgloss.Color("#555555")
	errorColor   = lipgloss.Color("#ff5555")
	warningColor = lipgloss.Color("#ffaa00")

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(accentColor).
			MarginBottom(1)

	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(lipgloss.Color("#ffffff")).
			Background(lipgloss.Color("#333333"))

	statusRunning = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00"))
	statusIdle    = lipgloss.NewStyle().Foreground(dimColor)
	statusWaiting = lipgloss.NewStyle().Foreground(warningColor)
	statusError   = lipgloss.NewStyle().Foreground(errorColor)

	helpStyle = lipgloss.NewStyle().Foreground(dimColor)

	asciiBanner = accentColor
	copyrightStyle = lipgloss.NewStyle().Foreground(dimColor)
)

// bannerText is the 3D ASCII art for "VibeFlow" displayed on TUI startup.
const bannerText = `
 __      __ _  _            _____  _
 \ \    / /(_)| |          |  ___|| |
  \ \  / /  _ | |__    ___ | |_   | |  ___  __      __
   \ \/ /  | ||  _ \  / _ \|  _|  | | / _ \ \ \ /\ / /
    \  /   | || |_) ||  __/| |    | || (_) | \ V  V /
     \/    |_||_.__/  \___||_|    |_| \___/   \_/\_/`

const copyrightText = "by axiomstudio.ai | Copyright 2026"

// SessionRow represents a session displayed in the TUI.
type SessionRow struct {
	Name          string
	Project       string
	Persona       string
	Provider      string
	Branch        string
	WorktreePath  string
	WorkingDir    string
	Status        string
	CurrentWork   string
	LastHeartbeat time.Time
	TmuxAttached  bool
	Recovered     bool
}

// ViewState controls which sub-view is active.
type ViewState int

const (
	ViewSessions ViewState = iota
	ViewWizard
	ViewConflict
	ViewWorktrees
	ViewHelp
)

// Model is the Bubble Tea model for vibeflow-cli.
type Model struct {
	sessions   []SessionRow
	cursor     int
	client     *Client
	tmux       *TmuxManager
	worktrees  *WorktreeManager
	store      *Store
	registry   *ProviderRegistry
	config     *Config
	width      int
	height     int
	err        error
	quitting   bool
	projectID  int64
	activeView     ViewState
	wizard         WizardModel
	conflictModal  ConflictModal
	worktreeList   WorktreeListModel
	pendingWizard  *WizardResult // wizard result waiting for conflict resolution
	captureOutput  string        // last captured pane output for selected session
	captureName    string        // tmux session name for current capture
	confirmDelete  bool          // showing delete confirmation
	confirmQuit    bool          // showing quit confirmation
	confirmDetach  bool          // showing detach confirmation
	serverWarning  string        // non-empty if server unreachable at startup
	healthMonitor  *HealthMonitor // session error detection and auto-recovery
	logger         *Logger       // file-based logger

	// Grouped view state.
	groupMode       bool              // true = grouped by repo root, false = flat
	repoRootCache   map[string]string // workingDir → repo root cache
	collapsedGroups map[string]bool   // repo root → collapsed state
	groupOrder      []string          // ordered list of repo roots
	groupedSessions map[string][]int  // repo root → indices into m.sessions
}

// NewModel creates a new TUI model.
func NewModel(cfg *Config, client *Client, tmux *TmuxManager, worktrees *WorktreeManager, store *Store, registry *ProviderRegistry, projectID int64) Model {
	logger := NewLogger()
	logger.Info("vibeflow-cli started (server=%s, project=%s)", cfg.ServerURL, cfg.DefaultProject)
	errorRegistry := NewErrorPatternRegistry()
	healthMonitor := NewHealthMonitor(errorRegistry, tmux, cfg.ErrorRecovery, logger)
	return Model{
		config:          cfg,
		client:          client,
		tmux:            tmux,
		worktrees:       worktrees,
		store:           store,
		registry:        registry,
		projectID:       projectID,
		activeView:      ViewSessions,
		logger:          logger,
		healthMonitor:   healthMonitor,
		groupMode:       cfg.ViewMode == "grouped",
		repoRootCache:   make(map[string]string),
		collapsedGroups: make(map[string]bool),
	}
}

// attachExitMsg is sent when a tmux attach-session process exits.
type attachExitMsg struct{ err error }

// tickMsg triggers periodic refresh.
type tickMsg time.Time

// sessionsMsg carries refreshed session data.
type sessionsMsg struct {
	sessions []SessionRow
	err      error
}

// errClearMsg clears the displayed error after a delay.
type errClearMsg struct{}

// captureTickMsg triggers periodic capture-pane refresh.
type captureTickMsg time.Time

// captureMsg carries captured pane output.
type captureMsg struct {
	name   string
	output string
}

func tickCmd(d time.Duration) tea.Cmd {
	return tea.Tick(d, func(t time.Time) tea.Msg {
		return tickMsg(t)
	})
}

func captureTickCmd() tea.Cmd {
	return tea.Tick(3*time.Second, func(t time.Time) tea.Msg {
		return captureTickMsg(t)
	})
}

func (m Model) refreshCapture() tea.Msg {
	idx := m.selectedSessionIdx()
	if idx < 0 {
		return captureMsg{}
	}
	name := m.sessions[idx].Name
	output, err := m.tmux.CapturePaneOutput(name, 20)
	if err != nil {
		return captureMsg{name: name, output: "(no output)"}
	}
	if strings.TrimSpace(output) == "" {
		return captureMsg{name: name, output: "(no output yet)"}
	}
	return captureMsg{name: name, output: stripANSI(output)}
}

func (m Model) refreshSessions() tea.Msg {
	var rows []SessionRow

	// Get tmux sessions
	tmuxSessions, err := m.tmux.ListSessions()
	if err != nil {
		return sessionsMsg{err: err}
	}

	// Re-bind vibeflow keys to ensure persistence across tmux reloads.
	m.tmux.BindAllSessionKeys()

	// Sync store with live tmux sessions to clean up orphans.
	tmuxNames := make([]string, len(tmuxSessions))
	for i, ts := range tmuxSessions {
		tmuxNames[i] = ts.Name
	}
	if m.store != nil {
		_ = m.store.Sync(tmuxNames)
	}

	// Discover orphaned sessions (live in tmux but not in store) and
	// reconstruct their metadata from tmux state.
	recoveredNames := make(map[string]bool)
	if m.store != nil {
		discovered := m.store.Discover(tmuxNames)
		for _, tmuxName := range discovered {
			provider := ParseSessionProvider(tmuxName)
			workDir := m.tmux.GetPaneWorkDir(tmuxName)
			branch := GetGitBranch(workDir)
			shortName := strings.TrimPrefix(tmuxName, sessionPrefix)
			_ = m.store.Add(SessionMeta{
				Name:        shortName,
				TmuxSession: tmuxName,
				Provider:    provider,
				Branch:      branch,
				WorkingDir:  workDir,
				CreatedAt:   time.Now(),
			})
			recoveredNames[tmuxName] = true
		}
	}

	// Build a lookup from tmux session name → store metadata.
	storeMeta := make(map[string]SessionMeta)
	if m.store != nil {
		if metas, err := m.store.List(); err == nil {
			for _, meta := range metas {
				storeMeta[meta.TmuxSession] = meta
			}
		}
	}

	for _, ts := range tmuxSessions {
		shortName := strings.TrimPrefix(ts.Name, sessionPrefix)
		row := SessionRow{
			Name:         shortName,
			Status:       sessionStatus(ts.Attached),
			TmuxAttached: ts.Attached,
		}
		// Enrich with store metadata (provider, branch, worktree, persona).
		if meta, ok := storeMeta[ts.Name]; ok {
			row.Provider = meta.Provider
			row.Branch = meta.Branch
			row.WorktreePath = meta.WorktreePath
			row.Project = meta.Project
			row.Persona = meta.Persona
			row.WorkingDir = meta.WorkingDir
		}
		if recoveredNames[ts.Name] {
			row.Recovered = true
		}
		rows = append(rows, row)
	}

	// Enrich with VibeFlow API data if available.
	// Match API sessions by VibeFlowSessionID from the store, since API
	// session IDs (e.g. "session-20260224-...") differ from tmux names.
	if m.client != nil && m.projectID > 0 {
		// Build vibeflow session ID → row index map from store metadata.
		vfIDToRow := make(map[string]int)
		for i, ts := range tmuxSessions {
			if meta, ok := storeMeta[ts.Name]; ok && meta.VibeFlowSessionID != "" {
				vfIDToRow[meta.VibeFlowSessionID] = i
			}
		}

		apiSessions, err := m.client.ListSessions(m.projectID)
		if err == nil {
			for _, s := range apiSessions {
				if idx, ok := vfIDToRow[s.ID]; ok {
					rows[idx].LastHeartbeat = s.LastHeartbeat
					if rows[idx].Project == "" {
						rows[idx].Project = fmt.Sprintf("Project %d", s.ProjectID)
					}
				}
			}
		}
	}

	return sessionsMsg{sessions: rows}
}

func sessionStatus(attached bool) string {
	// All live tmux sessions are "running" — the process inside the pane is
	// active regardless of whether a client is viewing it. "attached" is
	// shown separately in the detail panel.
	if attached {
		return "attached"
	}
	return "running"
}

// getRepoRoot returns the git repository root for a directory, using a cache
// to avoid repeated git calls. Returns the directory itself if not a git repo.
func (m *Model) getRepoRoot(dir string) string {
	if dir == "" {
		return ""
	}
	if root, ok := m.repoRootCache[dir]; ok {
		return root
	}
	root := dir // fallback
	if out, err := exec.Command("git", "-C", dir, "rev-parse", "--show-toplevel").Output(); err == nil {
		root = strings.TrimSpace(string(out))
	}
	m.repoRootCache[dir] = root
	return root
}

// buildGroups rebuilds the grouped session data from the current flat session list.
func (m *Model) buildGroups() {
	m.groupedSessions = make(map[string][]int)
	seen := make(map[string]bool)
	m.groupOrder = nil

	for i, s := range m.sessions {
		root := m.getRepoRoot(s.WorkingDir)
		if root == "" {
			root = "(unknown)"
		}
		m.groupedSessions[root] = append(m.groupedSessions[root], i)
		if !seen[root] {
			m.groupOrder = append(m.groupOrder, root)
			seen[root] = true
		}
	}
}

// groupedListLen returns the total number of navigable items in grouped mode
// (group headers + visible session items).
func (m Model) groupedListLen() int {
	count := 0
	for _, root := range m.groupOrder {
		count++ // group header
		if !m.collapsedGroups[root] {
			count += len(m.groupedSessions[root])
		}
	}
	return count
}

// groupedCursorToSession maps the flat grouped-cursor position to a session
// index, or -1 if the cursor is on a group header. Also returns the group root.
func (m Model) groupedCursorToSession() (sessionIdx int, groupRoot string) {
	pos := 0
	for _, root := range m.groupOrder {
		if pos == m.cursor {
			return -1, root // cursor is on group header
		}
		pos++
		if !m.collapsedGroups[root] {
			for _, idx := range m.groupedSessions[root] {
				if pos == m.cursor {
					return idx, root
				}
				pos++
			}
		}
	}
	return -1, ""
}

// selectedSessionIdx returns the flat session index for the current cursor
// position, accounting for grouped mode. Returns -1 if no session is selected
// (e.g. cursor is on a group header or sessions list is empty).
func (m Model) selectedSessionIdx() int {
	if len(m.sessions) == 0 {
		return -1
	}
	if m.groupMode {
		idx, _ := m.groupedCursorToSession()
		return idx
	}
	if m.cursor >= len(m.sessions) {
		return -1
	}
	return m.cursor
}

// Init initializes the model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.refreshSessions,
		captureTickCmd(),
		tickCmd(time.Duration(m.config.PollInterval)*time.Second),
	)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global handlers — process regardless of active view so ticks and
	// session refreshes continue while sub-views (wizard, conflict modal,
	// worktree list) are active.
	switch msg := msg.(type) {
	case tickMsg:
		return m, tea.Batch(
			m.refreshSessions,
			tickCmd(time.Duration(m.config.PollInterval)*time.Second),
		)
	case sessionsMsg:
		m.err = msg.err
		if msg.err != nil {
			m.logger.Error("sessions: %v", msg.err)
			// Auto-clear error after 10 seconds.
			return m, tea.Tick(10*time.Second, func(time.Time) tea.Msg { return errClearMsg{} })
		}
		m.sessions = msg.sessions
		m.buildGroups()
		maxIdx := len(m.sessions) - 1
		if m.groupMode {
			maxIdx = m.groupedListLen() - 1
		}
		if m.cursor > maxIdx && maxIdx >= 0 {
			m.cursor = maxIdx
		}
		return m, nil
	case errClearMsg:
		m.err = nil
		return m, nil
	case captureTickMsg:
		return m, tea.Batch(m.refreshCapture, captureTickCmd())
	case captureMsg:
		m.captureOutput = msg.output
		m.captureName = msg.name
		// Health monitoring: scan capture output for error patterns.
		if m.healthMonitor != nil && msg.name != "" && msg.output != "" {
			provider := ""
			isAttached := false
			for _, s := range m.sessions {
				if s.Name == msg.name {
					provider = s.Provider
					isAttached = s.TmuxAttached
					break
				}
			}
			if shouldRecover := m.healthMonitor.CheckOutput(msg.name, provider, msg.output, isAttached); shouldRecover {
				_ = m.healthMonitor.AttemptRecovery(msg.name)
			}
		}
		return m, nil
	case attachExitMsg:
		// tmux attach exited — refresh sessions to pick up status changes.
		return m, m.refreshSessions
	case autoAttachMsg:
		// Auto-attach to a newly created session.
		cmd := m.tmux.AttachSessionCmd(msg.name)
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			return attachExitMsg{err: err}
		})
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		// Fall through so sub-views can also handle resize if needed.
	}

	// Delegate to sub-views if active.
	switch m.activeView {
	case ViewWizard:
		return m.updateWizard(msg)
	case ViewConflict:
		return m.updateConflict(msg)
	case ViewWorktrees:
		return m.updateWorktreeList(msg)
	case ViewHelp:
		// Any keypress closes the help popup.
		if _, ok := msg.(tea.KeyMsg); ok {
			m.activeView = ViewSessions
			return m, nil
		}
	}

	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Handle confirmation dialogs first.
		if m.confirmDelete {
			switch msg.String() {
			case "y":
				m.confirmDelete = false
				// Resolve the session to delete (grouped mode may differ from flat).
				delIdx := m.cursor
				if m.groupMode {
					delIdx, _ = m.groupedCursorToSession()
				}
				if delIdx >= 0 && delIdx < len(m.sessions) {
					row := m.sessions[delIdx]
					if err := m.tmux.KillSession(row.Name); err != nil {
						m.logger.Error("kill session %s: %v", row.Name, err)
					} else {
						m.logger.Info("session killed: %s", row.Name)
					}
					if m.store != nil {
						if meta, found, _ := m.store.Get(row.Name); found {
							RemoveSessionFile(meta.WorkingDir)
							if meta.WorktreePath != "" && m.worktrees != nil && m.config.Worktree.CleanupOnKill == "always" {
								_ = m.worktrees.Remove(meta.WorktreePath, true)
							}
						}
						_ = m.store.Remove(row.Name)
					} else {
						RemoveSessionFile(".")
					}
					return m, m.refreshSessions
				}
			default:
				m.confirmDelete = false
			}
			return m, nil
		}
		if m.confirmQuit {
			switch msg.String() {
			case "y":
				m.quitting = true
				return m, tea.Quit
			default:
				m.confirmQuit = false
			}
			return m, nil
		}
		if m.confirmDetach {
			switch msg.String() {
			case "y":
				m.confirmDetach = false
				m.quitting = true
				return m, tea.Quit
			default:
				m.confirmDetach = false
			}
			return m, nil
		}

		switch msg.String() {
		case "ctrl+c":
			m.quitting = true
			return m, tea.Quit
		case "q":
			if len(m.sessions) > 0 {
				m.confirmQuit = true
				return m, nil
			}
			m.quitting = true
			return m, tea.Quit
		case "up", "k":
			if m.cursor > 0 {
				m.cursor--
			}
		case "down", "j":
			maxIdx := len(m.sessions) - 1
			if m.groupMode {
				maxIdx = m.groupedListLen() - 1
			}
			if m.cursor < maxIdx {
				m.cursor++
			}
		case "enter":
			if m.groupMode {
				sessionIdx, groupRoot := m.groupedCursorToSession()
				if sessionIdx == -1 && groupRoot != "" {
					// Toggle group collapse.
					m.collapsedGroups[groupRoot] = !m.collapsedGroups[groupRoot]
					return m, nil
				}
				if sessionIdx >= 0 && sessionIdx < len(m.sessions) {
					cmd := m.tmux.AttachSessionCmd(m.sessions[sessionIdx].Name)
					return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
						return attachExitMsg{err: err}
					})
				}
			} else if m.cursor < len(m.sessions) {
				cmd := m.tmux.AttachSessionCmd(m.sessions[m.cursor].Name)
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return attachExitMsg{err: err}
				})
			}
		case "g":
			m.groupMode = !m.groupMode
			m.cursor = 0
			// Persist view mode to config.
			if m.groupMode {
				m.config.ViewMode = "grouped"
			} else {
				m.config.ViewMode = "flat"
			}
			_ = SaveConfig(m.config, ConfigPath())
			return m, nil
		case "n":
			repoRoot := "."
			if m.worktrees != nil {
				repoRoot = m.worktrees.RepoRoot()
			}
			m.wizard = NewWizardModel(m.registry, repoRoot, m.worktrees, m.client, m.config.DefaultProject, m.config.DirectoryHistory, m.config)
			m.activeView = ViewWizard
			return m, nil
		case "d":
			// In grouped mode, only allow delete when cursor is on a session, not a header.
			if m.groupMode {
				if idx, _ := m.groupedCursorToSession(); idx >= 0 {
					m.confirmDelete = true
				}
			} else if m.cursor < len(m.sessions) {
				m.confirmDelete = true
			}
			return m, nil
		case "r":
			// Manual recovery retry for failed sessions, otherwise refresh.
			idx := m.selectedSessionIdx()
			if idx >= 0 && idx < len(m.sessions) && m.healthMonitor != nil {
				if sh := m.healthMonitor.GetHealth(m.sessions[idx].Name); sh != nil && sh.Status == HealthFailed {
					m.healthMonitor.ResetSession(m.sessions[idx].Name)
					m.logger.Info("health: manual recovery reset for session %s", m.sessions[idx].Name)
					return m, nil
				}
			}
			return m, m.refreshSessions
		case "w":
			m.worktreeList = NewWorktreeListModel(m.worktrees, m.store)
			m.activeView = ViewWorktrees
			return m, nil
		case "?":
			m.activeView = ViewHelp
			return m, nil
		case "D":
			// Detach: quit TUI while sessions continue running.
			if len(m.sessions) > 0 {
				m.confirmDetach = true
			} else {
				m.quitting = true
				return m, tea.Quit
			}
			return m, nil
		}

	case conflictDetectedMsg:
		result := msg.wizardResult
		m.pendingWizard = &result
		m.conflictModal = NewConflictModal(msg.conflict)
		m.activeView = ViewConflict
		return m, nil
	}

	return m, nil
}

// updateWizard delegates to the wizard sub-model and handles completion.
func (m Model) updateWizard(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Allow global quit even in wizard.
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	w, cmd := m.wizard.Update(msg)
	m.wizard = w

	if m.wizard.Cancelled() {
		m.activeView = ViewSessions
		return m, nil
	}

	if m.wizard.Done() {
		result := m.wizard.Result()

		// Persist custom binary path to config and update registry.
		if result.CustomBinaryPath != "" {
			m.registry.SetBinary(result.ProviderKey, result.CustomBinaryPath)
			if p, ok := m.config.Providers[result.ProviderKey]; ok {
				p.Binary = result.CustomBinaryPath
				m.config.Providers[result.ProviderKey] = p
			}
			_ = SaveConfig(m.config, ConfigPath())
		}

		m.activeView = ViewSessions
		return m, func() tea.Msg { return m.launchFromWizard(result) }
	}

	return m, cmd
}

// updateConflict delegates to the conflict modal and handles the result.
func (m Model) updateConflict(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	cm, cmd := m.conflictModal.Update(msg)
	m.conflictModal = cm

	if !cm.Done() {
		return m, cmd
	}

	m.activeView = ViewSessions

	switch cm.Action() {
	case ConflictSwitch:
		// Attach to existing session using the full tmux session name.
		return m, func() tea.Msg {
			_ = m.tmux.AttachSession(cm.Conflict().TmuxSession)
			return m.refreshSessions()
		}
	case ConflictWorktree:
		// Re-run wizard result with forced worktree.
		if m.pendingWizard != nil {
			result := *m.pendingWizard
			result.WorktreeChoice = WorktreeNew
			m.pendingWizard = nil
			return m, func() tea.Msg { return m.executeLaunch(result) }
		}
	case ConflictCleanup:
		// Clean up stale/external session and proceed with launch.
		// Pass the old session ID for server-side reuse when the session
		// type is vibeflow (allows the server to resume the session).
		oldSessionID := cm.Conflict().SessionID
		// Use the directory from the conflict result (not CWD) to ensure
		// the correct .vibeflow-session file is removed.
		conflictDir := filepath.Dir(cm.Conflict().FilePath)
		_ = CleanupStaleSession(conflictDir)
		if m.pendingWizard != nil {
			result := *m.pendingWizard
			if result.SessionType == "vibeflow" && oldSessionID != "" {
				result.ReuseSessionID = oldSessionID
			}
			m.pendingWizard = nil
			return m, func() tea.Msg { return m.executeLaunch(result) }
		}
	case ConflictCancel:
		m.pendingWizard = nil
	}

	return m, nil
}

// updateWorktreeList delegates to the worktree list sub-model.
func (m Model) updateWorktreeList(msg tea.Msg) (tea.Model, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok && keyMsg.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}

	wl, cmd := m.worktreeList.Update(msg)
	m.worktreeList = wl

	if wl.Deleted() && m.worktrees != nil {
		_ = m.worktrees.Remove(wl.DeletedPath(), true)
		// Stay on worktrees view — rebuild list after deletion.
		m.worktreeList = NewWorktreeListModel(m.worktrees, m.store)
		return m, nil
	}

	if wl.Done() {
		m.activeView = ViewSessions
		return m, m.refreshSessions
	}

	return m, cmd
}

// launchFromWizard checks for conflicts and either launches or shows the conflict modal.
func (m Model) launchFromWizard(result WizardResult) tea.Msg {
	workDir := "."
	if result.WorkDir != "" {
		workDir = result.WorkDir
	}

	// Check for conflicts before launching (current-dir and specified-dir modes).
	switch result.WorktreeChoice {
	case WorktreeCurrent:
		conflict := CheckConflict(workDir, m.tmux)
		if conflict.Status != NoConflict {
			return conflictDetectedMsg{conflict: conflict, wizardResult: result}
		}
	case WorktreeSpecifyDir:
		if result.SpecifiedWorkDir != "" {
			conflict := CheckConflict(result.SpecifiedWorkDir, m.tmux)
			if conflict.Status != NoConflict {
				return conflictDetectedMsg{conflict: conflict, wizardResult: result}
			}
		}
	}

	return m.executeLaunch(result)
}

// conflictDetectedMsg triggers the conflict modal from within launchFromWizard.
type conflictDetectedMsg struct {
	conflict     ConflictResult
	wizardResult WizardResult
}

// autoAttachMsg signals that a newly created session should be auto-attached.
type autoAttachMsg struct{ name string }

// executeLaunch performs the actual session creation after conflict resolution.
func (m Model) executeLaunch(result WizardResult) tea.Msg {
	workDir := m.config.ResolveWorkDir("")
	if result.WorkDir != "" {
		workDir = result.WorkDir
	}
	name := sessionid.GenerateSessionID(workDir)
	provider := result.ProviderKey
	branch := result.Branch

	// Resolve the WorktreeManager to use — if the wizard selected a different
	// directory than the TUI's default, create a temporary manager for it.
	wm := m.worktrees
	if result.WorkDir != "" && (wm == nil || wm.RepoRoot() != result.WorkDir) {
		if newWM, err := NewWorktreeManager(result.WorkDir, m.config.Worktree.BaseDir); err == nil {
			wm = newWM
		}
	}

	// Handle worktree selection.
	var worktreePath string
	switch result.WorktreeChoice {
	case WorktreeNew:
		if wm != nil {
			wtName := result.WorktreeName
			if wtName == "" {
				wtName = fmt.Sprintf("%s-%s-%d", provider, branch, time.Now().Unix())
			}
			wtPath, wtErr := wm.CreateBranch(wtName, branch, result.NewBranch)
			if wtErr != nil {
				return sessionsMsg{err: fmt.Errorf("create worktree: %w", wtErr)}
			}
			workDir = wtPath
			worktreePath = wtPath
		}
	case WorktreeExisting:
		if result.ExistingWorktreePath != "" {
			workDir = result.ExistingWorktreePath
			worktreePath = result.ExistingWorktreePath
		}
	case WorktreeCustom:
		if wm != nil && result.CustomBaseDir != "" {
			wtName := result.WorktreeName
			if wtName == "" {
				wtName = fmt.Sprintf("%s-%s-%d", provider, branch, time.Now().Unix())
			}
			wtPath, wtErr := wm.CreateBranchInDir(result.CustomBaseDir, wtName, branch, result.NewBranch)
			if wtErr != nil {
				return sessionsMsg{err: fmt.Errorf("create worktree in custom dir: %w", wtErr)}
			}
			workDir = wtPath
			worktreePath = wtPath
			// Persist last-used custom dir for convenience.
			m.config.Worktree.LastCustomDir = result.CustomBaseDir
			_ = SaveConfig(m.config, ConfigPath())
		}
	case WorktreeSpecifyDir:
		if result.SpecifiedWorkDir != "" {
			workDir = result.SpecifiedWorkDir
		}
	}

	// For VibeFlow managed sessions, call session_init to get a server-
	// generated session ID and the autonomous agent prompt.
	var vibeflowSessionID string
	var vibeflowProjectID int64
	var agentPrompt string
	projectName := m.config.DefaultProject
	if result.SessionType == "vibeflow" && m.client != nil {
		if result.ProjectName != "" {
			projectName = result.ProjectName
		}
		// If no ReuseSessionID was set (e.g. no conflict modal), read
		// .vibeflow-session from the target workDir as a fallback so we
		// reuse the existing server session instead of creating a duplicate.
		reuseID := result.ReuseSessionID
		if reuseID == "" {
			if existingID, _, _ := readSessionFileID(workDir); existingID != "" {
				reuseID = existingID
				m.logger.Info("read existing session ID from .vibeflow-session: %s", existingID)
			}
		}
		initResult, initErr := m.client.SessionInit(SessionInitRequest{
			ProjectName:      projectName,
			SessionID:        reuseID,
			Persona:          result.Persona,
			GitBranch:        branch,
			WorkingDirectory: workDir,
			AgentType:        provider,
		})
		if initErr != nil {
			m.logger.Warn("session_init failed (falling back to local ID): %v", initErr)
		} else {
			vibeflowSessionID = initResult.SessionID
			vibeflowProjectID = initResult.ProjectID
			agentPrompt = initResult.Prompt
			// Use server-generated session ID as the local name so the format
			// matches vanilla Claude launches (session-YYYYMMDD-HHMMSS-hex).
			name = vibeflowSessionID
			m.logger.Info("session_init: got server session %s (project_id=%d, prompt_len=%d)", vibeflowSessionID, vibeflowProjectID, len(agentPrompt))
		}
	}

	// Inject vibeflow agent prompt BEFORE tmux session creation
	// so the agent reads it on first boot.
	if result.SessionType == "vibeflow" && agentPrompt != "" {
		if result.Provider.VibeFlowIntegrated {
			// Claude provider: append to CLAUDE.md (preserve existing content).
			injectClaudeMD(workDir, agentPrompt, projectName, result.Persona, vibeflowSessionID)
		} else {
			// Non-Claude providers: write .vibeflow-prompt file.
			promptPath := filepath.Join(workDir, ".vibeflow-prompt")
			_ = os.WriteFile(promptPath, []byte(agentPrompt), 0600)
		}
	}

	// Render launch command.
	var command string
	cmd, err := RenderLaunchCommand(result.Provider.LaunchTemplate, LaunchTemplateVars{
		WorkDir:         workDir,
		ServerURL:       m.config.ServerURL,
		SessionID:       vibeflowSessionID,
		SkipPermissions: result.SkipPermissions,
		Binary:          result.Provider.Binary,
	})
	if err == nil && cmd != "" {
		command = cmd
	} else {
		command = result.Provider.Binary
	}

	// For vibeflow sessions with integrated providers (e.g. Claude), pass the
	// init prompt via the CLI's -p flag so the agent starts autonomously.
	if result.SessionType == "vibeflow" && vibeflowSessionID != "" && result.Provider.VibeFlowIntegrated {
		initPrompt := fmt.Sprintf(
			"Initialize a vibeflow session for project %s with persona %q and follow the agent prompt.",
			projectName, result.Persona,
		)
		escaped := strings.ReplaceAll(initPrompt, "'", "'\\''")
		command += fmt.Sprintf(" -p '%s'", escaped)
	}

	// Merge wizard-resolved env vars (e.g. codex bearer token) into provider env.
	if result.EnvVars != nil {
		if result.Provider.Env == nil {
			result.Provider.Env = make(map[string]string)
		}
		for k, v := range result.EnvVars {
			result.Provider.Env[k] = v
		}
	}

	err = m.tmux.CreateSessionWithOpts(SessionOpts{
		Name:     name,
		Provider: provider,
		WorkDir:  workDir,
		Command:  command,
		Env:      result.Provider.Env,
		Branch:   branch,
		Project:  projectName,
	})
	if err != nil {
		m.logger.Error("create session (provider=%s, workdir=%s): %v", provider, workDir, err)
		return sessionsMsg{err: err}
	}

	// Compute full tmux name for session file and metadata.
	tmuxName := m.tmux.FullSessionName(provider, name)

	// Verify the session was actually created.
	if !m.tmux.HasSession(tmuxName) {
		m.logger.Error("session %q not verified by has-session after create", tmuxName)
		return sessionsMsg{err: fmt.Errorf("session %q was not created — tmux has-session check failed", tmuxName)}
	}
	m.logger.Info("session created: %s (provider=%s, workdir=%s, command=%q)", tmuxName, provider, workDir, command)

	// Bind Ctrl+Q to open vibeflow TUI popup inside the tmux session.
	if bindErr := m.tmux.BindSessionKeys(tmuxName); bindErr != nil {
		m.logger.Warn("bind session keys for %s: %v", tmuxName, bindErr)
	}

	// Write session file — use server session ID if available.
	// Only write if the file doesn't already contain this session ID.
	sessionFileID := name
	if vibeflowSessionID != "" {
		sessionFileID = vibeflowSessionID
	}
	if result.Provider.SessionFile != "" {
		_ = WriteSessionFileIfNeeded(workDir, sessionFileID)
	}

	// Register session with vibeflow server (best-effort).
	if vibeflowSessionID != "" && m.client != nil {
		regErr := m.client.SessionRegister(SessionRegisterRequest{
			SessionID:        vibeflowSessionID,
			ProjectID:        vibeflowProjectID,
			WorkingDirectory: workDir,
			GitBranch:        branch,
			GitWorktreePath:  worktreePath,
		})
		if regErr != nil {
			m.logger.Warn("session_register failed: %v", regErr)
		}
	}

	// Persist metadata.
	if m.store != nil {
		_ = m.store.Add(SessionMeta{
			Name:              name,
			TmuxSession:       tmuxName,
			Provider:          provider,
			Project:           projectName,
			Persona:           result.Persona,
			Branch:            branch,
			WorktreePath:      worktreePath,
			WorkingDir:        workDir,
			VibeFlowSessionID: vibeflowSessionID,
			CreatedAt:         time.Now(),
		})
	}

	// Save working directory to history for quick access in future sessions.
	if result.WorkDir != "" {
		m.config.AddDirectoryToHistory(result.WorkDir)
		_ = SaveConfig(m.config, ConfigPath())
	}

	// Stay in the TUI — refresh the session list so the new session appears.
	// The user can attach later via Enter key.
	return m.refreshSessions()
}

// vibeflowSection is the marker used to delimit vibeflow-injected content in CLAUDE.md.
const vibeflowSection = "<!-- vibeflow-agent-prompt -->"

// injectClaudeMD appends the vibeflow agent prompt to the CLAUDE.md file in dir.
// If the file already contains a vibeflow section, it is replaced. Existing
// user content outside the section markers is preserved.
func injectClaudeMD(dir, prompt, project, persona, sessionID string) {
	mdPath := filepath.Join(dir, "CLAUDE.md")

	section := fmt.Sprintf(
		"%s\n# VibeFlow Agent Session\n\n"+
			"- **Project**: %s\n"+
			"- **Persona**: %s\n"+
			"- **Session ID**: %s\n\n"+
			"%s\n%s\n",
		vibeflowSection, project, persona, sessionID, prompt, vibeflowSection,
	)

	existing, err := os.ReadFile(mdPath)
	if err != nil {
		// No existing CLAUDE.md — write fresh.
		_ = os.WriteFile(mdPath, []byte(section), 0600)
		return
	}

	content := string(existing)
	// Replace existing vibeflow section if present.
	if idx := strings.Index(content, vibeflowSection); idx >= 0 {
		endIdx := strings.Index(content[idx+len(vibeflowSection):], vibeflowSection)
		if endIdx >= 0 {
			// Remove old section (including both markers).
			end := idx + len(vibeflowSection) + endIdx + len(vibeflowSection)
			content = content[:idx] + content[end:]
		}
	}

	// Append new section to preserved content.
	content = strings.TrimRight(content, "\n") + "\n\n" + section
	_ = os.WriteFile(mdPath, []byte(content), 0600)
}

func (m Model) createSession(_ tea.Msg) tea.Msg {
	workDir := m.config.ResolveWorkDir("")

	// Check for session conflicts before launching.
	conflict := CheckConflict(workDir, m.tmux)
	switch conflict.Status {
	case ActiveConflict:
		// If worktrees are available, auto-create a worktree instead of blocking.
		if m.worktrees != nil && m.config.Worktree.AutoCreate {
			// Fall through — worktree creation below will give us a clean dir.
		} else {
			return sessionsMsg{err: fmt.Errorf("active session conflict in %s (session %s, provider %s) — switch to it or use a worktree",
				workDir, conflict.SessionID, conflict.Provider)}
		}
	case ExternalConflict:
		// External session (not managed by TUI) — treat as stale for non-wizard path.
		_ = CleanupStaleSession(workDir)
	case StaleConflict:
		_ = CleanupStaleSession(workDir)
	}

	name := sessionid.GenerateSessionID(workDir)

	// Use default provider from config.
	provider := m.config.DefaultProvider
	if provider == "" {
		provider = "claude"
	}

	// Create worktree if configured and available.
	var worktreePath string
	branch := "main" // default; wizard (Todo #344) will let user pick
	if m.worktrees != nil && m.config.Worktree.AutoCreate {
		wtName := fmt.Sprintf("%s-%s-%d", provider, branch, time.Now().Unix())
		wtPath, wtErr := m.worktrees.Create(wtName, branch)
		if wtErr == nil {
			workDir = wtPath
			worktreePath = wtPath
		} else {
			m.logger.Warn("worktree creation failed, using current dir: %v", wtErr)
		}
	}

	// Render launch command from provider config.
	var command string
	var provCfg Provider
	if p, ok := m.config.Providers[provider]; ok {
		provCfg = p
		cmd, err := RenderLaunchCommand(p.LaunchTemplate, LaunchTemplateVars{
			WorkDir:   workDir,
			ServerURL: m.config.ServerURL,
			Binary:    p.Binary,
		})
		if err == nil && cmd != "" {
			command = cmd
		} else {
			command = p.Binary
		}
	} else {
		command = fmt.Sprintf("%s --dangerously-skip-permissions", m.config.ClaudeBinary)
	}

	err := m.tmux.CreateSessionWithOpts(SessionOpts{
		Name:     name,
		Provider: provider,
		WorkDir:  workDir,
		Command:  command,
		Env:      provCfg.Env,
		Branch:   branch,
		Project:  m.config.DefaultProject,
	})
	if err != nil {
		m.logger.Error("create session (provider=%s, workdir=%s): %v", provider, workDir, err)
		return sessionsMsg{err: err}
	}

	// Compute full tmux name for session file and metadata.
	tmuxName := m.tmux.FullSessionName(provider, name)
	m.logger.Info("session created: %s (provider=%s, workdir=%s)", tmuxName, provider, workDir)

	// Write session file if the provider uses one.
	// Only write if the file doesn't already contain this session ID.
	if provCfg.SessionFile != "" {
		_ = WriteSessionFileIfNeeded(workDir, name)
	}

	// Persist session metadata to store.
	if m.store != nil {
		_ = m.store.Add(SessionMeta{
			Name:         name,
			TmuxSession:  tmuxName,
			Provider:     provider,
			Project:      m.config.DefaultProject,
			Branch:       branch,
			WorktreePath: worktreePath,
			WorkingDir:   workDir,
			CreatedAt:    time.Now(),
		})
	}

	return m.refreshSessions()
}

// View renders the TUI with a two-column layout: session list (left) and detail panel (right).
func (m Model) View() string {
	if m.quitting {
		return ""
	}

	// Delegate to sub-views if active.
	switch m.activeView {
	case ViewWizard:
		return m.wizard.View()
	case ViewConflict:
		return m.conflictModal.View()
	case ViewWorktrees:
		return m.worktreeList.View()
	case ViewHelp:
		return m.renderHelpPopup()
	}

	width := m.width
	if width < 40 {
		width = 80
	}
	height := m.height
	if height < 10 {
		height = 24
	}

	// ASCII banner.
	bannerStyle := lipgloss.NewStyle().Foreground(asciiBanner).Bold(true)
	title := bannerStyle.Render(bannerText) + "\n" + copyrightStyle.Render("  "+copyrightText)

	// Error/warning line (optional).
	var errLine string
	if m.err != nil {
		errMsg := m.err.Error()
		if len(errMsg) > 120 {
			errMsg = errMsg[:117] + "..."
		}
		errStyle := lipgloss.NewStyle().Foreground(errorColor)
		hintStyle := lipgloss.NewStyle().Foreground(dimColor)
		errLine = errStyle.Render("Error: "+errMsg) + "\n" +
			hintStyle.Render("  See ~/.vibeflow-cli/vibeflow-cli.log for details")
	} else if m.serverWarning != "" {
		warnBannerStyle := lipgloss.NewStyle().Foreground(warningColor)
		errLine = warnBannerStyle.Render("⚠ " + m.serverWarning + " — local sessions still available")
	}

	// Help bar — context-sensitive based on confirmation state.
	var helpBar string
	warnStyle := lipgloss.NewStyle().Foreground(warningColor)
	switch {
	case m.confirmDelete:
		delName := ""
		if m.groupMode {
			if idx, _ := m.groupedCursorToSession(); idx >= 0 && idx < len(m.sessions) {
				delName = m.sessions[idx].Name
			}
		} else if m.cursor < len(m.sessions) {
			delName = m.sessions[m.cursor].Name
		}
		if delName != "" {
			helpBar = warnStyle.Render(fmt.Sprintf("Delete '%s'? (y/n)", delName))
		}
	case m.confirmQuit:
		helpBar = warnStyle.Render(fmt.Sprintf("%d session(s) still running (will continue in background). Quit? (y/n)", len(m.sessions)))
	case m.confirmDetach:
		helpBar = warnStyle.Render(fmt.Sprintf("Detach? %d session(s) will continue running in background. (y/n)", len(m.sessions)))
	default:
		enterHint := "attach"
		if m.groupMode {
			if _, groupRoot := m.groupedCursorToSession(); groupRoot != "" {
				enterHint = "expand/collapse"
			}
		}
		keys := fmt.Sprintf("n: new  enter: %s  d: delete  D: detach  g: group  w: worktrees  ?: help  q: quit", enterHint)
		socket := m.config.TmuxSocket
		if socket == "" {
			socket = "vibeflow"
		}
		tmuxInfo := helpStyle.Render("tmux -L " + socket)
		keysRendered := helpStyle.Render(keys)
		pad := width - lipgloss.Width(keysRendered) - lipgloss.Width(tmuxInfo)
		if pad < 2 {
			pad = 2
		}
		helpBar = keysRendered + strings.Repeat(" ", pad) + tmuxInfo
	}

	// Column widths (in lipgloss v1, Width includes border + padding).
	leftWidth := width * 35 / 100
	rightWidth := width - leftWidth
	if leftWidth < 20 {
		leftWidth = 20
	}
	if rightWidth < 20 {
		rightWidth = 20
	}

	// Available height for columns: total minus banner, copyright, gap, help.
	usedLines := 10 // banner(7) + copyright(1) + gap(1) + help(1)
	if errLine != "" {
		usedLines++
	}
	colHeight := height - usedLines
	if colHeight < 6 {
		colHeight = 6
	}

	// Content area = total - border(2) - horizontal padding(2).
	leftContentW := leftWidth - 4
	rightContentW := rightWidth - 4
	contentH := colHeight - 2
	if leftContentW < 10 {
		leftContentW = 10
	}
	if rightContentW < 10 {
		rightContentW = 10
	}
	if contentH < 4 {
		contentH = 4
	}

	leftContent := m.renderSessionList(leftContentW, contentH)
	rightContent := m.renderDetailPanel(rightContentW, contentH)

	borderStyle := lipgloss.RoundedBorder()
	leftStyle := lipgloss.NewStyle().
		Width(leftWidth).
		Height(colHeight).
		Border(borderStyle).
		BorderForeground(dimColor).
		Padding(0, 1)
	rightStyle := lipgloss.NewStyle().
		Width(rightWidth).
		Height(colHeight).
		Border(borderStyle).
		BorderForeground(dimColor).
		Padding(0, 1)

	columns := lipgloss.JoinHorizontal(lipgloss.Top,
		leftStyle.Render(leftContent),
		rightStyle.Render(rightContent),
	)

	// Assemble final view.
	parts := []string{title}
	if errLine != "" {
		parts = append(parts, errLine)
	}
	parts = append(parts, columns, helpBar)

	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

// renderSessionList renders the left column with session entries.
func (m Model) renderSessionList(width, height int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	modeLabel := "flat"
	if m.groupMode {
		modeLabel = "grouped"
	}
	b.WriteString(headerStyle.Render(fmt.Sprintf("Sessions (%s)", modeLabel)))
	b.WriteString("\n")

	if len(m.sessions) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("No active sessions."))
		b.WriteString("\n")
		b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("Press 'n' to create one."))
		return b.String()
	}

	if m.groupMode {
		return m.renderGroupedList(width, &b)
	}

	for i, s := range m.sessions {
		m.renderSessionRow(&b, s, i, m.cursor, width, "")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderGroupedList renders the session list grouped by repo root.
func (m Model) renderGroupedList(width int, b *strings.Builder) string {
	pos := 0
	groupHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("#888888"))

	for _, root := range m.groupOrder {
		indices := m.groupedSessions[root]
		collapsed := m.collapsedGroups[root]

		// Render group header.
		arrow := "▾"
		if collapsed {
			arrow = "▸"
		}
		// Shorten long paths.
		displayRoot := root
		if len(displayRoot) > width-12 {
			displayRoot = "..." + displayRoot[len(displayRoot)-(width-15):]
		}
		header := fmt.Sprintf("%s %s (%d)", arrow, displayRoot, len(indices))

		if pos == m.cursor {
			b.WriteString(selectedStyle.Width(width).Render("> " + header))
		} else {
			b.WriteString("  " + groupHeaderStyle.Render(header))
		}
		b.WriteString("\n")
		pos++

		if !collapsed {
			for _, idx := range indices {
				m.renderSessionRow(b, m.sessions[idx], pos, m.cursor, width, "  ")
				pos++
			}
		}
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderSessionRow renders a single session row into the builder.
func (m Model) renderSessionRow(b *strings.Builder, s SessionRow, pos, cursor, width int, indent string) {
	indicator := "○"
	indStyle := statusIdle
	switch s.Status {
	case "running", "attached":
		indicator = "●"
		indStyle = statusRunning
	case "waiting":
		indicator = "●"
		indStyle = statusWaiting
	case "error":
		indicator = "●"
		indStyle = statusError
	}

	provDot := ""
	if s.Provider != "" {
		color, ok := providerColors[s.Provider]
		if !ok {
			color = accentColor
		}
		provDot = lipgloss.NewStyle().Foreground(color).Render("●") + " "
	}

	recoveredBadge := ""
	if s.Recovered {
		recoveredBadge = lipgloss.NewStyle().Foreground(warningColor).Render(" (recovered)")
	}

	healthBadge := ""
	if m.healthMonitor != nil {
		if sh := m.healthMonitor.GetHealth(s.Name); sh != nil {
			switch sh.Status {
			case HealthErrorDetected:
				healthBadge = lipgloss.NewStyle().Foreground(warningColor).Render(" [err]")
			case HealthRecovering:
				healthBadge = lipgloss.NewStyle().Foreground(warningColor).Render(fmt.Sprintf(" [recovering %d/%d]", sh.RecoveryCount, m.healthMonitor.config.MaxRetries))
			case HealthFailed:
				healthBadge = lipgloss.NewStyle().Foreground(errorColor).Render(" [FAILED]")
			}
		}
	}

	nameMax := width - 7 - len(indent)
	if s.Recovered {
		nameMax -= 12
	}
	if healthBadge != "" {
		nameMax -= 16
	}
	if nameMax < 8 {
		nameMax = 8
	}
	name := truncate(s.Name, nameMax)
	line := fmt.Sprintf("%s %s%s%s%s", indStyle.Render(indicator), provDot, name, recoveredBadge, healthBadge)

	if pos == cursor {
		b.WriteString(selectedStyle.Width(width).Render("> " + indent + line))
	} else {
		b.WriteString("  " + indent + line)
	}
	b.WriteString("\n")

	// Subtitle line: branch, persona, project (dim, indented).
	var parts []string
	if s.Branch != "" {
		parts = append(parts, s.Branch)
	}
	if s.Persona != "" {
		parts = append(parts, s.Persona)
	}
	if s.Project != "" {
		parts = append(parts, s.Project)
	}
	if len(parts) > 0 {
		subtitle := strings.Join(parts, " · ")
		subtitleStyle := lipgloss.NewStyle().Foreground(dimColor)
		// Align with the name text (after indicator + provider dot).
		pad := "    " + indent
		if s.Provider != "" {
			pad += "  "
		}
		b.WriteString(pad + subtitleStyle.Render(subtitle))
		b.WriteString("\n")
	}
}

// renderDetailPanel renders the right column with metadata for the selected session.
func (m Model) renderDetailPanel(width, height int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	b.WriteString(headerStyle.Render("Detail"))
	b.WriteString("\n")

	idx := m.selectedSessionIdx()
	if idx < 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("Select a session to view details."))
		return b.String()
	}

	s := m.sessions[idx]

	labelStyle := lipgloss.NewStyle().Foreground(dimColor).Width(14)
	valueStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff"))

	row := func(label, value string) {
		b.WriteString(labelStyle.Render(label))
		b.WriteString(valueStyle.Render(value))
		b.WriteString("\n")
	}

	// Name.
	row("Name", s.Name)

	// Status (uses styled render).
	b.WriteString(labelStyle.Render("Status"))
	b.WriteString(renderStatus(s.Status))
	b.WriteString("\n")

	// Provider (uses styled render with color dot).
	if s.Provider != "" {
		b.WriteString(labelStyle.Render("Provider"))
		b.WriteString(renderProvider(s.Provider))
		b.WriteString("\n")
	}

	// Project.
	if s.Project != "" {
		row("Project", s.Project)
	}

	// Branch.
	if s.Branch != "" {
		row("Branch", renderBranch(s.Branch, s.WorktreePath))
	}

	// Current work.
	if s.CurrentWork != "" {
		valMax := width - 14
		if valMax < 10 {
			valMax = 10
		}
		row("Current Work", truncate(s.CurrentWork, valMax))
	}

	// Last heartbeat.
	if !s.LastHeartbeat.IsZero() {
		row("Heartbeat", time.Since(s.LastHeartbeat).Truncate(time.Second).String()+" ago")
	}

	// Worktree path.
	if s.WorktreePath != "" {
		valMax := width - 14
		if valMax < 10 {
			valMax = 10
		}
		row("Worktree", truncate(s.WorktreePath, valMax))
	}

	// Attached indicator.
	if s.TmuxAttached {
		row("Attached", "yes")
	}

	// Health status banner.
	if m.healthMonitor != nil {
		if sh := m.healthMonitor.GetHealth(s.Name); sh != nil && sh.Status != HealthHealthy {
			b.WriteString("\n")
			switch sh.Status {
			case HealthErrorDetected:
				b.WriteString(lipgloss.NewStyle().Foreground(warningColor).Render("⚠ Error detected — debouncing..."))
				b.WriteString("\n")
			case HealthRecovering:
				b.WriteString(lipgloss.NewStyle().Foreground(warningColor).Render(
					fmt.Sprintf("⚠ Auto-recovery in progress (attempt %d/%d)", sh.RecoveryCount, m.healthMonitor.config.MaxRetries)))
				b.WriteString("\n")
				if sh.MatchedPattern != nil {
					b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("  " + sh.MatchedPattern.Description))
					b.WriteString("\n")
				}
			case HealthFailed:
				b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(
					fmt.Sprintf("✘ Unrecoverable after %d attempts — press 'r' to retry", sh.RecoveryCount)))
				b.WriteString("\n")
				if sh.MatchedPattern != nil {
					b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("  " + sh.MatchedPattern.Description))
					b.WriteString("\n")
				}
			}
		}
	}

	// Separator and capture-pane output.
	b.WriteString("\n")
	sepStyle := lipgloss.NewStyle().Foreground(dimColor)
	b.WriteString(sepStyle.Render(strings.Repeat("─", width)))
	b.WriteString("\n")
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(dimColor).Render("Output"))
	b.WriteString("\n")

	if m.captureName == s.Name && m.captureOutput != "" {
		// Limit output lines to fit remaining height.
		// metadata uses ~10 lines, separator+header 3, so remaining = height - 13.
		maxLines := height - 13
		if maxLines < 3 {
			maxLines = 3
		}
		lines := strings.Split(m.captureOutput, "\n")
		if len(lines) > maxLines {
			lines = lines[len(lines)-maxLines:]
		}
		outputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa"))
		for _, line := range lines {
			b.WriteString(outputStyle.Render(truncate(line, width)))
			b.WriteString("\n")
		}
	} else {
		b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("(no output yet)"))
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderHelpPopup renders a centered help overlay with categorized keyboard shortcuts.
func (m Model) renderHelpPopup() string {
	width := m.width
	if width < 40 {
		width = 80
	}
	height := m.height
	if height < 10 {
		height = 24
	}

	catStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	keyStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Width(16)
	descStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa"))
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)

	var b strings.Builder
	b.WriteString(catStyle.Render("Navigation"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  j / k") + descStyle.Render("Move down / up") + "\n")
	b.WriteString(keyStyle.Render("  enter") + descStyle.Render("Attach to session") + "\n")
	b.WriteString(keyStyle.Render("  g") + descStyle.Render("Toggle flat / grouped view") + "\n")
	b.WriteString("\n")

	b.WriteString(catStyle.Render("Session Management"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  n") + descStyle.Render("New session (wizard)") + "\n")
	b.WriteString(keyStyle.Render("  d") + descStyle.Render("Delete session") + "\n")
	b.WriteString(keyStyle.Render("  D") + descStyle.Render("Detach (quit, sessions persist)") + "\n")
	b.WriteString(keyStyle.Render("  w") + descStyle.Render("Manage worktrees") + "\n")
	b.WriteString(keyStyle.Render("  r") + descStyle.Render("Retry recovery / refresh") + "\n")
	b.WriteString("\n")

	b.WriteString(catStyle.Render("Application"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  ?") + descStyle.Render("Show this help") + "\n")
	b.WriteString(keyStyle.Render("  q") + descStyle.Render("Quit vibeflow-cli") + "\n")
	b.WriteString(keyStyle.Render("  ctrl+c") + descStyle.Render("Force quit") + "\n")
	b.WriteString("\n")

	b.WriteString(catStyle.Render("Inside Agent Session"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  ctrl+q") + descStyle.Render("Open vibeflow menu") + "\n")
	b.WriteString(keyStyle.Render("  ctrl+\\") + descStyle.Render("Open vibeflow menu (backup)") + "\n")
	b.WriteString("\n")

	// Info section.
	socket := m.config.TmuxSocket
	if socket == "" {
		socket = "vibeflow"
	}
	b.WriteString(catStyle.Render("Info"))
	b.WriteString("\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  tmux socket:  %s", socket)) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  config:       %s", ConfigPath())) + "\n")
	b.WriteString(dimStyle.Render(fmt.Sprintf("  store:        %s", DefaultStorePath())) + "\n")
	b.WriteString("\n")
	b.WriteString(dimStyle.Render("Press any key to close"))

	content := b.String()

	// Wrap in a styled box.
	popupWidth := 52
	popupStyle := lipgloss.NewStyle().
		Width(popupWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 2)

	popup := popupStyle.Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup)
}

// Provider color-coded dots.
var providerColors = map[string]lipgloss.Color{
	"claude": lipgloss.Color("#cc785c"), // warm amber
	"codex":  lipgloss.Color("#10a37f"), // OpenAI green
	"gemini": lipgloss.Color("#4285f4"), // Google blue
}

func renderProvider(provider string) string {
	if provider == "" {
		return helpStyle.Render("-")
	}
	color, ok := providerColors[provider]
	if !ok {
		color = accentColor
	}
	dot := lipgloss.NewStyle().Foreground(color).Render("●")
	return fmt.Sprintf("%s %s", provider, dot)
}

func renderBranch(branch, worktreePath string) string {
	if branch == "" {
		return "-"
	}
	if worktreePath != "" {
		return branch + " (worktree)"
	}
	return branch
}

func renderStatus(status string) string {
	switch status {
	case "running":
		return statusRunning.Render("running")
	case "attached":
		return statusRunning.Render("attached")
	case "idle":
		return statusIdle.Render("idle")
	case "waiting":
		return statusWaiting.Render("waiting")
	case "error":
		return statusError.Render("error")
	default:
		return statusIdle.Render(status)
	}
}

func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}
