//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strconv"
	"strings"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// Vera is part of the normal binary: New Agent always offers her.
func TestVeraAgentPickerAlwaysOffersVera(t *testing.T) {
	cfg := DefaultConfig()
	m := Model{config: cfg, registry: NewProviderRegistry(cfg)}
	next, _ := m.Update(tea.KeyPressMsg{Text: "n"})
	w := next.(Model).wizard
	found := wizardPersonaIndex(w, "code_reviewer")
	if found < 0 {
		t.Fatal("New Agent does not offer Vera")
	}
	w.step = StepTeam
	w.projects = []Project{{ID: 66, Name: "Selected"}}
	w.selectedWorkDir = t.TempDir()
	w.selectedSessionType = 1
	w.selectedPersonas = map[int]bool{found: true}
	updated, _ := w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	w = updated
	if w.done || w.step != StepProvider {
		t.Fatalf("solo Vera must continue to the Provider step: step=%v done=%v", w.step, w.done)
	}
}

// veraWizardFixture is a New Agent wizard at the Team step with Vera alone selected.
// Providers: claude and qwen run Vera, aider is installed but Vera cannot run
// it, cursor is not installed.
func veraWizardFixture(t *testing.T) (WizardModel, int) {
	t.Helper()
	cfg := &Config{Providers: map[string]Provider{
		"aider":  {Name: "Aider", Binary: "sh"},
		"claude": {Name: "Claude", Binary: "sh"},
		"cursor": {Name: "Cursor", Binary: "this-binary-does-not-exist-xyz-123"},
		"qwen":   {Name: "Qwen", Binary: "sh"},
	}}
	w := NewWizardModel(NewProviderRegistry(cfg), ".", nil, nil, "", nil, cfg)
	vera := wizardPersonaIndex(w, "code_reviewer")
	w.step, w.selectedSessionType = StepTeam, 1
	w.projects, w.selectedProject = []Project{{ID: 66, Name: "Selected"}}, 0
	w.selectedWorkDir = t.TempDir()
	w.selectedPersonas = map[int]bool{vera: true}
	return w, vera
}

func wizardKey(t *testing.T, w WizardModel, keys ...tea.KeyPressMsg) WizardModel {
	t.Helper()
	for _, key := range keys {
		w, _ = w.Update(key)
	}
	return w
}

var (
	wizardEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	wizardEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
)

// Vera alone goes Team -> Provider -> Confirm in the ordinary wizard.
func TestVeraWizardReusesProviderAndConfirmSteps(t *testing.T) {
	w, _ := veraWizardFixture(t)
	w = wizardKey(t, w, wizardEnter)
	if w.done || w.step != StepProvider {
		t.Fatalf("Team did not lead to Provider: step=%v done=%v", w.step, w.done)
	}
	view := w.View()
	for _, want := range []string{"Directory", "Type", "Project", "Team", "[Provider]", "Confirm", "Claude", "Qwen"} {
		if !strings.Contains(view, want) {
			t.Fatalf("provider step missing %q:\n%s", want, view)
		}
	}
	for _, skipped := range []string{"Env", "Routing", "Branch", "Worktree", "Permissions"} {
		if strings.Contains(view, skipped) {
			t.Fatalf("Vera step line shows skipped step %q:\n%s", skipped, view)
		}
	}
	// Vera cannot run aider, and cursor is not installed: neither is selectable.
	for _, key := range []string{"aider", "cursor"} {
		w.cursor = providerIdxByKey(t, w, key)
		if next := wizardKey(t, w, wizardEnter); next.step != StepProvider || next.editingBinary {
			t.Fatalf("%s was selectable for Vera: step=%v editingBinary=%v", key, next.step, next.editingBinary)
		}
	}
	w.cursor = providerIdxByKey(t, w, "qwen")
	w = wizardKey(t, w, wizardEnter)
	if w.step != StepConfirm {
		t.Fatalf("provider did not lead to Confirm: %v", w.step)
	}
	view = w.View()
	for _, want := range []string{"Selected", w.selectedWorkDir, "Qwen", "harness default", "@vibeflow review", "own tmux session", "until you delete the session", "full permissions", "disposable worktree"} {
		if !strings.Contains(view, want) {
			t.Fatalf("confirm missing %q:\n%s", want, view)
		}
	}
	if w = wizardKey(t, w, wizardEsc); w.step != StepProvider {
		t.Fatalf("esc from Vera confirm went to %v", w.step)
	}
	w = wizardKey(t, w, wizardEnter, wizardEnter)
	r := w.Result()
	if !w.done || r.ProviderKey != "qwen" || r.Model != "" || r.ProjectID != 66 || r.ProjectName != "Selected" || r.WorkDir != w.selectedWorkDir || !reflect.DeepEqual(r.Personas, []string{"code_reviewer"}) {
		t.Fatalf("wrong Vera result: done=%v %+v", w.done, r)
	}
}

// In a mixed team Vera's provider row only offers harnesses Vera can run.
func TestVeraWizardTeamRowOnlyOffersVeraHarnesses(t *testing.T) {
	w, vera := veraWizardFixture(t)
	w.selectedPersonas[0] = true // developer
	w = wizardKey(t, w, wizardEnter)
	if w.step != StepProvider || !w.teamModeProvider() {
		t.Fatalf("mixed team not in team provider mode: %v", w.step)
	}
	w.selectedProvider = providerIdxByKey(t, w, "aider") // coding default Vera cannot run
	if next := wizardKey(t, w, wizardEnter); next.step != StepProvider {
		t.Fatal("Vera inherited a harness it cannot run")
	}
	if !strings.Contains(w.View(), "Vera cannot run") {
		t.Fatalf("no reason shown for the blocked Vera row:\n%s", w.View())
	}
	w.cursor = 2 // developer row is 1, Vera row is 2
	seen := map[string]bool{}
	for range 6 {
		w = wizardKey(t, w, tea.KeyPressMsg{Code: tea.KeyRight})
		seen[w.providers[w.resolvedProviderForPersona(vera)].key] = true
	}
	if seen["aider"] || seen["cursor"] || !seen["claude"] || !seen["qwen"] {
		t.Fatalf("Vera row cycled through %v", seen)
	}
	w.personaProviderIdx[vera] = providerIdxByKey(t, w, "claude")
	if next := wizardKey(t, w, wizardEnter); next.step == StepProvider {
		t.Fatal("valid Vera harness did not continue the coding setup")
	}
	w.step = StepConfirm
	if !strings.Contains(w.View(), "@vibeflow review") {
		t.Fatalf("team confirm does not explain Vera:\n%s", w.View())
	}
	w, _ = w.advance()
	if r := w.Result(); r.ProviderKey != "aider" || r.PersonaProviders["code_reviewer"] != "claude" {
		t.Fatalf("Vera provider lost: %+v", r)
	}
}

// newVeraFixture serves the review API for project 66 with links 7 (acme/repo)
// and 8 (acme/second), saves the config the listener reads, and counts runner
// registrations, idle work polls and DELETEs.
func newVeraFixture(t *testing.T) (cfg *Config, repo string, registrations, polls, stops *atomic.Int64) {
	t.Helper()
	withTempRoot(t)
	repo, _ = reviewTestRepo(t)
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	registrations, polls, stops = new(atomic.Int64), new(atomic.Int64), new(atomic.Int64)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects"):
			fmt.Fprint(w, `[{"id":66,"name":"Selected"}]`)
		case strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
			fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/second"}],"supported_runner_capabilities":["repository_review_v1"]}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			_ = json.NewDecoder(r.Body).Decode(&body)
			body["user_id"] = 42
			registrations.Add(1)
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pr-review-summaries"): // Vera's history pane.
			fmt.Fprint(w, `{"summaries":[]}`)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
			polls.Add(1)
			fmt.Fprint(w, `{"reviews":[]}`)
		case r.Method == "DELETE":
			stops.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg = DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "vera-api-canary"
	cfg.Providers["claude"] = Provider{Name: "Claude", Binary: provider}
	cfg.Providers["codex"] = Provider{Name: "Codex", Binary: provider}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	return cfg, repo, registrations, polls, stops
}

// veraTmuxModel is a TUI model on a private tmux socket.
func veraTmuxModel(t *testing.T, cfg *Config) Model {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager(fmt.Sprintf("vftest-vera-%d-%d", os.Getpid(), time.Now().UnixNano()%1e9))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	return Model{config: cfg, tmux: tm, registry: NewProviderRegistry(cfg), logger: NewLogger(), store: NewStore(), cache: NewSessionCache(), repoRootCache: map[string]string{}}
}

// paneCommand is the command tmux started a session's pane with.
func paneCommand(t *testing.T, tm *TmuxManager, session string) string {
	t.Helper()
	out, err := tm.run("display-message", "-p", "-t", session, "#{pane_start_command}")
	if err != nil {
		t.Fatal(err)
	}
	return strings.Trim(strings.TrimSpace(out), `"`) // tmux quotes the stored command.
}

// setVeraExecutable points Vera sessions at binary instead of the test binary.
func setVeraExecutable(t *testing.T, binary string) {
	t.Helper()
	orig := selfExecutable
	selfExecutable = func() (string, error) { return binary, nil }
	t.Cleanup(func() { selfExecutable = orig })
}

// launchVera drives the wizard result for Vera through the TUI and returns the
// message the launch produced.
func launchVera(t *testing.T, m Model, result WizardResult) (Model, tea.Msg) {
	t.Helper()
	next, cmd := m.Update(m.launchFromWizard(result))
	m = next.(Model)
	if cmd == nil {
		t.Fatalf("Vera launch produced no command; err=%v", m.err)
	}
	return m, cmd()
}

func veraResult(repo, provider string) WizardResult {
	return WizardResult{SessionType: "vibeflow", Persona: "code_reviewer", Personas: []string{"code_reviewer"}, ProjectID: 66, ProjectName: "Selected", WorkDir: repo, ProviderKey: provider}
}

func veraRunnerName() string {
	name, _ := os.Hostname()
	if !reviewStartupText(name, 100) {
		name = "Review runner"
	}
	return name
}

func storedVera(t *testing.T) []SessionMeta {
	t.Helper()
	metas, err := NewStore().List()
	if err != nil {
		t.Fatal(err)
	}
	var vera []SessionMeta
	for _, meta := range metas {
		if meta.Vera != nil {
			vera = append(vera, meta)
		}
	}
	return vera
}

// Confirming Vera creates an ordinary persona tmux session whose command is the
// foreground listener for exactly the chosen binding and harness.
func TestVeraWizardConfirmCreatesListenerSession(t *testing.T) {
	cfg, repo, registrations, _, _ := newVeraFixture(t)
	fake := filepath.Join(t.TempDir(), "vibeflow")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, fake)
	m := veraTmuxModel(t, cfg)
	m, msg := launchVera(t, m, veraResult(repo, "claude"))
	if launched, ok := msg.(veraLaunchedMsg); !ok || launched.err != nil || launched.existing != "" {
		t.Fatalf("Vera did not launch: %#v", msg)
	}
	metas := storedVera(t)
	if len(metas) != 1 {
		t.Fatalf("want one stored Vera session, got %+v", metas)
	}
	meta := metas[0]
	want := SessionMeta{Name: meta.Name, TmuxSession: sessionPrefix + "claude-" + meta.Name, Provider: "claude", Project: "Selected", ProjectID: 66, Persona: "code_reviewer", Branch: meta.Branch, WorkingDir: repo, SessionType: "vibeflow", Vera: &veraBinding{ProjectID: 66, RepositoryLinkID: 7, GitProvider: "github", RunnerName: veraRunnerName()}, CreatedAt: meta.CreatedAt}
	if !reflect.DeepEqual(meta, want) {
		t.Fatalf("stored Vera session\n got %+v\nwant %+v", meta, want)
	}
	root, _ := filepath.Abs(RootDir())
	command := "exec " + shellJoin([]string{fake, "--root", root, "--config", filepath.Join(root, "config.yaml"), "review-watch", "--project", "66", "--repo", repo, "--repository-link", "7", "--git-provider", "github", "--provider", "claude", "--name", veraRunnerName()})
	started := paneCommand(t, m.tmux, meta.TmuxSession)
	if started != command {
		t.Fatalf("pane command\n got %q\nwant %q", started, command)
	}
	if strings.Contains(started, cfg.APIToken) {
		t.Fatal("listener command carries the API token")
	}
	history := "exec " + shellJoin([]string{fake, "--root", root, "--config", filepath.Join(root, "config.yaml"), "review-watch", "--history", "--project", "66", "--repo", repo, "--repository-link", "7", "--git-provider", "github", "--provider", "claude", "--name", veraRunnerName()})
	assertVeraPanes(t, m.tmux, meta.TmuxSession, command, history)
	if _, err := os.Stat(filepath.Join(repo, ".vibeflow-session-code_reviewer")); !os.IsNotExist(err) {
		t.Fatal("Vera wrote coding-agent session state")
	}
	if registrations.Load() != 0 {
		t.Fatal("the TUI itself registered a runner; only the listener may")
	}
	// Restart re-runs the same command in the exited pane.
	if _, err := m.tmux.run("respawn-pane", "-k", "-t", meta.TmuxSession, "true"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if dead, _ := m.tmux.run("display-message", "-p", "-t", meta.TmuxSession, "#{pane_dead}"); strings.TrimSpace(dead) == "1" {
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if _, err := RestartSession(meta, cfg, m.tmux, m.store, m.cache, m.registry); err != nil {
		t.Fatal(err)
	}
	if again := paneCommand(t, m.tmux, meta.TmuxSession); again != command {
		t.Fatalf("restart ran %q, want %q", again, command)
	}
	assertVeraPanes(t, m.tmux, meta.TmuxSession, command, history)
	// Restarting a live session recreates both panes.
	if _, err := RestartSession(meta, cfg, m.tmux, m.store, m.cache, m.registry); err != nil {
		t.Fatal(err)
	}
	assertVeraPanes(t, m.tmux, meta.TmuxSession, command, history)
}

// assertVeraPanes checks Vera's layout: the listener on the left (~70%) with
// keyboard focus and the launch identity, the history list on the right (~30%).
func assertVeraPanes(t *testing.T, tm *TmuxManager, session, listener, history string) {
	t.Helper()
	out, err := tm.run("list-panes", "-t", session, "-F", "#{pane_left}\t#{pane_width}\t#{window_width}\t#{pane_active}\t#{@vibeflow_session}\t#{pane_start_command}")
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) != 2 {
		t.Fatalf("want two Vera panes, got:\n%s", out)
	}
	left, right := strings.SplitN(lines[0], "\t", 6), strings.SplitN(lines[1], "\t", 6)
	if left[0] != "0" || left[3] != "1" || left[4] != session || strings.Trim(left[5], `"`) != listener {
		t.Fatalf("left pane is not the focused listener: %q", lines[0])
	}
	width, _ := strconv.Atoi(right[1])
	window, _ := strconv.Atoi(right[2])
	if right[0] == "0" || right[3] != "0" || right[4] != "" || strings.Trim(right[5], `"`) != history || window == 0 || width*100/window < 27 || width*100/window > 33 {
		t.Fatalf("right pane is not the 30%% history list: %q", lines[1])
	}
}

// The session list shows Vera like any persona, with the listener's state.
func TestVeraSessionRowShowsListenerStatus(t *testing.T) {
	cfg, repo, _, _, _ := newVeraFixture(t)
	fake := filepath.Join(t.TempDir(), "vibeflow")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, fake)
	m := veraTmuxModel(t, cfg)
	m, _ = launchVera(t, m, veraResult(repo, "claude"))
	meta := storedVera(t)[0]
	row := func() SessionRow {
		t.Helper()
		msg := m.refreshSessions().(sessionsMsg)
		if msg.err != nil || len(msg.sessions) != 1 {
			t.Fatalf("session list %+v %v", msg.sessions, msg.err)
		}
		next, _ := m.Update(msg)
		m = next.(Model)
		return msg.sessions[0]
	}
	rendered := func(s SessionRow) string {
		var b strings.Builder
		m.renderSessionRow(&b, s, 1, 0, 120, "")
		return ansi.Strip(b.String())
	}
	if r := row(); r.Persona != "code_reviewer" || r.Provider != "claude" || r.WorkingDir != repo || r.Project != "Selected" || r.Status != "listening" {
		t.Fatalf("idle Vera row %+v", r)
	} else if text := rendered(r); !strings.Contains(text, "Vera · Code Reviewer · "+filepath.Base(repo)) || !strings.Contains(text, "Selected") || !strings.Contains(text, "listening") || strings.Contains(text, "claude-") {
		t.Fatalf("idle Vera row renders as:\n%s", text)
	}
	dir := filepath.Join(RootDir(), "review-runners", reviewBackgroundID(cfg.ServerURL, veraOptions(meta)))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	e := &reviewExecution{Review: reviewJob{ID: "job", Number: 12}}
	if err := saveReviewJSON(filepath.Join(dir, "state.json"), reviewRunnerState{ID: "runner", Pending: &reviewReceipt{JobID: "job", Execution: e}}); err != nil {
		t.Fatal(err)
	}
	if r := row(); r.Status != "reviewing" || !strings.Contains(rendered(r), "reviewing PR #12") {
		t.Fatalf("reviewing Vera row %+v:\n%s", r, rendered(r))
	}
	// Status follows the listener even while the history pane has focus.
	listener, err := m.tmux.agentPaneID(meta.TmuxSession)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := m.tmux.run("select-pane", "-R", "-t", listener); err != nil {
		t.Fatal(err)
	}
	if _, err := m.tmux.run("respawn-pane", "-k", "-t", listener, "true"); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(5 * time.Second)
	for r := row(); r.Status != "stopped"; r = row() {
		if time.Now().After(deadline) {
			t.Fatalf("exited Vera row %+v", r)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

// Choosing Vera again for the same repository attaches the live session; a
// different harness is refused instead of silently keeping the old one.
func TestVeraSecondSelectionReusesLiveSession(t *testing.T) {
	cfg, repo, _, _, _ := newVeraFixture(t)
	fake := filepath.Join(t.TempDir(), "vibeflow")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, fake)
	m := veraTmuxModel(t, cfg)
	m, _ = launchVera(t, m, veraResult(repo, "claude"))
	first := storedVera(t)[0]
	m, msg := launchVera(t, m, veraResult(repo, "claude"))
	if launched, ok := msg.(veraLaunchedMsg); !ok || launched.err != nil || launched.existing != first.TmuxSession {
		t.Fatalf("second selection did not reuse %s: %#v", first.TmuxSession, msg)
	}
	next, cmd := m.Update(msg)
	m = next.(Model)
	attached := false
	for _, c := range batchCmds(cmd) {
		if a, ok := c().(autoAttachMsg); ok && a.name == first.TmuxSession {
			attached = true
		}
	}
	if !attached {
		t.Fatal("existing Vera session was not attached")
	}
	_, msg = launchVera(t, m, veraResult(repo, "codex"))
	if launched, ok := msg.(veraLaunchedMsg); !ok || launched.err == nil || !strings.Contains(launched.err.Error(), "already listening") || !strings.Contains(launched.err.Error(), "claude") {
		t.Fatalf("harness change was not reported: %#v", msg)
	}
	sessions, _ := m.tmux.ListSessions()
	if len(sessions) != 1 || len(storedVera(t)) != 1 {
		t.Fatalf("duplicate Vera sessions: %+v", sessions)
	}
}

// batchCmds flattens a tea.Batch into its commands.
func batchCmds(cmd tea.Cmd) []tea.Cmd {
	if cmd == nil {
		return nil
	}
	if batch, ok := cmd().(tea.BatchMsg); ok {
		var cmds []tea.Cmd
		for _, c := range batch {
			cmds = append(cmds, batchCmds(c)...)
		}
		return cmds
	}
	return []tea.Cmd{cmd}
}

// Deleting the session with d stops the real listener, which deregisters.
func TestVeraSessionDeleteStopsListener(t *testing.T) {
	cfg, repo, registrations, polls, stops := newVeraFixture(t)
	setVeraExecutable(t, builtVibeflow(t))
	m := veraTmuxModel(t, cfg)
	m, _ = launchVera(t, m, veraResult(repo, "claude"))
	meta := storedVera(t)[0]
	waitFor := func(what string, ok func() bool) {
		t.Helper()
		deadline := time.Now().Add(15 * time.Second)
		for !ok() {
			if time.Now().After(deadline) {
				pane, _ := m.tmux.run("capture-pane", "-p", "-t", meta.TmuxSession)
				t.Fatalf("missing %s; pane:\n%s", what, pane)
			}
			time.Sleep(20 * time.Millisecond)
		}
	}
	waitFor("listener registration and idle poll", func() bool { return registrations.Load() == 1 && polls.Load() >= 1 })
	pane, _ := m.tmux.run("capture-pane", "-p", "-J", "-t", meta.TmuxSession) // -J: the 70% pane wraps.
	if !strings.Contains(pane, reviewListeningLine) || strings.Contains(pane, cfg.APIToken) {
		t.Fatalf("listener pane:\n%s", pane)
	}
	pid, err := m.tmux.run("display-message", "-p", "-t", meta.TmuxSession, "#{pane_pid}")
	if err != nil {
		t.Fatal(err)
	}
	next, _ := m.Update(m.refreshSessions())
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Text: "d"})
	next, _ = next.(Model).Update(tea.KeyPressMsg{Text: "y"})
	m = next.(Model)
	waitFor("runner DELETE", func() bool { return stops.Load() == 1 })
	listener, _ := strconv.Atoi(strings.TrimSpace(pid))
	waitFor("listener exit", func() bool { return syscall.Kill(listener, 0) != nil })
	if m.tmux.HasSession(meta.TmuxSession) {
		t.Fatal("tmux session survived delete")
	}
}

// Vera is not a coding agent: the wizard routes her to the Vera launch, and
// the coding-session path refuses her outright.
func TestVeraSoloLaunchNeverStartsCodingLoop(t *testing.T) {
	cfg := DefaultConfig()
	m := Model{config: cfg}
	msg := m.launchFromWizard(WizardResult{SessionType: "vibeflow", Persona: "code_reviewer", Personas: []string{"code_reviewer"}})
	if _, ok := msg.(veraLaunchRequestedMsg); !ok {
		t.Fatalf("Vera must take the Vera launch path, got %#v", msg)
	}
	msg = m.executeLaunch(WizardResult{SessionType: "vibeflow", Persona: "code_reviewer"})
	if failure, ok := msg.(sessionsMsg); !ok || failure.err == nil || !strings.Contains(failure.err.Error(), "not a coding agent") {
		t.Fatalf("coding launch must refuse Vera, got %#v", msg)
	}
}

func TestVeraHeadlessNeverStartsCodingLoop(t *testing.T) {
	withTempRoot(t)
	cmd := launchCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--persona", "code_reviewer"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "review-watch") {
		t.Fatalf("expected explicit review-watch direction, got %v", err)
	}
}

func TestVeraHeadlessTeamSelectionIsExplicit(t *testing.T) {
	withTempRoot(t)
	cmd := launchCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetOut(io.Discard)
	cmd.SetArgs([]string{"--personas", "developer,code_reviewer"})
	if err := cmd.Execute(); err == nil || !strings.Contains(err.Error(), "review-watch") {
		t.Fatalf("team Vera entered coding loop: %v", err)
	}
}

// A team with Vera starts Vera's session and the coding personas with their
// own providers; removing Vera must keep the developer's override.
func TestVeraTeamLaunchStartsVeraAndCodingSessions(t *testing.T) {
	cfg, repo, _, _, _ := newVeraFixture(t)
	fake := filepath.Join(t.TempDir(), "vibeflow")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, fake)
	agents := t.TempDir()
	for _, key := range []string{"qwen", "codex"} {
		binary := filepath.Join(agents, key)
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 300\n"), 0700); err != nil {
			t.Fatal(err)
		}
		cfg.Providers[key] = Provider{Name: key, Binary: binary, LaunchTemplate: "{{.Binary}}"}
	}
	providers := make(map[string]Provider, len(cfg.Providers))
	for key, value := range cfg.Providers {
		providers[key] = value
	}
	m := veraTmuxModel(t, cfg)
	result := WizardResult{SessionType: "vibeflow", Personas: []string{"developer", "code_reviewer"}, Persona: "developer", ProviderKey: "qwen", Provider: cfg.Providers["qwen"], PersonaProviders: map[string]string{"developer": "codex", "code_reviewer": "claude"}, ProjectID: 66, ProjectName: "Selected", WorkDir: repo, WorktreeChoice: WorktreeCurrent}
	m, msg := launchVera(t, m, result)
	if m.veraPending == nil || len(m.veraPending.Personas) != 1 || m.veraPending.Persona != "developer" {
		t.Fatalf("coding launch altered %+v", m.veraPending)
	}
	next, cmd := m.Update(msg)
	m = next.(Model)
	if m.veraPending != nil || m.activeView != ViewSessions {
		t.Fatal("starting Vera lost the selected coding agents")
	}
	for _, c := range batchCmds(cmd) {
		if failure, ok := c().(sessionsMsg); ok && failure.err != nil {
			t.Fatal(failure.err)
		}
	}
	sessions, err := m.tmux.run("list-sessions", "-F", "#{session_name}")
	if err != nil || !strings.Contains(sessions, sessionPrefix+"codex-") || !strings.Contains(sessions, sessionPrefix+"claude-") || strings.Contains(sessions, sessionPrefix+"qwen-") {
		t.Fatalf("want Vera on claude and developer on codex: %v %q", err, sessions)
	}
	if !reflect.DeepEqual(cfg.Providers, providers) {
		t.Fatal("Vera rewrote coding providers")
	}
}

// A harness Vera cannot run is refused before any server call.
func TestVeraUnsupportedHarnessIsRefused(t *testing.T) {
	cfg, repo, registrations, _, _ := newVeraFixture(t)
	m := Model{config: cfg}
	next, cmd := m.beginVeraLaunch(WizardResult{ProjectID: 66, WorkDir: repo, Persona: "code_reviewer", ProviderKey: "aider"})
	if next.(Model).err == nil || cmd != nil || registrations.Load() != 0 {
		t.Fatal("unsupported harness was not refused")
	}
}

// Only a checkout that does not match the linked repository needs more input;
// the small prompt asks for the checkout and never for a model.
func TestVeraCheckoutMismatchAsksForCheckoutOnly(t *testing.T) {
	cfg, repo, _, _, _ := newVeraFixture(t)
	wrong, _ := reviewTestRepo(t)
	reviewTestGit(t, wrong, "remote", "set-url", "origin", "https://github.com/acme/unlinked.git")
	fake := filepath.Join(t.TempDir(), "vibeflow")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\nexec sleep 300\n"), 0700); err != nil {
		t.Fatal(err)
	}
	setVeraExecutable(t, fake)
	m := veraTmuxModel(t, cfg)
	m, msg := launchVera(t, m, veraResult(wrong, "claude"))
	next, _ := m.Update(msg)
	m = next.(Model)
	if m.activeView != ViewVeraLaunch || m.veraPrompt == nil || m.veraPrompt.input == nil || m.veraPrompt.input.Field != "repository" {
		t.Fatalf("checkout mismatch not actionable: view=%v prompt=%+v", m.activeView, m.veraPrompt)
	}
	if view := m.viewContent(); !strings.Contains(view, "acme/repo") {
		t.Fatalf("checkout prompt does not name the linked repository:\n%s", view)
	}
	if len(storedVera(t)) != 0 {
		t.Fatal("mismatched checkout started a session")
	}
	next, _ = m.Update(tea.PasteMsg{Content: repo})
	next, cmd := next.(Model).Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	for cmd != nil {
		msg := cmd()
		if _, ok := msg.(veraLaunchedMsg); ok {
			next, _ = m.Update(msg)
			m = next.(Model)
			break
		}
		next, cmd = m.Update(msg)
		m = next.(Model)
	}
	if metas := storedVera(t); len(metas) != 1 || metas[0].WorkingDir != repo || m.activeView != ViewSessions || m.veraPrompt != nil {
		t.Fatalf("corrected checkout did not start Vera: %+v view=%v", metas, m.activeView)
	}
}

func TestVeraAmbiguousLinksRequireSelection(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/projects") {
			fmt.Fprint(w, `[{"id":66,"name":"Selected"}]`)
			return
		}
		fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/repo"}]}`)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "fixture"
	o := reviewWatchOptions{Project: "66", Repository: repo, Provider: "claude"}
	_, input, err := resolveReviewStartup(context.Background(), cfg, o, true)
	if err != nil || input == nil || input.Field != "repository_link" || len(input.Choices) != 2 {
		t.Fatalf("ambiguous binding silently picked: input=%+v err=%v", input, err)
	}
}

// A TUI whose binary was deleted after it started must refuse to start Vera
// instead of leaving a dead pane ("no such file or directory", status 127).
func TestVeraLaunchRefusesRemovedBinary(t *testing.T) {
	withTempRoot(t)
	setVeraExecutable(t, filepath.Join(t.TempDir(), "vibeflow-removed"))
	meta := SessionMeta{Name: "v", Provider: "claude", WorkingDir: t.TempDir(), Vera: &veraBinding{ProjectID: 66, RepositoryLinkID: 7, GitProvider: "github", RunnerName: "r"}}
	for _, command := range []func(SessionMeta) (string, error){veraListenerCommand, veraHistoryCommand} {
		if _, err := command(meta); err == nil || !strings.Contains(err.Error(), "was removed or replaced; restart vibeflow") {
			t.Fatalf("removed binary not refused: %v", err)
		}
	}
}
