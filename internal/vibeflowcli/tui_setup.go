package vibeflowcli

import (
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// SetupStep enumerates the first-run setup wizard steps.
type SetupStep int

const (
	SetupStepURL SetupStep = iota
	SetupStepToken
	SetupStepProject
	SetupStepDone
)

// SetupModel is the Bubble Tea model for first-run configuration.
type SetupModel struct {
	step            SetupStep
	cfg             *Config
	cfgPath         string
	urlInput        string
	tokenInput      string
	projects        []Project
	projectCursor   int
	creatingProject bool
	newProjectInput string
	err             error
	validating      bool
	width           int
	height          int
	done            bool
}

type serverValidateMsg struct{ err error }
type projectsFetchedMsg struct {
	projects []Project
	err      error
}
type projectCreatedMsg struct {
	project *Project
	err     error
}

// NewSetupModel creates a setup wizard for first-run configuration.
func NewSetupModel(cfg *Config, cfgPath string) SetupModel {
	return SetupModel{
		step:     SetupStepURL,
		cfg:      cfg,
		cfgPath:  cfgPath,
		urlInput: cfg.ServerURL,
	}
}

// Init initializes the setup model.
func (m SetupModel) Init() tea.Cmd {
	return nil
}

// Update handles messages for the setup wizard.
func (m SetupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height

	case serverValidateMsg:
		m.validating = false
		if msg.err != nil {
			m.err = fmt.Errorf("server check: %w (continuing anyway)", msg.err)
		} else {
			m.err = nil
		}
		m.cfg.ServerURL = m.urlInput
		m.step = SetupStepToken
		return m, nil

	case projectsFetchedMsg:
		m.validating = false
		if msg.err != nil {
			m.err = fmt.Errorf("fetch projects: %w (skipping project selection)", msg.err)
			m.step = SetupStepDone
			return m, m.saveConfig
		}
		m.projects = msg.projects
		if len(m.projects) == 0 {
			m.step = SetupStepDone
			return m, m.saveConfig
		}
		m.step = SetupStepProject
		return m, nil

	case projectCreatedMsg:
		m.validating = false
		if msg.err != nil {
			m.err = fmt.Errorf("create project: %w", msg.err)
			m.creatingProject = false
			return m, nil
		}
		m.projects = append(m.projects, *msg.project)
		m.projectCursor = len(m.projects) - 1
		m.creatingProject = false
		m.newProjectInput = ""
		m.err = nil
		return m, nil

	case setupSavedMsg:
		m.done = true
		return m, tea.Quit

	case tea.KeyMsg:
		if m.validating {
			return m, nil
		}
		switch m.step {
		case SetupStepURL:
			return m.updateURL(msg)
		case SetupStepToken:
			return m.updateToken(msg)
		case SetupStepProject:
			return m.updateProject(msg)
		}
	}
	return m, nil
}

func (m SetupModel) updateURL(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		if m.urlInput == "" {
			m.urlInput = "http://localhost:7080"
		}
		m.validating = true
		m.err = nil
		url := m.urlInput
		return m, func() tea.Msg {
			err := CheckServerReachable(url)
			return serverValidateMsg{err: err}
		}
	case "backspace":
		if len(m.urlInput) > 0 {
			m.urlInput = m.urlInput[:len(m.urlInput)-1]
		}
	case "ctrl+c":
		return m, tea.Quit
	default:
		if len(msg.String()) == 1 {
			m.urlInput += msg.String()
		}
	}
	return m, nil
}

func (m SetupModel) updateToken(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		m.cfg.APIToken = m.tokenInput
		m.validating = true
		m.err = nil
		client := NewClient(m.cfg.ServerURL, m.cfg.APIToken)
		return m, func() tea.Msg {
			projects, err := client.ListProjects()
			return projectsFetchedMsg{projects: projects, err: err}
		}
	case "backspace":
		if len(m.tokenInput) > 0 {
			m.tokenInput = m.tokenInput[:len(m.tokenInput)-1]
		}
	case "ctrl+c":
		return m, tea.Quit
	default:
		if len(msg.String()) == 1 {
			m.tokenInput += msg.String()
		}
	}
	return m, nil
}

func (m SetupModel) updateProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	if m.creatingProject {
		return m.updateCreateProject(msg)
	}
	switch msg.String() {
	case "enter":
		if m.projectCursor < len(m.projects) {
			m.cfg.DefaultProject = m.projects[m.projectCursor].Name
			m.step = SetupStepDone
			return m, m.saveConfig
		}
	case "up", "k":
		if m.projectCursor > 0 {
			m.projectCursor--
		}
	case "down", "j":
		if m.projectCursor < len(m.projects)-1 {
			m.projectCursor++
		}
	case "n":
		m.creatingProject = true
		m.newProjectInput = ""
		m.err = nil
	case "s":
		m.step = SetupStepDone
		return m, m.saveConfig
	case "ctrl+c":
		return m, tea.Quit
	}
	return m, nil
}

func (m SetupModel) updateCreateProject(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "enter":
		name := strings.TrimSpace(m.newProjectInput)
		if name == "" {
			m.err = fmt.Errorf("project name cannot be empty")
			return m, nil
		}
		m.validating = true
		m.err = nil
		client := NewClient(m.cfg.ServerURL, m.cfg.APIToken)
		return m, func() tea.Msg {
			project, err := client.CreateProject(name)
			return projectCreatedMsg{project: project, err: err}
		}
	case "esc":
		m.creatingProject = false
		m.newProjectInput = ""
		m.err = nil
	case "backspace":
		if len(m.newProjectInput) > 0 {
			m.newProjectInput = m.newProjectInput[:len(m.newProjectInput)-1]
		}
	case "ctrl+c":
		return m, tea.Quit
	default:
		if len(msg.String()) == 1 {
			m.newProjectInput += msg.String()
		}
	}
	return m, nil
}

type setupSavedMsg struct{}

func (m SetupModel) saveConfig() tea.Msg {
	_ = SaveConfig(m.cfg, m.cfgPath)
	return setupSavedMsg{}
}

// View renders the setup wizard.
func (m SetupModel) View() string {
	width := m.width
	if width < 40 {
		width = 80
	}
	height := m.height
	if height < 10 {
		height = 24
	}

	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	labelStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#aaaaaa"))
	inputStyle := lipgloss.NewStyle().Foreground(lipgloss.Color("#ffffff")).Bold(true)
	errStyle := lipgloss.NewStyle().Foreground(errorColor)
	dimStyle := lipgloss.NewStyle().Foreground(dimColor)

	var b strings.Builder
	b.WriteString(titleStyle.Render("vibeflow-cli — First Run Setup"))
	b.WriteString("\n\n")

	if m.validating {
		b.WriteString(dimStyle.Render("Validating..."))
		return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, b.String())
	}

	switch m.step {
	case SetupStepURL:
		b.WriteString(labelStyle.Render("VibeFlow Server URL:"))
		b.WriteString("\n")
		b.WriteString(inputStyle.Render(m.urlInput + "█"))
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Default: http://localhost:7080"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render("Press Enter to continue"))

	case SetupStepToken:
		b.WriteString(labelStyle.Render("API Token (optional — press Enter to skip):"))
		b.WriteString("\n")
		if m.tokenInput == "" {
			b.WriteString(inputStyle.Render("█"))
		} else {
			b.WriteString(inputStyle.Render(strings.Repeat("*", len(m.tokenInput)) + "█"))
		}
		b.WriteString("\n\n")
		b.WriteString(dimStyle.Render("Press Enter to continue"))

	case SetupStepProject:
		if m.creatingProject {
			b.WriteString(labelStyle.Render("New project name:"))
			b.WriteString("\n")
			b.WriteString(inputStyle.Render(m.newProjectInput + "█"))
			b.WriteString("\n\n")
			b.WriteString(dimStyle.Render("Enter: create  Esc: cancel"))
		} else {
			b.WriteString(labelStyle.Render("Select default project:"))
			b.WriteString("\n")
			for i, p := range m.projects {
				cursor := "  "
				style := dimStyle
				if i == m.projectCursor {
					cursor = "> "
					style = inputStyle
				}
				b.WriteString(style.Render(fmt.Sprintf("%s%s", cursor, p.Name)))
				b.WriteString("\n")
			}
			b.WriteString("\n")
			b.WriteString(dimStyle.Render("j/k: navigate  Enter: select  n: new  s: skip"))
		}

	case SetupStepDone:
		b.WriteString(labelStyle.Render("Setup complete!"))
		b.WriteString("\n")
		b.WriteString(dimStyle.Render(fmt.Sprintf("Config saved to %s", m.cfgPath)))
	}

	if m.err != nil {
		b.WriteString("\n")
		b.WriteString(errStyle.Render(m.err.Error()))
	}

	content := b.String()

	popupWidth := 56
	popupStyle := lipgloss.NewStyle().
		Width(popupWidth).
		Border(lipgloss.RoundedBorder()).
		BorderForeground(accentColor).
		Padding(1, 2)

	popup := popupStyle.Render(content)
	return lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup)
}

// Done reports whether the setup wizard has completed.
func (m SetupModel) Done() bool {
	return m.done
}

// Config returns the configured Config after setup is complete.
func (m SetupModel) Config() *Config {
	return m.cfg
}
