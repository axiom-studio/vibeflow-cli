package vibeflowcli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
)

type veraLaunchRequestedMsg struct{ result WizardResult }
type veraLaunchedMsg struct {
	statuses []reviewRunnerStatus
	err      error
}

// veraLaunchInputMsg means the binding needs a choice the wizard cannot make:
// an ambiguous repository link or a checkout that does not match the link.
type veraLaunchInputMsg struct {
	options reviewWatchOptions
	input   *reviewStartupInput
}

// beginVeraLaunch starts Vera with the harness chosen in the wizard's Provider
// step and the harness default model. Coding personas selected alongside Vera
// launch after it with their own settings.
func (m Model) beginVeraLaunch(result WizardResult) (tea.Model, tea.Cmd) {
	if !m.craEnabled || m.reviewSupervisor == nil {
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
	s, projectName := m.reviewSupervisor, result.ProjectName
	o := reviewWatchOptions{Project: strconv.FormatInt(result.ProjectID, 10), ProjectID: result.ProjectID, Repository: result.WorkDir, Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute, Name: s.options.Name, RepositoryRequestsApproved: true, Provider: provider}
	if result.ProjectID <= 0 {
		o.Project = projectName
	}
	m.veraProjectName = projectName
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		resolved, input, err := resolveReviewStartup(ctx, s.cfg, o, true)
		if err != nil {
			return veraLaunchedMsg{err: err}
		}
		if input != nil {
			return veraLaunchInputMsg{options: resolved, input: input}
		}
		return startVera(ctx, s, resolved, projectName)
	}
}

// showVeraLaunchInput opens the small setup popup for the one choice the
// binding still needs; the harness and model are already settled.
func (m Model) showVeraLaunchInput(msg veraLaunchInputMsg) (tea.Model, tea.Cmd) {
	setup := newReviewStartupModel(m.reviewSupervisor.ctx, m.reviewSupervisor.cfg, m.reviewSupervisor.configPath, msg.options)
	setup.selectedBinding, setup.repositorySelected = true, true
	setup.title, setup.cancelHint = reviewSessionLabel, "Esc: cancel"
	setup.input = msg.input
	m.veraSetup = &setup
	m.activeView = ViewVeraLaunch
	return m, nil
}

func (m Model) updateVeraLaunch(msg tea.Msg) (tea.Model, tea.Cmd) {
	if key, ok := msg.(tea.KeyPressMsg); ok && key.String() == "ctrl+c" {
		m.quitting = true
		return m, tea.Quit
	}
	if m.veraSetup == nil {
		m.activeView = ViewSessions
		return m, nil
	}
	if m.veraSetup.done && m.veraSetup.busy {
		return m, nil
	}
	next, cmd := m.veraSetup.Update(msg)
	setup := next.(reviewStartupModel)
	m.veraSetup = &setup
	if !setup.done && !setup.quit {
		return m, cmd
	}
	if setup.quit {
		m.quitting = true
		return m, tea.Quit
	}
	if !setup.enabled {
		m.veraSetup = nil
		m.activeView = ViewSessions
		if m.veraPending != nil {
			result := *m.veraPending
			m.veraPending = nil
			return m, func() tea.Msg { return m.launchFromWizard(result) }
		}
		return m, nil
	}
	o, s, projectName := setup.options, m.reviewSupervisor, m.veraProjectName
	m.veraSetup.busy = true
	return m, func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		return startVera(ctx, s, o, projectName)
	}
}

// startVera starts the runner for a resolved binding, refusing to silently
// keep a runner already listening with another harness or model.
func startVera(ctx context.Context, s *reviewSupervisor, o reviewWatchOptions, projectName string) veraLaunchedMsg {
	binding, err := resolveVeraBinding(ctx, s.cfg, o, projectName)
	if err != nil {
		return veraLaunchedMsg{err: err}
	}
	id := reviewBackgroundID(s.cfg.ServerURL, binding.Options)
	for _, row := range s.Snapshot() {
		if running := row.Binding.Options; row.BindingID == id && row.State == "online" && (running.Provider != o.Provider || running.Model != o.Model) {
			model := running.Model
			if model == "" {
				model = "harness default"
			}
			return veraLaunchedMsg{err: fmt.Errorf("Vera is already listening for this repository with %s (%s); close this CLI and start Vera again to change its harness", running.Provider, model)}
		}
	}
	return veraLaunchedMsg{statuses: s.StartBinding(binding)}
}

func resolveVeraBinding(ctx context.Context, cfg *Config, o reviewWatchOptions, projectName string) (reviewBinding, error) {
	if projectName == "" {
		projectName = strconv.FormatInt(o.ProjectID, 10)
	}
	id := reviewBackgroundID(cfg.ServerURL, o)
	d, err := discoverReviewProjectBindings(ctx, cfg, NewClient(cfg.ServerURL, cfg.APIToken), reviewDiscovery{Projects: []Project{{ID: o.ProjectID, Name: projectName}}, Problems: map[int64]string{}, Revoked: map[int64]bool{}}, []string{o.Repository}, map[string]string{id: o.Repository}, o.Name)
	if err != nil {
		return reviewBinding{}, err
	}
	if problem := d.Problems[o.ProjectID]; problem != "" {
		return reviewBinding{}, fmt.Errorf("could not load selected review repository: %s", problem)
	}
	for _, b := range d.Bindings {
		if b.Options.ProjectID == o.ProjectID && b.Options.RepositoryLinkID == o.RepositoryLinkID && b.Options.GitProvider == o.GitProvider && b.Options.Repository != "" {
			b.Options = o
			return b, nil
		}
	}
	return reviewBinding{}, fmt.Errorf("selected checkout no longer matches the linked repository; choose its current checkout or repository link")
}
