package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"

	"vibeflow-cli/sessionid"
)

// veraBinding is the repository a Vera session listens for. Its harness is the
// session's Provider, its model the session's Model (empty: harness default)
// and its checkout the session's WorkingDir.
type veraBinding struct {
	ProjectID        int64  `json:"project_id"`
	RepositoryLinkID int64  `json:"repository_link_id"`
	GitProvider      string `json:"git_provider"`
	RunnerName       string `json:"runner_name"`
}

// veraExecutable is the CLI a Vera session's listener runs; tests substitute a
// freshly built binary for the test executable.
var veraExecutable = os.Executable

type veraLaunchRequestedMsg struct{ result WizardResult }

// veraLaunchedMsg reports a Vera session launch. existing names the live Vera
// session that already listens for the repository, which is attached instead.
type veraLaunchedMsg struct {
	err      error
	existing string
}

// veraLaunchInputMsg means the binding needs a choice the wizard cannot make:
// an ambiguous repository link or a checkout that does not match the link.
type veraLaunchInputMsg struct {
	options reviewWatchOptions
	input   *reviewStartupInput
}

func veraOptions(meta SessionMeta) reviewWatchOptions {
	b := meta.Vera
	return reviewWatchOptions{Project: strconv.FormatInt(b.ProjectID, 10), ProjectID: b.ProjectID, Repository: meta.WorkingDir, RepositoryLinkID: b.RepositoryLinkID, GitProvider: b.GitProvider, Provider: meta.Provider, Model: meta.Model, Kind: "local", Name: b.RunnerName}
}

// veraListenerCommand is the foreground review-watch a Vera session runs.
// --cra is the explicit invocation consent. Credentials come from the config
// file, so no token appears on the command line or in the session env.
func veraListenerCommand(meta SessionMeta) (string, error) { return veraCommand(meta, false) }

// veraHistoryCommand is the read-only review list beside the listener.
func veraHistoryCommand(meta SessionMeta) (string, error) { return veraCommand(meta, true) }

func veraCommand(meta SessionMeta, history bool) (string, error) {
	bin, err := veraExecutable()
	if err != nil {
		return "", err
	}
	root, err := filepath.Abs(RootDir())
	if err != nil {
		return "", err
	}
	config := flagConfigPath
	if config == "" {
		config = ConfigPath()
	}
	if config, err = filepath.Abs(config); err != nil {
		return "", err
	}
	o := veraOptions(meta)
	args := []string{bin, "--cra", "--root", root, "--config", config, "review-watch"}
	if history {
		args = append(args, "--history")
	}
	args = append(args, "--project", o.Project, "--repo", o.Repository, "--repository-link", strconv.FormatInt(o.RepositoryLinkID, 10), "--git-provider", o.GitProvider, "--provider", o.Provider)
	if o.Model != "" {
		args = append(args, "--model", o.Model)
	}
	args = append(args, "--name", o.Name)
	// exec: the process itself gets the hangup when its session is deleted.
	return "exec " + shellJoin(args), nil
}

// veraPendingReceipt is the review a Vera listener holds, from the durable
// runner state it recovers from; nil while it listens.
func veraPendingReceipt(serverURL string, o reviewWatchOptions) *reviewReceipt {
	data, err := os.ReadFile(filepath.Join(RootDir(), "review-runners", reviewBackgroundID(serverURL, o), "state.json"))
	var state reviewRunnerState
	if err != nil || json.Unmarshal(data, &state) != nil {
		return nil
	}
	return state.Pending
}

// veraRowStatus reads what a Vera session's listener is doing.
func veraRowStatus(meta SessionMeta, serverURL string, paneDead bool) (status, work string) {
	if paneDead {
		return "stopped", "stopped"
	}
	if p := veraPendingReceipt(serverURL, veraOptions(meta)); p != nil {
		if e := p.Execution; e != nil {
			return "reviewing", "reviewing " + reviewPRLabel(e.Review)
		}
		return "reviewing", "claiming a PR review"
	}
	return "listening", "listening"
}

// beginVeraLaunch resolves the repository binding for the harness chosen in
// the wizard's Provider step, then starts Vera's tmux session. Coding personas
// selected alongside Vera launch after it with their own settings.
func (m Model) beginVeraLaunch(result WizardResult) (tea.Model, tea.Cmd) {
	if !m.craEnabled {
		m.err = fmt.Errorf("start this CLI with --cra to launch Vera")
		return m, nil
	}
	provider := result.PersonaProviders["code_reviewer"]
	if provider == "" {
		provider = result.ProviderKey
	}
	if !reviewHarnessSupported(provider) {
		m.err = fmt.Errorf("Vera cannot run harness %q; choose one of: %s", provider, strings.Join(reviewHarnessKeys, ", "))
		return m, nil
	}
	personas := result.Personas
	if len(personas) == 0 {
		personas = []string{result.Persona}
	}
	var coding []string
	for _, persona := range personas {
		if persona != "code_reviewer" {
			coding = append(coding, persona)
		}
	}
	if len(coding) > 0 {
		pending := result
		pending.Personas, pending.Persona = coding, coding[0]
		m.veraPending = &pending
	}
	projectName := result.ProjectName
	o := reviewWatchOptions{Project: strconv.FormatInt(result.ProjectID, 10), ProjectID: result.ProjectID, Repository: result.WorkDir, Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute, RepositoryRequestsApproved: true, Provider: provider}
	if result.ProjectID <= 0 {
		o.Project = projectName
	}
	m.veraProjectName = projectName
	cfg := m.config
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		resolved, input, err := resolveReviewStartup(ctx, cfg, o, true)
		if err != nil {
			return veraLaunchedMsg{err: err}
		}
		if input != nil {
			return veraLaunchInputMsg{options: resolved, input: input}
		}
		return m.launchVeraSession(resolved, projectName)
	}
}

// launchVeraSession creates Vera's tmux session through the same machinery as
// every other persona. A live Vera session for the same repository is reused.
func (m Model) launchVeraSession(o reviewWatchOptions, projectName string) tea.Msg {
	meta := SessionMeta{Provider: o.Provider, Project: projectName, ProjectID: o.ProjectID, Persona: "code_reviewer", WorkingDir: o.Repository, SessionType: "vibeflow", Model: o.Model, Vera: &veraBinding{ProjectID: o.ProjectID, RepositoryLinkID: o.RepositoryLinkID, GitProvider: o.GitProvider, RunnerName: o.Name}, CreatedAt: time.Now()}
	if existing, live, ok := m.veraSessionFor(meta); ok {
		if live {
			if existing.Provider != meta.Provider || existing.Model != meta.Model {
				return veraLaunchedMsg{err: fmt.Errorf("Vera is already listening for this repository with %s in %s; delete that session with d to change its harness", existing.Provider, strings.TrimPrefix(existing.TmuxSession, sessionPrefix))}
			}
			return veraLaunchedMsg{existing: existing.TmuxSession}
		}
		m.killSessionMeta(existing) // Stopped: replace it rather than list two.
	}
	meta.Name = sessionid.GenerateSessionID(o.Repository)
	meta.Branch = GetGitBranch(o.Repository)
	meta.TmuxSession = m.tmux.FullSessionName(meta.Provider, meta.Name)
	if err := startVeraTmuxSession(m.tmux, meta, ""); err != nil {
		return veraLaunchedMsg{err: err}
	}
	if m.store != nil {
		_ = m.store.Add(meta)
	}
	if m.cache != nil {
		_ = m.cache.Add(meta)
	}
	return veraLaunchedMsg{}
}

// startVeraTmuxSession runs the listener in meta's tmux session, or respawns
// it in an exited pane, with the history list beside it.
func startVeraTmuxSession(tmux *TmuxManager, meta SessionMeta, respawnPane string) error {
	command, err := veraListenerCommand(meta)
	if err != nil {
		return err
	}
	if err := tmux.CreateSessionWithOpts(SessionOpts{PaneID: respawnPane, Name: meta.Name, Provider: meta.Provider, WorkDir: meta.WorkingDir, Command: command, Branch: meta.Branch, Project: meta.Project, Persona: meta.Persona}); err != nil {
		return err
	}
	if !tmux.HasSession(meta.TmuxSession) {
		return fmt.Errorf("session %q was not created — tmux has-session check failed", meta.TmuxSession)
	}
	if err := startVeraHistoryPane(tmux, meta, respawnPane); err != nil {
		return err
	}
	// Clicks and the wheel reach the history pane; only this session's option.
	if _, err := tmux.run("set-option", "-t", meta.TmuxSession, "mouse", "on"); err != nil {
		return err
	}
	_ = tmux.BindSessionKeys(meta.TmuxSession)
	return nil
}

// veraHistorySplitArgs splits the listener pane, keeping focus on it, with
// the history list in a new right-hand pane.
func veraHistorySplitArgs(listenerPane, workDir, command string) []string {
	return []string{"split-window", "-h", "-d", "-l", veraHistoryWidth, "-t", listenerPane, "-c", workDir, "-P", "-F", "#{pane_id}", command}
}

// startVeraHistoryPane makes sure the listener's window shows a running
// history list: it respawns an exited one and splits a missing one. A
// listener composed into a workbench keeps its list in its own session.
func startVeraHistoryPane(tmux *TmuxManager, meta SessionMeta, listener string) error {
	var err error
	if listener == "" {
		if listener, err = tmux.agentPaneID(meta.TmuxSession); err != nil {
			return err
		}
	}
	if session, _ := tmux.run("display-message", "-p", "-t", listener, "#{session_name}"); strings.TrimSpace(session) != meta.TmuxSession {
		return nil
	}
	command, err := veraHistoryCommand(meta)
	if err != nil {
		return err
	}
	out, _ := tmux.run("list-panes", "-t", listener, "-F", "#{pane_id}\t#{pane_dead}\t#{@vibeflow_vera_history}")
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		if f := strings.Split(line, "\t"); len(f) == 3 && f[2] == "1" {
			if f[1] != "1" {
				return nil
			}
			_, err := tmux.run("respawn-pane", "-t", f[0], "-c", meta.WorkingDir, command)
			return err
		}
	}
	out, err = tmux.run(veraHistorySplitArgs(listener, meta.WorkingDir, command)...)
	if err != nil {
		return fmt.Errorf("open Vera's review history pane: %w: %s", err, strings.TrimSpace(out))
	}
	_, err = tmux.run("set-option", "-p", "-t", strings.TrimSpace(out), "@vibeflow_vera_history", "1")
	return err
}

// restartVeraSession re-runs the same listener command: in place when the
// pane exited, otherwise in a fresh session.
func restartVeraSession(meta SessionMeta, tmux *TmuxManager, store *Store, cache *SessionCache, recoveryPane string) (SessionMeta, error) {
	target := meta.TmuxSession
	if recoveryPane != "" {
		target = recoveryPane
	}
	respawn := ""
	if pane, _ := tmux.agentPaneID(target); pane != "" && tmux.paneDead(pane) {
		respawn = pane
	}
	if recoveryPane != "" && respawn == "" {
		return SessionMeta{}, fmt.Errorf("pane %q has not exited", recoveryPane)
	}
	if respawn == "" && tmux.HasSession(meta.TmuxSession) {
		if err := tmux.KillSession(meta.TmuxSession); err != nil {
			return SessionMeta{}, err
		}
	}
	if err := startVeraTmuxSession(tmux, meta, respawn); err != nil {
		return SessionMeta{}, err
	}
	if store != nil {
		_ = store.Add(meta)
	}
	if cache != nil {
		_ = cache.Add(meta)
	}
	return meta, nil
}

// veraSessionFor finds the stored Vera session for meta's repository and
// whether its listener pane is still running.
func (m Model) veraSessionFor(meta SessionMeta) (existing SessionMeta, live, ok bool) {
	if m.store == nil {
		return SessionMeta{}, false, false
	}
	metas, _ := m.store.List()
	for _, stored := range metas {
		if b := stored.Vera; b != nil && b.ProjectID == meta.Vera.ProjectID && b.RepositoryLinkID == meta.Vera.RepositoryLinkID && b.GitProvider == meta.Vera.GitProvider {
			sessions, _ := m.tmux.ListSessions()
			for _, s := range sessions {
				if s.Name == stored.TmuxSession {
					return stored, !s.PaneDead, true
				}
			}
			return stored, false, true
		}
	}
	return SessionMeta{}, false, false
}

// finishVeraLaunch returns to the session list, reports the outcome, attaches
// an existing Vera session, and launches coding personas chosen alongside Vera.
func (m Model) finishVeraLaunch(msg veraLaunchedMsg) (tea.Model, tea.Cmd) {
	if msg.err != nil && m.veraPrompt != nil {
		m.veraPrompt.busy, m.veraPrompt.err = false, msg.err
		return m, nil
	}
	m.veraPrompt = nil
	if m.activeView == ViewVeraLaunch {
		m.activeView = ViewSessions
	}
	cmds := []tea.Cmd{m.refreshSessions}
	if msg.err != nil {
		m.err = msg.err
		cmds = append(cmds, tea.Tick(10*time.Second, func(time.Time) tea.Msg { return errClearMsg{} }))
	}
	if name := msg.existing; name != "" {
		cmds = append(cmds, func() tea.Msg { return autoAttachMsg{name: name} })
	}
	if m.veraPending != nil {
		result := *m.veraPending
		m.veraPending = nil
		cmds = append(cmds, func() tea.Msg { return m.launchFromWizard(result) })
	}
	return m, tea.Batch(cmds...)
}

// showVeraLaunchInput opens the small prompt for the one choice the binding
// still needs; the harness and model are already settled.
func (m Model) showVeraLaunchInput(msg veraLaunchInputMsg) (tea.Model, tea.Cmd) {
	m.veraPrompt = &veraPrompt{cfg: m.config, options: msg.options, input: msg.input}
	m.activeView = ViewVeraLaunch
	return m, nil
}

func (m Model) updateVeraLaunch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.veraPrompt == nil {
		m.activeView = ViewSessions
		return m, nil
	}
	next, cmd := m.veraPrompt.Update(msg)
	m.veraPrompt = &next
	if next.cancelled {
		return m.finishVeraLaunch(veraLaunchedMsg{})
	}
	if next.resolved {
		next.resolved = false // Launch once; the prompt stays busy until it reports.
		m.veraPrompt = &next
		o, projectName := next.options, m.veraProjectName
		return m, func() tea.Msg { return m.launchVeraSession(o, projectName) }
	}
	return m, cmd
}

// veraPrompt asks for the one binding input the wizard cannot supply.
type veraPrompt struct {
	cfg       *Config
	options   reviewWatchOptions
	input     *reviewStartupInput
	text      string
	cursor    int
	busy      bool
	err       error
	resolved  bool // The binding is complete; launch the session.
	cancelled bool
}

type veraPromptResolvedMsg struct {
	options reviewWatchOptions
	input   *reviewStartupInput
	err     error
}

func (p veraPrompt) resolve() tea.Cmd {
	cfg, o := p.cfg, p.options
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		o, input, err := resolveReviewStartup(ctx, cfg, o, true)
		return veraPromptResolvedMsg{options: o, input: input, err: err}
	}
}

func (p veraPrompt) Update(msg tea.Msg) (veraPrompt, tea.Cmd) {
	if paste, ok := msg.(tea.PasteMsg); ok {
		msg = tea.KeyPressMsg{Text: paste.Content}
	}
	switch msg := msg.(type) {
	case veraPromptResolvedMsg:
		p.busy, p.err = false, msg.err
		p.options, p.input = msg.options, msg.input
		p.cursor, p.text = 0, ""
		if p.err == nil && p.input == nil {
			p.resolved, p.busy = true, true
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if p.busy {
			return p, nil
		}
		if key == "esc" || (p.err != nil && key == "enter") {
			p.cancelled = true
			return p, nil
		}
		if p.err != nil {
			if key == "r" {
				p.err, p.busy = nil, true
				return p, p.resolve()
			}
			return p, nil
		}
		if p.input == nil {
			return p, nil
		}
		if len(p.input.Choices) > 0 {
			switch key {
			case "up", "k":
				p.cursor = max(0, p.cursor-1)
			case "down", "j":
				p.cursor = min(len(p.input.Choices)-1, p.cursor+1)
			case "enter":
				p.text = p.input.Choices[p.cursor].Value
			}
		} else if key == "backspace" {
			if runes := []rune(p.text); len(runes) > 0 {
				p.text = string(runes[:len(runes)-1])
			}
		} else {
			for _, r := range msg.Text {
				if unicode.IsPrint(r) && len(p.text) < 4096 {
					p.text += string(r)
				}
			}
		}
		value := strings.TrimSpace(p.text)
		if key != "enter" || value == "" {
			return p, nil
		}
		switch p.input.Field {
		case "project":
			p.options.Project, p.options.ProjectID, p.options.RepositoryLinkID = value, 0, 0
		case "repository":
			p.options.Repository, p.options.RepositoryLinkID = value, 0
		case "repository_link":
			provider, id, _ := strings.Cut(value, ":")
			p.options.GitProvider = provider
			p.options.RepositoryLinkID, _ = strconv.ParseInt(id, 10, 64)
		case "model":
			p.options.Model = value
		default:
			p.err = fmt.Errorf("unsupported Vera setup field")
			return p, nil
		}
		p.busy = true
		return p, p.resolve()
	}
	return p, nil
}

func (p veraPrompt) View(width, height int) string {
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	popupWidth := max(1, min(64, width-6))
	contentWidth := max(1, popupWidth-4)
	var b strings.Builder
	b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(reviewSessionLabel) + "\n\n")
	switch {
	case p.busy:
		b.WriteString("Starting Vera...")
	case p.err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(p.err.Error()))
		b.WriteString("\n\nEnter: close  r: retry")
	case p.input != nil:
		b.WriteString(lipgloss.NewStyle().Width(contentWidth).Render(p.input.Message) + "\n\n")
		if len(p.input.Choices) == 0 {
			b.WriteString(ansi.TruncateLeft(p.text, max(0, lipgloss.Width(p.text)-contentWidth+1), "") + "█\n")
		} else {
			visible := max(1, min(5, height-12))
			start := max(0, p.cursor-visible/2)
			for i := start; i < min(len(p.input.Choices), start+visible); i++ {
				prefix := "  "
				if i == p.cursor {
					prefix = "> "
				}
				b.WriteString(ansi.Truncate(prefix+p.input.Choices[i].Label, contentWidth, "…") + "\n")
			}
			b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("%d of %d", p.cursor+1, len(p.input.Choices))) + "\n")
		}
		b.WriteString("\nEnter: continue  Esc: cancel")
	}
	popup := lipgloss.NewStyle().Width(popupWidth).Border(oceanBorder()).BorderForeground(accentColor).Padding(1, 2).Render(b.String())
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup)
}
