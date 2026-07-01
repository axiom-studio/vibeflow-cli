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
	"fmt"
	"os/exec"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"

	"vibeflow-cli/sessionid"
)

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// Colors for the vibeflow theme — semantic aliases onto the Ocean design
// system palette (see theme.go / design_system doc #401). Downstream render
// code references these names; the concrete values live in theme.go.
var (
	accentColor  = oceanPrimary
	dimColor     = oceanMuted
	errorColor   = oceanError
	warningColor = oceanWarning

	titleStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(accentColor).
			MarginBottom(1)

	// Selected: dark text on a sky-blue bar (Ocean primary).
	selectedStyle = lipgloss.NewStyle().
			Bold(true).
			Foreground(oceanBackground).
			Background(oceanPrimary)

	statusRunning = lipgloss.NewStyle().Foreground(oceanSuccess)
	statusIdle    = lipgloss.NewStyle().Foreground(dimColor)
	statusWaiting = lipgloss.NewStyle().Foreground(warningColor)
	statusError   = lipgloss.NewStyle().Foreground(errorColor)

	helpStyle = lipgloss.NewStyle().Foreground(dimColor)

	asciiBanner    = accentColor
	copyrightStyle = lipgloss.NewStyle().Foreground(dimColor)
)

// bannerText is the 3D ASCII art for "VibeFlow" displayed on TUI startup.
const bannerText = `
██╗   ██╗██╗██████╗ ███████╗███████╗██╗      ██████╗ ██╗    ██╗
██║   ██║██║██╔══██╗██╔════╝██╔════╝██║     ██╔═══██╗██║    ██║
██║   ██║██║██████╔╝█████╗  █████╗  ██║     ██║   ██║██║ █╗ ██║
╚██╗ ██╔╝██║██╔══██╗██╔══╝  ██╔══╝  ██║     ██║   ██║██║███╗██║
 ╚████╔╝ ██║██████╔╝███████╗██║     ███████╗╚██████╔╝╚███╔███╔╝
  ╚═══╝  ╚═╝╚═════╝ ╚══════╝╚═╝     ╚══════╝ ╚═════╝  ╚══╝╚══╝`

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

	// LLMGatewayEnabled mirrors SessionMeta.LLMGatewayEnabled so the detail
	// panel can re-derive the gateway env wiring for the selected session.
	LLMGatewayEnabled bool
}

// ViewState controls which sub-view is active.
type ViewState int

const (
	ViewSessions ViewState = iota
	ViewWizard
	ViewConflict
	ViewWorktrees
	ViewHelp
	ViewRestart
)

// Model is the Bubble Tea model for vibeflow-cli.
type Model struct {
	sessions        []SessionRow
	cursor          int
	client          *Client
	tmux            *TmuxManager
	worktrees       *WorktreeManager
	store           *Store
	registry        *ProviderRegistry
	config          *Config
	width           int
	height          int
	err             error
	quitting        bool
	projectID       int64
	activeView      ViewState
	wizard          WizardModel
	conflictModal   ConflictModal
	worktreeList    WorktreeListModel
	pendingWizard   *WizardResult      // wizard result waiting for conflict resolution
	switchMeta      *SessionMeta       // non-nil during quick branch switch flow
	captureOutput   string             // last captured pane output for selected session
	captureName     string             // tmux session name for current capture
	confirmDelete   bool               // showing delete confirmation
	confirmQuit     bool               // showing quit confirmation
	confirmDetach   bool               // showing detach confirmation
	workbenchActive bool               // true while a pane-join workbench is composing/attached/restoring (pauses store prune)
	serverWarning   string             // non-empty if server unreachable at startup
	healthMonitor   *HealthMonitor     // session error detection and auto-recovery
	logger          *Logger            // file-based logger
	cache           *SessionCache      // session cache for restart-without-intervention
	restartSelect   RestartSelectModel // dead-session restart multiselect

	// Grouped view state.
	groupMode       bool              // true = grouped by repo root, false = flat
	repoRootCache   map[string]string // workingDir → repo root cache
	collapsedGroups map[string]bool   // repo root → collapsed state
	groupOrder      []string          // ordered list of repo roots
	groupedSessions map[string][]int  // repo root → indices into m.sessions
}

// NewModel creates a new TUI model.
func NewModel(cfg *Config, client *Client, tmux *TmuxManager, worktrees *WorktreeManager, store *Store, cache *SessionCache, registry *ProviderRegistry, projectID int64) Model {
	logger := NewLogger()
	logger.Info("vibeflow-cli started (server=%s, project=%s)", cfg.ServerURL, cfg.DefaultProject)
	tmux.SetLogger(logger)
	errorRegistry := NewErrorPatternRegistry()
	healthMonitor := NewHealthMonitor(errorRegistry, tmux, cfg.ErrorRecovery, logger)
	return Model{
		config:          cfg,
		client:          client,
		tmux:            tmux,
		worktrees:       worktrees,
		store:           store,
		cache:           cache,
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

// workbenchReadyMsg carries the result of composing the pane-join workbench.
// The composition (or the error) is produced off the Update goroutine so the
// tmux calls do not block the UI.
type workbenchReadyMsg struct {
	comp  *WorkbenchComposition
	err   error
	metas []SessionMeta // store metadata of the composed sessions, re-applied on restore
}

// workbenchExitMsg is sent when the composed workbench attach process exits, so
// the joined panes can be restored to their own sessions.
type workbenchExitMsg struct {
	comp  *WorkbenchComposition
	metas []SessionMeta
}

// workbenchRestoredMsg is sent after the workbench panes have been restored to
// their own sessions and their store metadata re-applied.
type workbenchRestoredMsg struct{}

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

// cacheGCMsg triggers periodic session cache garbage collection.
type cacheGCMsg time.Time

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

// isWorktreeInUseByOthers returns true if any session other than excludeSession
// references the same worktree path. Prevents deleting a worktree that sibling
// sessions (e.g. qa_lead sharing a worktree with developer) still use.
func (m Model) isWorktreeInUseByOthers(worktreePath, excludeSession string) bool {
	if m.store == nil || worktreePath == "" {
		return false
	}
	metas, err := m.store.List()
	if err != nil {
		return true // err on side of caution
	}
	for _, meta := range metas {
		if meta.Name != excludeSession && meta.WorktreePath == worktreePath {
			return true
		}
	}
	return false
}

// safeRemoveWorktree removes a worktree only if it is not shared by other sessions
// and has no uncommitted changes. Returns true if the worktree was removed.
func (m Model) safeRemoveWorktree(worktreePath, sessionName string) bool {
	if worktreePath == "" || m.worktrees == nil {
		return false
	}
	if m.isWorktreeInUseByOthers(worktreePath, sessionName) {
		return false
	}
	if isDirtyGit(worktreePath) {
		if m.logger != nil {
			m.logger.Warn("keeping dirty worktree %s — has uncommitted changes", worktreePath)
		}
		return false
	}
	_ = m.worktrees.Remove(worktreePath, true)
	return true
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
	// Skip store prune/rediscover while a workbench is composing/attached: its
	// sessions are transiently moved into the holder (absent from tmux), and
	// pruning would drop their non-reconstructable metadata (persona/project).
	if m.store != nil && !m.workbenchActive {
		_ = m.store.Sync(tmuxNames)
	}

	// Discover orphaned sessions (live in tmux but not in store) and
	// reconstruct their metadata from tmux state.
	recoveredNames := make(map[string]bool)
	if m.store != nil && !m.workbenchActive {
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
		// The workbench holder is an internal composition session, not a user
		// agent — never list it, or it shows as "workbench" and (while a
		// workbench is composed/orphaned) masks the agents joined into it (#3300).
		if isWorkbenchHolder(ts.Name) {
			continue
		}
		shortName := strings.TrimPrefix(ts.Name, sessionPrefix)
		row := SessionRow{
			Name:         shortName,
			Status:       sessionStatus(ts.Attached, ts.PaneDead),
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
			row.LLMGatewayEnabled = meta.LLMGatewayEnabled
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

func sessionStatus(attached, paneDead bool) string {
	if paneDead {
		return "exited"
	}
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

// projectLabel returns a short display label for a repo root (its basename), or
// "(unknown)" when the root is unknown.
func projectLabel(root string) string {
	if root == "" || root == "(unknown)" {
		return "(unknown)"
	}
	return filepath.Base(root)
}

// selectedProjectSessions returns the label and tmux names of the sessions that
// share the selected session's project (repo root), in list order. Used by the
// `m` (single-project) workbench.
// selectedRepoRoot resolves the current cursor to a project's repo root. In
// grouped mode groupedCursorToSession returns the group's root even when the
// cursor is on a project HEADER (not a session), so the m/M workbench shortcuts
// work with a project root selected (#3293) — previously selectedSessionIdx
// returned -1 on a header and the shortcuts no-op'd.
func (m Model) selectedRepoRoot() (root string, ok bool) {
	if m.groupMode {
		idx, groupRoot := m.groupedCursorToSession()
		if idx < 0 && groupRoot == "" {
			return "", false // cursor maps to no group
		}
		return groupRoot, true // group root for both a header and a session
	}
	idx := m.selectedSessionIdx()
	if idx < 0 || idx >= len(m.sessions) {
		return "", false
	}
	return m.getRepoRoot(m.sessions[idx].WorkingDir), true
}

func (m Model) selectedProjectSessions() (label string, names []string) {
	selRoot, ok := m.selectedRepoRoot()
	if !ok {
		return "", nil
	}
	for _, s := range m.sessions {
		if m.getRepoRoot(s.WorkingDir) == selRoot {
			names = append(names, s.Name)
		}
	}
	return projectLabel(selRoot), names
}

// projectGroups returns every project (repo-root group) with its session names,
// in first-seen order. Used by the `M` (all-projects) workbench.
func (m Model) projectGroups() []WorkbenchProject {
	var order []string
	byRoot := map[string][]string{}
	for _, s := range m.sessions {
		root := m.getRepoRoot(s.WorkingDir)
		if _, ok := byRoot[root]; !ok {
			order = append(order, root)
		}
		byRoot[root] = append(byRoot[root], s.Name)
	}
	out := make([]WorkbenchProject, 0, len(order))
	for _, root := range order {
		out = append(out, WorkbenchProject{Label: projectLabel(root), Sessions: byRoot[root]})
	}
	return out
}

// workbenchMetas returns the store SessionMeta for the given full tmux session
// names, so the workbench can re-apply persona/project/etc. after restore (tmux
// alone cannot hold those fields).
func (m Model) workbenchMetas(fullNames []string) []SessionMeta {
	if m.store == nil {
		return nil
	}
	want := make(map[string]bool, len(fullNames))
	for _, n := range fullNames {
		want[n] = true
	}
	all, err := m.store.List()
	if err != nil {
		return nil
	}
	var out []SessionMeta
	for _, meta := range all {
		if want[meta.TmuxSession] {
			out = append(out, meta)
		}
	}
	return out
}

// workbenchTitles maps each session's full tmux name to its workbench pane
// header ("persona · project · branch"), built from the currently listed rows
// so the composed panes are self-labeled. Rows are what the user already sees
// in the list, so they are the reliable header source even when the store meta
// is absent.
func (m Model) workbenchTitles() map[string]string {
	titles := make(map[string]string, len(m.sessions))
	for _, s := range m.sessions {
		if h := workbenchHeader(s.Persona, s.Project, s.Branch); h != "" {
			// Key by the FULL tmux name (with the vibeflow_ prefix): composeInto
			// looks the header up via titles[ensurePrefix(name)], and s.Name has
			// the prefix stripped. Keying by the short name made every lookup
			// miss, so panes fell back to the session-name title (#3291).
			titles[sessionPrefix+s.Name] = h
		}
	}
	return titles
}

// composeWorkbenchCmd runs ComposeWorkbench off the Update goroutine (it issues
// several tmux commands) and reports the result via workbenchReadyMsg.
func (m Model) composeWorkbenchCmd(names []string, metas []SessionMeta, titles map[string]string) tea.Cmd {
	tmux := m.tmux
	return func() tea.Msg {
		comp, err := tmux.ComposeWorkbench(names, titles)
		return workbenchReadyMsg{comp: comp, err: err, metas: metas}
	}
}

// composeProjectWorkbenchCmd runs ComposeProjectWorkbench off the Update
// goroutine and reports the result via workbenchReadyMsg.
func (m Model) composeProjectWorkbenchCmd(projects []WorkbenchProject, selectLabel string, metas []SessionMeta, titles map[string]string) tea.Cmd {
	tmux := m.tmux
	return func() tea.Msg {
		comp, err := tmux.ComposeProjectWorkbench(projects, selectLabel, titles)
		return workbenchReadyMsg{comp: comp, err: err, metas: metas}
	}
}

// cacheGCTickCmd returns a command that fires a GC tick every 1 minute.
func cacheGCTickCmd() tea.Cmd {
	return tea.Tick(1*time.Minute, func(t time.Time) tea.Msg {
		return cacheGCMsg(t)
	})
}

// Init initializes the model.
func (m Model) Init() tea.Cmd {
	return tea.Batch(
		m.refreshSessions,
		captureTickCmd(),
		tickCmd(time.Duration(m.config.PollInterval)*time.Second),
		cacheGCTickCmd(),
	)
}

// Update handles messages.
func (m Model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	// Global handlers — process regardless of active view so ticks and
	// session refreshes continue while sub-views (wizard, conflict modal,
	// worktree list) are active.
	switch msg := msg.(type) {
	case tea.FocusMsg:
		// Pane regained focus (e.g. tmux pane switch). Force a full repaint
		// so the diff-based renderer doesn't skip lines it assumes are unchanged.
		return m, tea.ClearScreen
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
	case cacheGCMsg:
		// Periodic session cache garbage collection (every 1 minute).
		if m.cache != nil {
			if names, err := m.tmux.ListSessionNames(); err == nil {
				_ = m.cache.GC(names)
			}
		}
		return m, cacheGCTickCmd()
	case restartConfirmMsg:
		// User confirmed dead sessions to restart.
		m.activeView = ViewSessions
		for _, meta := range msg.sessions {
			if _, err := RestartSession(meta, m.config, m.tmux, m.store, m.cache, m.registry); err != nil {
				m.logger.Error("restart session %s: %v", meta.Name, err)
			} else {
				m.logger.Info("restarted dead session: %s", meta.Name)
			}
		}
		return m, m.refreshSessions
	case restartSkipMsg:
		// User skipped dead session restart — clean up cache.
		m.activeView = ViewSessions
		if m.cache != nil {
			if names, err := m.tmux.ListSessionNames(); err == nil {
				_ = m.cache.GC(names)
			}
		}
		return m, nil
	case attachExitMsg:
		// tmux attach exited — refresh sessions to pick up status changes.
		// No ClearScreen needed: RestoreTerminal already re-enters alt screen
		// which clears the screen and calls repaint() internally.
		return m, m.refreshSessions
	case workbenchReadyMsg:
		// Composition finished off-goroutine. On success, attach natively to the
		// holder; on failure, surface the error, auto-clear it, and end the
		// workbench window so store sync resumes.
		if msg.err != nil {
			m.logger.Error("workbench compose: %v", msg.err)
			m.err = msg.err
			m.workbenchActive = false
			return m, tea.Tick(10*time.Second, func(time.Time) tea.Msg { return errClearMsg{} })
		}
		comp := msg.comp
		metas := msg.metas
		cmd := m.tmux.AttachSessionCmd(comp.HolderName())
		return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
			if err != nil {
				m.logger.Error("workbench attach: %v", err)
			}
			return workbenchExitMsg{comp: comp, metas: metas}
		})
	case workbenchExitMsg:
		// Workbench attach exited — restore every joined pane to its own session
		// and re-apply its captured store metadata (off-goroutine). workbenchActive
		// stays true until this completes so a racing refresh cannot prune the
		// transiently-absent sessions.
		comp := msg.comp
		metas := msg.metas
		store := m.store
		logger := m.logger
		return m, func() tea.Msg {
			if comp != nil {
				if err := comp.Restore(); err != nil {
					logger.Error("workbench restore: %v", err)
				}
			}
			// Re-apply persona/project/etc. in case a refresh raced the compose
			// and pruned it — Restore only recovers the tmux session, not the
			// store fields that tmux cannot hold.
			if store != nil {
				for _, meta := range metas {
					_ = store.Add(meta)
				}
			}
			return workbenchRestoredMsg{}
		}
	case workbenchRestoredMsg:
		// Restore complete — re-enable store sync and refresh the list.
		m.workbenchActive = false
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
	case ViewRestart:
		var cmd tea.Cmd
		m.restartSelect, cmd = m.restartSelect.Update(msg)
		return m, cmd
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
							// Session file is intentionally kept so the session
							// ID can be reused on next launch. Stale conflict
							// detection handles cleanup and ID preservation.
							if m.config.Worktree.CleanupOnKill == "always" {
								m.safeRemoveWorktree(meta.WorktreePath, meta.Name)
							}
						}
						_ = m.store.Remove(row.Name)
					}
					if m.cache != nil {
						_ = m.cache.Remove(row.Name)
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
		case "b":
			// Quick branch switch for the selected session.
			idx := m.selectedSessionIdx()
			if idx < 0 || idx >= len(m.sessions) || m.store == nil {
				return m, nil
			}
			row := m.sessions[idx]
			meta, found, _ := m.store.Get(row.Name)
			if !found {
				return m, nil
			}
			repoRoot := meta.WorkingDir
			if meta.WorktreePath != "" && m.worktrees != nil {
				repoRoot = m.worktrees.RepoRoot()
			}
			m.wizard = NewQuickSwitchWizard(meta, m.registry, repoRoot, m.worktrees, m.config)
			m.switchMeta = &meta
			m.activeView = ViewWizard
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
		case "m":
			// Project workbench: compose the selected session's project (its
			// repo-root group) into one natively interactive tmux view. One
			// session → attach it directly; none → no-op.
			_, names := m.selectedProjectSessions()
			switch len(names) {
			case 0:
				return m, nil
			case 1:
				cmd := m.tmux.AttachSessionCmd(names[0])
				return m, tea.ExecProcess(cmd, func(err error) tea.Msg {
					return attachExitMsg{err: err}
				})
			default:
				m.workbenchActive = true
				return m, m.composeWorkbenchCmd(names, m.workbenchMetas(names), m.workbenchTitles())
			}
		case "M":
			// All-projects workbench: one tmux window per project, cycled with
			// Ctrl-b n/p. Worth composing only with ≥2 sessions total.
			projects := m.projectGroups()
			var allNames []string
			for _, p := range projects {
				allNames = append(allNames, p.Sessions...)
			}
			if len(allNames) < 2 {
				return m, nil
			}
			selLabel, _ := m.selectedProjectSessions()
			m.workbenchActive = true
			return m, m.composeProjectWorkbenchCmd(projects, selLabel, m.workbenchMetas(allNames), m.workbenchTitles())
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
		m.switchMeta = nil
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

		// Quick branch switch: kill old session, then launch new one.
		if m.switchMeta != nil {
			oldMeta := *m.switchMeta
			m.switchMeta = nil
			return m, func() tea.Msg {
				// For in-place switches, check dirty state BEFORE killing.
				if result.WorktreeChoice == WorktreeCurrent || result.WorktreeChoice == WorktreeSpecifyDir {
					dir := oldMeta.WorkingDir
					if result.WorktreeChoice == WorktreeSpecifyDir {
						dir = result.SpecifiedWorkDir
					}
					if isDirtyGit(dir) {
						return sessionsMsg{err: fmt.Errorf(
							"working tree has uncommitted changes — commit/stash first, or choose 'New worktree'")}
					}
				}
				// Kill old session. Abort if it fails to avoid ghost duplicates.
				if err := m.tmux.KillSession(oldMeta.TmuxSession); err != nil {
					// Check if session is truly still running.
					if m.tmux.HasSession(oldMeta.TmuxSession) {
						return sessionsMsg{err: fmt.Errorf("failed to kill old session %s: %w — switch aborted", oldMeta.Name, err)}
					}
					// Session is already gone — safe to proceed.
				}
				if m.store != nil {
					if m.config.Worktree.CleanupOnKill == "always" {
						m.safeRemoveWorktree(oldMeta.WorktreePath, oldMeta.Name)
					}
					_ = m.store.Remove(oldMeta.Name)
				}
				if m.cache != nil {
					_ = m.cache.Remove(oldMeta.Name)
				}
				// Git checkout for in-place switches.
				if result.WorktreeChoice == WorktreeCurrent || result.WorktreeChoice == WorktreeSpecifyDir {
					dir := oldMeta.WorkingDir
					if result.WorktreeChoice == WorktreeSpecifyDir {
						dir = result.SpecifiedWorkDir
					}
					if err := gitCheckoutBranch(dir, result.Branch, result.NewBranch, result.NewBranchBase); err != nil {
						return sessionsMsg{err: err}
					}
				}
				return m.launchFromWizard(result)
			}
		}

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
		conflictPersona := cm.Conflict().Persona
		_ = CleanupStaleSession(conflictDir, conflictPersona)
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
	personas := result.Personas
	if len(personas) == 0 {
		personas = []string{result.Persona}
	}

	// Single persona — existing behavior (no multi-spawn overhead).
	if len(personas) == 1 {
		result.Persona = personas[0]
		workDir := "."
		if result.WorkDir != "" {
			workDir = result.WorkDir
		}
		switch result.WorktreeChoice {
		case WorktreeCurrent:
			conflict := CheckConflict(workDir, result.Persona, m.tmux)
			switch conflict.Status {
			case StaleConflict, ExternalConflict:
				// Silently clean up stale/external conflicts and reuse the
				// session ID so the vibeflow API session is preserved.
				if result.SessionType == "vibeflow" && conflict.SessionID != "" {
					result.ReuseSessionID = conflict.SessionID
				}
				_ = CleanupStaleSession(workDir, result.Persona)
			case ActiveConflict:
				return conflictDetectedMsg{conflict: conflict, wizardResult: result}
			}
		case WorktreeSpecifyDir:
			if result.SpecifiedWorkDir != "" {
				conflict := CheckConflict(result.SpecifiedWorkDir, result.Persona, m.tmux)
				switch conflict.Status {
				case StaleConflict, ExternalConflict:
					if result.SessionType == "vibeflow" && conflict.SessionID != "" {
						result.ReuseSessionID = conflict.SessionID
					}
					_ = CleanupStaleSession(result.SpecifiedWorkDir, result.Persona)
				case ActiveConflict:
					return conflictDetectedMsg{conflict: conflict, wizardResult: result}
				}
			}
		}
		return m.executeLaunch(result)
	}

	// Multi-persona: resolve workDir once (creates worktree if needed),
	// then spawn one session per persona in the same directory.
	workDir, worktreePath, err := m.resolveSessionWorkDir(result)
	if err != nil {
		return sessionsMsg{err: err}
	}

	// Check conflicts for each persona in the resolved workDir.
	// Stale/external conflicts are auto-cleaned with session ID preservation.
	reuseIDs := make(map[string]string) // persona → old session ID
	for _, persona := range personas {
		conflict := CheckConflict(workDir, persona, m.tmux)
		switch conflict.Status {
		case StaleConflict, ExternalConflict:
			if result.SessionType == "vibeflow" && conflict.SessionID != "" {
				reuseIDs[persona] = conflict.SessionID
			}
			_ = CleanupStaleSession(workDir, persona)
		case ActiveConflict:
			return conflictDetectedMsg{conflict: conflict, wizardResult: result}
		}
	}

	// Spawn a session for each persona. Override result to use the pre-resolved
	// workDir so executeLaunch doesn't try to create the worktree again.
	var firstErr error
	spawned := 0
	for _, persona := range personas {
		r := result
		r.Persona = persona
		r.WorkDir = workDir
		// Resolve per-persona provider override (team mode). Single-persona
		// flow above intentionally bypasses this — solo launches use
		// result.Provider directly.
		provider, providerKey, err := ResolvePersonaProvider(persona, result.PersonaProviders, result.ProviderKey, result.Provider, m.registry)
		if err != nil {
			m.logger.Error("resolve provider for persona %s: %v", persona, err)
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		r.Provider = provider
		r.ProviderKey = providerKey
		if rID, ok := reuseIDs[persona]; ok {
			r.ReuseSessionID = rID
		}
		if worktreePath != "" {
			r.WorktreeChoice = WorktreeExisting
			r.ExistingWorktreePath = worktreePath
		} else {
			r.WorktreeChoice = WorktreeCurrent
		}
		msg := m.executeLaunch(r)
		if errMsg, ok := msg.(sessionsMsg); ok && errMsg.err != nil {
			m.logger.Error("spawn persona %s: %v", persona, errMsg.err)
			if firstErr == nil {
				firstErr = errMsg.err
			}
			continue
		}
		spawned++
	}

	if spawned == 0 && firstErr != nil {
		return sessionsMsg{err: fmt.Errorf("all %d persona sessions failed: %w", len(personas), firstErr)}
	}
	return m.refreshSessions()
}

// conflictDetectedMsg triggers the conflict modal from within launchFromWizard.
type conflictDetectedMsg struct {
	conflict     ConflictResult
	wizardResult WizardResult
}

// autoAttachMsg signals that a newly created session should be auto-attached.
type autoAttachMsg struct{ name string }

// resolveSessionWorkDir resolves the working directory and optional worktree path
// from the wizard result. Creates a new worktree if needed.
func (m Model) resolveSessionWorkDir(result WizardResult) (workDir, worktreePath string, err error) {
	workDir = m.config.ResolveWorkDir("")
	if result.WorkDir != "" {
		workDir = result.WorkDir
	}

	// Resolve the WorktreeManager to use — if the wizard selected a different
	// directory than the TUI's default, create a temporary manager for it.
	wm := m.worktrees
	if result.WorkDir != "" && (wm == nil || wm.RepoRoot() != result.WorkDir) {
		if newWM, wmErr := NewWorktreeManager(result.WorkDir, m.config.Worktree.BaseDir); wmErr == nil {
			wm = newWM
		}
	}

	provider := result.ProviderKey
	branch := result.Branch

	switch result.WorktreeChoice {
	case WorktreeNew:
		if wm != nil {
			wtName := result.WorktreeName
			if wtName == "" {
				wtName = fmt.Sprintf("%s-%s-%d", provider, branch, time.Now().Unix())
			}
			wtPath, wtErr := wm.CreateBranch(wtName, branch, result.NewBranch, result.NewBranchBase)
			if wtErr != nil {
				return "", "", fmt.Errorf("create worktree: %w", wtErr)
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
			wtPath, wtErr := wm.CreateBranchInDir(result.CustomBaseDir, wtName, branch, result.NewBranch, result.NewBranchBase)
			if wtErr != nil {
				return "", "", fmt.Errorf("create worktree in custom dir: %w", wtErr)
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
	return workDir, worktreePath, nil
}

// executeLaunch performs the actual session creation after conflict resolution.
func (m Model) executeLaunch(result WizardResult) tea.Msg {
	workDir, worktreePath, err := m.resolveSessionWorkDir(result)
	if err != nil {
		return sessionsMsg{err: err}
	}
	name := sessionid.GenerateSessionID(workDir)
	provider := result.ProviderKey
	branch := result.Branch

	// For VibeFlow managed sessions, ensure a valid .vibeflow-session file
	// exists before spawning. The agent will call session_init itself via MCP
	// on startup to register with the server and get the full agent prompt.
	var vibeflowSessionID string
	projectName := m.config.DefaultProject
	if result.SessionType == "vibeflow" {
		if result.ProjectName != "" {
			projectName = result.ProjectName
		}
		// Try to reuse an existing session ID (from conflict modal or file).
		reuseID := result.ReuseSessionID
		if reuseID == "" {
			if existingID, _, _ := readSessionFileID(workDir, result.Persona); existingID != "" {
				reuseID = existingID
				m.logger.Info("read existing session ID from .vibeflow-session-%s: %s", result.Persona, existingID)
			}
		}
		if reuseID != "" {
			vibeflowSessionID = reuseID
		} else {
			// Generate a fresh session ID locally.
			vibeflowSessionID = sessionid.GenerateSessionID(workDir)
			m.logger.Info("generated local session ID: %s", vibeflowSessionID)
		}
		name = vibeflowSessionID
		// Ensure .vibeflow-session-{persona} exists so the agent can read it on startup.
		_ = WriteSessionFileIfNeeded(workDir, result.Persona, vibeflowSessionID)
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

	// Merge wizard-resolved env vars (e.g. codex bearer token) into provider env.
	if result.EnvVars != nil {
		if result.Provider.Env == nil {
			result.Provider.Env = make(map[string]string)
		}
		for k, v := range result.EnvVars {
			result.Provider.Env[k] = v
		}
	}

	// If LLM gateway is enabled, inject gateway env vars for the provider.
	// Otherwise, explicitly clear gateway-related vars to prevent inheritance
	// from the parent shell environment.
	if result.SessionType == "vibeflow" && result.LLMGatewayEnabled {
		if result.Provider.Env == nil {
			result.Provider.Env = make(map[string]string)
		}
		for k, v := range BuildLLMGatewayEnv(provider, m.config.ServerURL, m.config.APIToken) {
			result.Provider.Env[k] = v
		}
	} else {
		if result.Provider.Env == nil {
			result.Provider.Env = make(map[string]string)
		}
		for k, v := range ClearLLMGatewayEnv(provider) {
			result.Provider.Env[k] = v
		}
	}
	result.Provider.Env = WithMCPTokenEnv(result.Provider.Env, m.config)

	// Mirror Codex gateway config and qwen routed env vars onto the command
	// line so each provider sees the explicit launch-time configuration it
	// expects.
	command = AppendCodexGatewayProviderFlags(command, provider, result.Provider.Env)
	// For qwen, env vars alone don't always drive model reporting.
	// Must run after env merging and before the init-prompt append so the
	// flags land between the base command and the seed prompt argument.
	command = AppendQwenAPIFlags(command, provider, result.Provider.Env)

	// For vibeflow sessions, pass the init prompt so the agent starts
	// autonomously. AppendVibeflowInitPrompt picks the right per-provider
	// argument shape (positional vs `-p` vs `-i`). Always append for
	// vibeflow sessions — even if session_init failed, the agent has MCP
	// access and will call session_init itself on startup.
	if result.SessionType == "vibeflow" {
		initPrompt := BuildVibeflowInitPrompt(m.config.MCPToolName, projectName, result.Persona)
		command = AppendVibeflowInitPrompt(command, provider, initPrompt)
	}
	command, err = WrapOpenShellCommand(command, m.config.OpenShell)
	if err != nil {
		m.logger.Error("wrap openshell command (provider=%s): %v", provider, err)
		return sessionsMsg{err: err}
	}

	// Ensure all agent-specific markdown docs exist in the working directory
	// so any provider session picks up vibeflow session rules on startup.
	if result.SessionType == "vibeflow" {
		for _, docFile := range EnsureAllAgentDocs(workDir) {
			m.logger.Info("copied agent doc %s to %s", docFile, workDir)
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
	m.logger.Info("session created: %s (provider=%s, workdir=%s, command=%q)", tmuxName, provider, workDir, redactCommandSecrets(command))

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
		_ = WriteSessionFileIfNeeded(workDir, result.Persona, sessionFileID)
	}

	// Persist metadata.
	sessionMeta := SessionMeta{
		Name:              name,
		TmuxSession:       tmuxName,
		Provider:          provider,
		Project:           projectName,
		Persona:           result.Persona,
		Branch:            branch,
		WorktreePath:      worktreePath,
		WorkingDir:        workDir,
		VibeFlowSessionID: vibeflowSessionID,
		SessionType:       result.SessionType,
		SkipPermissions:   result.SkipPermissions,
		LLMGatewayEnabled: result.LLMGatewayEnabled,
		MCPToolName:       m.config.MCPToolName,
		OpenShell:         openShellMeta(m.config.OpenShell),
		CreatedAt:         time.Now(),
	}
	if m.store != nil {
		_ = m.store.Add(sessionMeta)
	}
	if m.cache != nil {
		_ = m.cache.Add(sessionMeta)
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
	case ViewRestart:
		return m.restartSelect.View()
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
			hintStyle.Render("  See "+RootDir()+"/vibeflow-cli.log for details")
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
		keys := fmt.Sprintf("n: new  enter: %s  m: project wb  M: all wb  d: delete  b: switch  D: detach  g: group  w: worktrees  ?: help  q: quit", enterHint)
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

	borderStyle := oceanBorder()
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
	groupHeaderStyle := lipgloss.NewStyle().Bold(true).Foreground(oceanMuted)

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
			b.WriteString(selectedStyle.Width(width).Render(iconActive + " " + header))
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
	case "exited":
		indicator = "●"
		indStyle = statusError
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
		b.WriteString(selectedStyle.Width(width).Render(iconActive + " " + indent + line))
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
		icon := PersonaCompactIcon(s.Persona)
		if icon != "" {
			parts = append(parts, lipgloss.NewStyle().Foreground(PersonaColor(s.Persona)).Render(icon)+" "+s.Persona)
		} else {
			parts = append(parts, s.Persona)
		}
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
	valueStyle := lipgloss.NewStyle().Foreground(oceanForeground)

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

	// Gateway env wiring (gateway mode only). Re-derived from current config
	// rather than persisted — BuildLLMGatewayEnv is deterministic per provider.
	// Secret-bearing values are masked with the same allowlist used for
	// spawn-log redaction (isSecretEnvKey).
	if s.LLMGatewayEnabled {
		row("Gateway", "enabled")
		env := BuildLLMGatewayEnv(s.Provider, m.config.ServerURL, m.config.APIToken)
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		envStyle := lipgloss.NewStyle().Foreground(dimColor)
		for _, k := range keys {
			v := env[k]
			if isSecretEnvKey(k) {
				v = "<redacted>"
			}
			b.WriteString(envStyle.Render(truncate("  "+k+"="+v, width)))
			b.WriteString("\n")
		}
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
		outputStyle := lipgloss.NewStyle().Foreground(oceanForeground)
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
	keyStyle := lipgloss.NewStyle().Foreground(oceanPrimary).Width(16)
	descStyle := lipgloss.NewStyle().Foreground(oceanMuted)
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)

	var b strings.Builder
	b.WriteString(catStyle.Render("Navigation"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  j / k") + descStyle.Render("Move down / up") + "\n")
	b.WriteString(keyStyle.Render("  enter") + descStyle.Render("Attach to session") + "\n")
	b.WriteString(keyStyle.Render("  m") + descStyle.Render("Workbench: this project's sessions, native view") + "\n")
	b.WriteString(keyStyle.Render("  M") + descStyle.Render("Workbench: all projects (Ctrl-b n/p to switch)") + "\n")
	b.WriteString(keyStyle.Render("  g") + descStyle.Render("Toggle flat / grouped view") + "\n")
	b.WriteString("\n")

	b.WriteString(catStyle.Render("Session Management"))
	b.WriteString("\n")
	b.WriteString(keyStyle.Render("  n") + descStyle.Render("New session (wizard)") + "\n")
	b.WriteString(keyStyle.Render("  d") + descStyle.Render("Delete session") + "\n")
	b.WriteString(keyStyle.Render("  b") + descStyle.Render("Switch branch") + "\n")
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
		Border(oceanBorder()).
		BorderForeground(accentColor).
		Padding(1, 2)

	popup := popupStyle.Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup)
}

// Provider color-coded dots — distinct hues drawn from the Ocean palette
// (theme.go). The provider glyph plus these keep providers distinguishable.
var providerColors = map[string]lipgloss.Color{
	"claude": oceanWarning,   // sandy
	"codex":  oceanAccent,    // seafoam
	"cursor": oceanPrimary,   // sky
	"gemini": oceanSecondary, // deep blue
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
	case "exited":
		return statusError.Render("exited")
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
