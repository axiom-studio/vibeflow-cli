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
	"strings"
	"sync/atomic"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// Removing the CRA-gated picker entry would make Vera unreachable from New Agent.
func TestVeraAgentPickerFeatureGate(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		cfg := DefaultConfig()
		m := Model{config: cfg, registry: NewProviderRegistry(cfg), craEnabled: enabled}
		next, _ := m.Update(tea.KeyPressMsg{Text: "n"})
		w := next.(Model).wizard
		found := -1
		for i, persona := range w.personas {
			if persona.key == "code_reviewer" {
				found = i
			}
		}
		if (found >= 0) != enabled {
			t.Fatalf("CRA=%v: Vera picker index=%d", enabled, found)
		}
		if !enabled {
			continue
		}
		w.step = StepTeam
		w.projects = []Project{{ID: 66, Name: "Selected"}}
		w.selectedWorkDir = t.TempDir()
		w.selectedSessionType = 1
		w.selectedPersonas = map[int]bool{found: true}
		updated, _ := w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		w = updated
		if !w.done || w.result.Persona != "code_reviewer" || w.result.ProjectID != 66 || w.result.WorkDir != w.selectedWorkDir {
			t.Fatalf("solo Vera entered ordinary coding setup: step=%v result=%+v", w.step, w.result)
		}
	}
}

// Declining all-project consent must not start runners during background discovery.
func TestVeraUnselectedDiscoveryDoesNotLaunch(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = "http://127.0.0.1:1", "fixture"
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.explicitOnly = true
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.RepositoryLinkID, o.GitProvider, o.Repository = 66, 7, "github", repo
	statuses := s.Reconcile(reviewDiscovery{Complete: true, Projects: []Project{{ID: 66}}, Bindings: []reviewBinding{{Options: o}}})
	if len(statuses) != 0 {
		t.Fatalf("no selected Vera, got runners %+v", statuses)
	}
}

func newVeraFixture(t *testing.T) (*Config, string, *atomic.Int64, *atomic.Int64) {
	t.Helper()
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var registrations, stops atomic.Int64
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
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
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
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "fixture"
	cfg.Providers["claude"] = Provider{Binary: provider}
	return cfg, repo, &registrations, &stops
}

// The picker must route to review setup before ordinary session initialization.
func TestVeraSoloLaunchNeverStartsCodingLoop(t *testing.T) {
	cfg := DefaultConfig()
	m := Model{config: cfg, craEnabled: false}
	msg := m.launchFromWizard(WizardResult{SessionType: "vibeflow", Persona: "code_reviewer", Personas: []string{"code_reviewer"}})
	if failure, ok := msg.(sessionsMsg); !ok || failure.err == nil || !strings.Contains(failure.err.Error(), "--cra") {
		t.Fatalf("CRA off must explicitly deny review launch, got %#v", msg)
	}
}

func TestVeraPickerLaunchBindingAndLifetime(t *testing.T) {
	cfg, repo, registrations, stops := newVeraFixture(t)
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.explicitOnly = true
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s}
	msg := m.launchFromWizard(WizardResult{SessionType: "vibeflow", Persona: "code_reviewer", Personas: []string{"code_reviewer"}, ProjectID: 66, ProjectName: "Selected", WorkDir: repo})
	next, _ := m.Update(msg)
	m = next.(Model)
	if m.activeView != ViewVeraLaunch {
		t.Fatalf("not review setup: %v", m.activeView)
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.Update(cmd())
	m = next.(Model)
	if m.veraSetup.input == nil || m.veraSetup.input.Field != "model" {
		t.Fatal("model choice missing")
	}
	m.veraSetup.cursor = len(m.veraSetup.input.Choices) - 1
	next, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	next, _ = m.Update(tea.KeyPressMsg{Text: "review-model"})
	m = next.(Model)
	next, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	resolved := cmd()
	next, cmd = m.Update(resolved)
	m = next.(Model)
	if cmd == nil {
		t.Fatalf("launch missing, setup %+v", m.veraSetup)
	}
	next, _ = m.Update(cmd())
	m = next.(Model)
	if len(m.reviewStatuses) != 1 || m.reviewStatuses[0].State != "online" {
		t.Fatalf("runner %+v setup %+v", m.reviewStatuses, m.veraSetup)
	}
	row := m.reviewStatuses[0]
	if row.Binding.ProjectName != "Selected" || row.Binding.Repository.Name != "acme/repo" || row.Binding.Repository.Host != "github.com" {
		t.Fatalf("runner identity is not visible: %+v", row.Binding)
	}
	if row.Binding.Options.ProjectID != 66 || row.Binding.Options.RepositoryLinkID != 7 || row.Binding.Options.Repository != repo || row.Binding.Options.Provider != "claude" || row.Binding.Options.Model != "review-model" || !row.Binding.Options.RepositoryRequestsApproved {
		t.Fatalf("wrong binding %+v", row.Binding)
	}
	if !strings.Contains(m.viewReviewRunners(), "Listening") {
		t.Fatal("idle runner must visibly listen")
	}
	if _, err := os.Stat(filepath.Join(repo, ".vibeflow-session-code_reviewer")); !os.IsNotExist(err) {
		t.Fatal("Vera wrote ordinary coding session state")
	}
	if registrations.Load() != 1 {
		t.Fatal("first runner not registered exactly once")
	}
	b := row.Binding
	b.Options.Model = "different-model"
	statuses := s.StartBinding(b)
	if registrations.Load() != 1 || statuses[0].Binding.Options.Model != "review-model" {
		t.Fatal("reuse duplicated runner or rewrote its options")
	}
	second, _ := reviewTestRepo(t)
	reviewTestGit(t, second, "remote", "set-url", "origin", "https://github.com/acme/second.git")
	b.Options.Repository, b.Options.RepositoryLinkID, b.Repository.ID, b.Repository.Name = second, 8, 8, "acme/second"
	statuses = s.StartBinding(b)
	if registrations.Load() != 2 || len(statuses) != 2 {
		t.Fatalf("second binding failed %+v", statuses)
	}
	// Full discovery must retain selected models without enrolling a third link.
	unselected := b
	unselected.Options.RepositoryLinkID = 9
	statuses = s.Reconcile(reviewDiscovery{Complete: true, Projects: []Project{{ID: 66, Name: "Selected"}}, Bindings: []reviewBinding{row.Binding, b, unselected}})
	if registrations.Load() != 2 || len(statuses) != 2 {
		t.Fatal("discovery enlarged selected consent scope")
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	if stops.Load() != 2 {
		t.Fatalf("owned runners not stopped: %d", stops.Load())
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

func TestVeraTeamSelectionDoesNotChangeCodingConfig(t *testing.T) {
	cfg, repo, _, _ := newVeraFixture(t)
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	providers := make(map[string]Provider, len(cfg.Providers))
	for key, value := range cfg.Providers {
		providers[key] = value
	}
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s}
	result := WizardResult{SessionType: "vibeflow", Personas: []string{"developer", "code_reviewer"}, Persona: "developer", ProviderKey: "qwen", ProjectID: 66, WorkDir: repo}
	next, _ := m.Update(m.launchFromWizard(result))
	m = next.(Model)
	if m.veraPending == nil || len(m.veraPending.Personas) != 1 || m.veraPending.Persona != "developer" || m.veraPending.ProviderKey != "qwen" {
		t.Fatalf("coding launch altered %+v", m.veraPending)
	}
	if !reflect.DeepEqual(cfg.Providers, providers) {
		t.Fatal("Vera rewrote coding providers")
	}
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if m.veraPending != nil || m.activeView != ViewSessions || cmd == nil {
		t.Fatal("canceling Vera silently lost selected coding agents")
	}
}

// Removing Vera leaves one coding persona, which must still honor its team provider override.
func TestVeraTeamCodingLaunchKeepsProviderOverride(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	cfg, repo, _, _ := newVeraFixture(t)
	agents := t.TempDir()
	for _, key := range []string{"qwen", "codex"} {
		binary := filepath.Join(agents, key)
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 300\n"), 0700); err != nil {
			t.Fatal(err)
		}
		cfg.Providers[key] = Provider{Name: key, Binary: binary, LaunchTemplate: "{{.Binary}}"}
	}
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	tm := NewTmuxManager(fmt.Sprintf("vftest-vera-team-%d", os.Getpid()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s, tmux: tm, registry: NewProviderRegistry(cfg), logger: NewLogger(), store: NewStore(), cache: NewSessionCache()}
	result := WizardResult{SessionType: "vibeflow", Personas: []string{"developer", "code_reviewer"}, Persona: "developer", ProviderKey: "qwen", Provider: cfg.Providers["qwen"], PersonaProviders: map[string]string{"developer": "codex"}, ProjectID: 66, ProjectName: "Selected", WorkDir: repo, WorktreeChoice: WorktreeCurrent}
	next, _ := m.Update(m.launchFromWizard(result))
	m = next.(Model)
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEscape})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("canceling Vera dropped the coding launch")
	}
	if failure, ok := cmd().(sessionsMsg); ok && failure.err != nil {
		t.Fatal(failure.err)
	}
	sessions, err := tm.run("list-sessions", "-F", "#{session_name}")
	if err != nil || !strings.Contains(sessions, sessionPrefix+"codex-") || strings.Contains(sessions, sessionPrefix+"qwen-") {
		t.Fatalf("developer ignored its codex override: %v %q", err, sessions)
	}
}

func TestVeraSelectedCheckoutRequiresExactBinding(t *testing.T) {
	cfg, repo, registrations, _ := newVeraFixture(t)
	cfg.DirectoryHistory = []string{repo}
	wrong, _ := reviewTestRepo(t)
	reviewTestGit(t, wrong, "remote", "set-url", "origin", "https://github.com/acme/unlinked.git")
	o := reviewWatchOptions{Project: "66", ProjectID: 66, Provider: "claude", Repository: wrong, Kind: "local", Model: "test", Name: "fixture"}
	_, input, err := resolveReviewStartup(context.Background(), cfg, o, true)
	if err != nil || input == nil || input.Field != "repository" {
		t.Fatalf("missing binding must be actionable, input=%+v err=%v", input, err)
	}
	if registrations.Load() != 0 {
		t.Fatal("unlinked selection launched a runner")
	}
	o.Repository, o.RepositoryLinkID, o.GitProvider = repo, 99, "github"
	if _, err := resolveVeraBinding(context.Background(), cfg, o, "Selected"); err == nil {
		t.Fatal("wrong link accepted")
	}
}

func TestVeraExternalRunnerIsNotAdopted(t *testing.T) {
	cfg, repo, registrations, stops := newVeraFixture(t)
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.Project, o.RepositoryLinkID, o.GitProvider, o.RepositoryRequestsApproved = 66, "66", 7, "github", true
	external, err := startReviewOwned(context.Background(), cfg, "", o)
	if err != nil {
		t.Fatal(err)
	}
	defer external.Close()
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	s.explicitOnly = true
	statuses := s.StartBinding(reviewBinding{Options: o})
	if len(statuses) != 1 || statuses[0].State != "external" || registrations.Load() != 1 || len(s.owned) != 0 {
		t.Fatalf("external ownership changed %+v", statuses)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	select {
	case <-external.Done():
		t.Fatal("TUI stopped an external runner")
	default:
	}
	if stops.Load() != 0 {
		t.Fatal("external runner disabled by TUI")
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

func TestVeraMissingCheckoutCanRecover(t *testing.T) {
	cfg, repo, _, _ := newVeraFixture(t)
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.explicitOnly = true
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.Project, o.RepositoryLinkID, o.GitProvider, o.RepositoryRequestsApproved = 66, "66", 7, "github", true
	o.Repository = ""
	statuses := s.StartBinding(reviewBinding{Options: o})
	if len(statuses) != 1 || statuses[0].State != "needs_checkout" {
		t.Fatalf("missing checkout unexpectedly started %+v", statuses)
	}
	o.Repository = repo
	statuses = s.Reconcile(reviewDiscovery{Complete: true, Projects: []Project{{ID: 66}}, Bindings: []reviewBinding{{Options: o}}})
	if len(statuses) != 1 || statuses[0].State != "online" || statuses[0].Binding.Options.Repository != repo {
		t.Fatalf("validated checkout could not recover selected runner %+v", statuses)
	}
}

// Declining startup consent leaves the coding default (here qwen) in the group options.
// Correcting a selected Vera checkout must still save with Vera's valid review harness.
func TestVeraCheckoutRecoveryIgnoresCodingDefault(t *testing.T) {
	cfg, repo, _, _ := newVeraFixture(t)
	cfg.DefaultProvider = "qwen"
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.options = reviewStartupOptions(cfg, "", repo)
	s.explicitOnly = true
	o := s.options
	o.ProjectID, o.Project, o.RepositoryLinkID, o.GitProvider, o.Provider, o.RepositoryRequestsApproved = 66, "66", 7, "github", "claude", true
	o.Repository = ""
	statuses := s.StartBinding(reviewBinding{Options: o, Repository: reviewStartupRepository{Provider: "github", Host: "github.com", ID: 7, Name: "acme/repo"}})
	if len(statuses) != 1 || statuses[0].State != "needs_checkout" {
		t.Fatalf("missing checkout unexpectedly started %+v", statuses)
	}
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s, activeView: ViewReviewRunners, reviewStatuses: statuses, reviewPreferences: map[string]string{}}
	next, _ := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if m.reviewCheckout == nil {
		t.Fatal("Enter did not open the checkout editor")
	}
	m.reviewCheckout.text = repo
	next, cmd := m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m = next.(Model)
	if cmd == nil {
		t.Fatal("checkout was not submitted")
	}
	saved, ok := cmd().(reviewCheckoutSavedMsg)
	if !ok || saved.err != nil || saved.path != repo {
		t.Fatalf("checkout recovery failed: %+v", saved)
	}
	if cfg.DefaultProvider != "qwen" {
		t.Fatal("checkout recovery changed the coding-agent default")
	}
}

func TestVeraCodexSetupPreservesHarnessAndModel(t *testing.T) {
	cfg, repo, registrations, _ := newVeraFixture(t)
	cfg.Providers["codex"] = Provider{Binary: "/bin/sh"}
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s}
	next, _ := m.beginVeraLaunch(WizardResult{ProjectID: 66, WorkDir: repo, Persona: "code_reviewer"})
	m = next.(Model)
	if view := m.veraSetup.View().Content; strings.Contains(view, "API key") || !strings.Contains(view, "full permissions") {
		t.Fatalf("Vera harness copy is stale:\n%s", view)
	}
	m.veraSetup.cursor = -1
	for i, choice := range m.veraSetup.input.Choices {
		if choice.Value == "codex" {
			m.veraSetup.cursor = i
		}
	}
	if m.veraSetup.cursor < 0 {
		t.Fatal("configured Codex harness is not offered")
	}
	next, cmd := m.veraSetup.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	setup := next.(reviewStartupModel)
	resolved := cmd().(reviewStartupResolvedMsg)
	setup.options, setup.modelSelected = resolved.options, true
	setup.options.Model = "codex-review-model"
	resolved = setup.resolve()().(reviewStartupResolvedMsg)
	if resolved.err != nil || resolved.input != nil || resolved.options.Provider != "codex" || resolved.options.Model != "codex-review-model" || resolved.options.RepositoryLinkID != 7 {
		t.Fatalf("Codex selection lost %+v", resolved)
	}
	if registrations.Load() != 0 {
		t.Fatal("setup started an LLM or runner prematurely")
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

func TestVeraRunnerActivityProjection(t *testing.T) {
	cfg, repo, _, _ := newVeraFixture(t)
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { s.owned = map[string]*reviewOwnedRunner{}; _ = s.Close() }()
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.Project, o.RepositoryLinkID, o.GitProvider, o.RepositoryRequestsApproved = 66, "66", 7, "github", true
	id := reviewBackgroundID(cfg.ServerURL, o)
	// The process handle is inert here: this test reads durable activity only,
	// while the launch/lifetime test above exercises the real owned subprocess.
	s.owned[id] = &reviewOwnedRunner{done: make(chan struct{})}
	s.statuses = []reviewRunnerStatus{{BindingID: id, Binding: reviewBinding{Options: o}}}
	statuses := s.Snapshot()
	if len(statuses) != 1 || !strings.Contains(statuses[0].Message, "Listening") {
		t.Fatalf("missing idle state %+v", statuses)
	}
	statePath := filepath.Join(RootDir(), "review-runners", id, "state.json")
	if err := os.MkdirAll(filepath.Dir(statePath), 0700); err != nil {
		t.Fatal(err)
	}
	if err := saveReviewJSON(statePath, reviewRunnerState{Pending: &reviewReceipt{}}); err != nil {
		t.Fatal(err)
	}
	if rows := s.Snapshot(); !strings.Contains(rows[0].Message, "Running") {
		t.Fatalf("pending attempt invisible %+v", rows)
	}
	s.owned[id].status = reviewLegacyRoutingNotice
	if rows := s.Snapshot(); !strings.Contains(rows[0].Message, "Running") || !strings.Contains(rows[0].Message, "server upgrade") {
		t.Fatalf("legacy notice hid the activity or itself %+v", rows)
	}
}

// A coding launch queued behind Vera can raise a conflict modal, which only the
// sessions view handles; staying on the runners view silently dropped it.
func TestVeraTeamLaunchReturnsToSessionsForConflicts(t *testing.T) {
	pending := WizardResult{SessionType: "vibeflow", Persona: "developer", Personas: []string{"developer"}}
	m := Model{config: DefaultConfig(), craEnabled: true, veraPending: &pending, activeView: ViewVeraLaunch}
	next, cmd := m.Update(veraLaunchedMsg{})
	m = next.(Model)
	if m.activeView != ViewSessions || cmd == nil {
		t.Fatalf("pending coding launch left view %v", m.activeView)
	}
	next, _ = m.Update(conflictDetectedMsg{conflict: ConflictResult{Status: ActiveConflict}, wizardResult: pending})
	if next.(Model).activeView != ViewConflict {
		t.Fatal("coding-agent conflict prompt was dropped")
	}
}

// Choosing Vera again for a running repository must not silently keep the old harness.
func TestVeraRepickWithDifferentHarnessIsReported(t *testing.T) {
	cfg, repo, registrations, _ := newVeraFixture(t)
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.explicitOnly = true
	o := reviewStartupOptions(cfg, "", repo)
	o.ProjectID, o.Project, o.RepositoryLinkID, o.GitProvider, o.Provider, o.Model, o.RepositoryRequestsApproved = 66, "66", 7, "github", "claude", "review-model", true
	binding, err := resolveVeraBinding(context.Background(), cfg, o, "Selected")
	if err != nil {
		t.Fatal(err)
	}
	if rows := s.StartBinding(binding); len(rows) != 1 || rows[0].State != "online" {
		t.Fatalf("first Vera did not start %+v", rows)
	}
	m := Model{config: cfg, craEnabled: true, reviewSupervisor: s}
	next, _ := m.beginVeraLaunch(WizardResult{ProjectID: 66, ProjectName: "Selected", WorkDir: repo, Persona: "code_reviewer"})
	m = next.(Model)
	m.veraSetup.options = o
	m.veraSetup.options.Model = "other-model"
	m.veraSetup.done, m.veraSetup.enabled = true, true
	next, cmd := m.updateVeraLaunch(tea.FocusMsg{})
	if cmd == nil {
		t.Fatal("launch command missing")
	}
	launched, ok := cmd().(veraLaunchedMsg)
	if !ok || launched.err == nil || !strings.Contains(launched.err.Error(), "already listening") || !strings.Contains(launched.err.Error(), "review-model") {
		t.Fatalf("harness change was silently ignored: %+v", launched)
	}
	if registrations.Load() != 1 {
		t.Fatal("re-picking Vera registered another runner")
	}
}

// Declined consent must not scan every project; it refreshes only Vera picks,
// using the runner name the TUI started with so runner IDs stay stable.
func TestReviewDiscoveryDeclinedScope(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	var projectLists, selectedReads, otherReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/projects"):
			projectLists.Add(1)
			fmt.Fprint(w, `[{"id":66,"name":"Selected"},{"id":67,"name":"Other"}]`)
		case strings.HasSuffix(r.URL.Path, "/projects/66/pr-review-repositories"):
			selectedReads.Add(1)
			fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}],"supported_runner_capabilities":["repository_review_v1"]}`)
		case strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
			otherReads.Add(1)
			fmt.Fprint(w, `{"repositories":[]}`)
		default:
			t.Errorf("unexpected %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "fixture"
	s, err := newReviewSupervisor(context.Background(), cfg, "")
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	s.explicitOnly = true
	s.options.Name = "runner-name-at-startup"
	m := &Model{config: cfg, craEnabled: true, reviewSupervisor: s, reviewPreferences: map[string]string{}}
	if msg := m.requestReviewDiscovery()(); len(msg.(reviewDiscoveryMsg).statuses) != 0 || projectLists.Load() != 0 {
		t.Fatalf("declined consent scanned projects: %d lists", projectLists.Load())
	}
	o := s.options
	o.ProjectID, o.RepositoryLinkID, o.GitProvider, o.Repository = 66, 7, "github", ""
	s.selected[reviewBackgroundID(cfg.ServerURL, o)] = o
	m.reviewDiscoveryBusy = false
	m.requestReviewDiscovery()()
	if selectedReads.Load() != 1 || otherReads.Load() != 0 {
		t.Fatalf("declined discovery read selected=%d other=%d", selectedReads.Load(), otherReads.Load())
	}
	d, err := discoverReviewBindings(context.Background(), cfg, []string{repo}, nil, "runner-name-at-startup", map[int64]bool{66: true})
	if err != nil || len(d.Bindings) != 1 || d.Bindings[0].Options.Name != "runner-name-at-startup" {
		t.Fatalf("discovery did not keep the TUI runner name: %+v %v", d.Bindings, err)
	}
}
