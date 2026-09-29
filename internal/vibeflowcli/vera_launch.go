package vibeflowcli

import (
	"context"
	"fmt"
	"strconv"
	"time"

	tea "charm.land/bubbletea/v2"
)

type veraLaunchRequestedMsg struct{ result WizardResult }
type veraLaunchedMsg struct {
	statuses []reviewRunnerStatus
	err      error
}

func (m Model) beginVeraLaunch(result WizardResult) (tea.Model, tea.Cmd) {
	if !m.craEnabled || m.reviewSupervisor == nil {
		m.err = fmt.Errorf("start this CLI with --cra to launch Vera")
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
	o := reviewWatchOptions{Project: strconv.FormatInt(result.ProjectID, 10), ProjectID: result.ProjectID, Repository: result.WorkDir, Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute, Name: m.reviewSupervisor.options.Name, RepositoryRequestsApproved: true}
	if result.ProjectID <= 0 {
		o.Project = result.ProjectName
	}
	setup := newReviewStartupModel(m.reviewSupervisor.ctx, m.reviewSupervisor.cfg, m.reviewSupervisor.configPath, o)
	setup.selectedBinding, setup.repositorySelected = true, true
	setup.title, setup.cancelHint = reviewSessionLabel, "Esc: cancel"
	setup.input = &reviewStartupInput{Field: "provider", Message: "Vera will review PRs for anyone who comments @vibeflow review on this linked repository, using your harness credentials, until this CLI closes. Choose Vera's review harness. Claude uses its existing credentials; Codex requires an OpenAI API key, not a ChatGPT login.", Choices: []reviewStartupChoice{{Label: "Claude", Value: "claude"}, {Label: "Codex (OpenAI API key required)", Value: "codex"}}}
	m.veraSetup = &setup
	m.veraProjectName = result.ProjectName
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
