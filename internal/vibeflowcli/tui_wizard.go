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
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// WizardStep represents the current step in the session creation wizard.
type WizardStep int

const (
	StepWorkDir WizardStep = iota
	StepSessionType
	StepProject
	StepPersona
	StepProvider
	StepEnvToken
	StepBranch
	StepWorktree
	StepPermissions
	StepConfirm
)

// WorktreeChoice represents the user's worktree selection.
type WorktreeChoice int

const (
	WorktreeNew WorktreeChoice = iota
	WorktreeExisting
	WorktreeCurrent
	WorktreeCustom
	WorktreeSpecifyDir // Use a user-specified directory (no worktree creation).
)

// WizardResult holds the output of a completed wizard.
type WizardResult struct {
	SessionType          string // "vanilla" or "vibeflow"
	ProjectID            int64  // VibeFlow project ID (vibeflow sessions only).
	ProjectName          string // VibeFlow project name (vibeflow sessions only).
	Persona              string // Persona key (vibeflow sessions only, e.g. "developer").
	Provider             Provider
	ProviderKey          string
	Branch               string
	NewBranch            bool   // True if user chose to create a new branch.
	WorktreeChoice       WorktreeChoice
	SkipPermissions      bool
	WorktreeName         string // Custom worktree directory name, or "" for auto-generated.
	CustomBinaryPath     string // User-provided absolute path if binary was not on PATH.
	ExistingWorktreePath string // Path of existing worktree to reuse (when WorktreeExisting).
	CustomBaseDir        string // Custom base directory for worktree (when WorktreeCustom).
	SpecifiedWorkDir     string // User-specified working directory (when WorktreeSpecifyDir).
	ReuseSessionID       string // Session ID from a previous conflict to reuse via session_init.
	WorkDir              string // Project root directory selected in StepWorkDir.
	EnvVars              map[string]string // Extra env vars to set on the tmux session.
}

// WizardModel is a Bubble Tea sub-model for multi-step session creation.
type WizardModel struct {
	step     WizardStep
	cursor   int
	done     bool
	cancelled bool

	// Data sources.
	sessionTypeOpts    []string
	projects           []Project
	filteredProjects   []int // indices into projects slice after filtering
	providers          []providerEntry
	branches           []string
	worktreeOpts       []string
	permissionOpts     []string
	existingWorktrees  map[string]string // branch → existing worktree path
	defaultProject     string            // pre-select from config

	// Persona data.
	personas         []personaEntry

	// Directory selection (StepWorkDir).
	dirHistory       []string // Recent directories from config.
	dirOpts          []string // Display options: "[+] Enter new path" + history entries.
	selectedWorkDir  string   // Resolved working directory path.
	editingWorkDir   bool     // True when text input for new directory is active.
	workDirInput     string   // Text input for new directory.
	workDirErr       string   // Validation error for directory.
	repoRoot         string   // Initial repo root from caller.
	registry         *ProviderRegistry // Provider registry for re-loading on dir change.
	client           *Client           // API client (may be nil).
	config           *Config           // Config for saved env vars and persisting.

	// Selections.
	selectedSessionType int
	selectedProject     int
	selectedPersona     int
	selectedProvider    int
	selectedBranch      int
	selectedWorktree    int
	selectedPermission  int

	// Project filtering.
	projectFilter       string
	projectFilterActive bool
	projectErr          string // error from API fetch

	// Branch filtering.
	branchFilter       string
	branchFilterActive bool
	filteredBranches   []int // indices into branches slice (always includes index 0 = "[+] Create new")

	// Text input state.
	worktreeName    string // Custom name entered by user.
	editingName     bool   // True when text input for worktree name is active.
	newBranchName   string // New branch name entered by user.
	editingBranch   bool   // True when text input for new branch name is active.
	binaryPath      string // Custom binary path entered by user.
	editingBinary   bool   // True when text input for binary path is active.
	binaryPathErr   string // Validation error for binary path.
	customBaseDir       string // Custom base directory for worktree.
	editingCustomDir    bool   // True when text input for custom dir is active.
	customDirErr        string // Validation error for custom dir.
	specifiedWorkDir    string // User-specified working directory path.
	editingSpecWorkDir  bool   // True when text input for specified work dir is active.
	specifiedWorkDirErr string // Validation error for specified work dir.

	// Env token input (StepEnvToken).
	envTokenVarName string // Name of the env var to prompt for (e.g. "MCP_TOKEN").
	envTokenValue   string // User-entered value for the env var.
	editingEnvToken bool   // True when text input for env token is active.
	envVars         map[string]string // Resolved env vars to pass to session.

	result WizardResult
}

type providerEntry struct {
	key       string
	provider  Provider
	available bool
}

type personaEntry struct {
	key         string
	displayName string
	description string
}

// defaultPersonas returns the known persona list from the vibeflow server.
func defaultPersonas() []personaEntry {
	return []personaEntry{
		{"developer", "Developer", "Write code, fix bugs, implement features"},
		{"architect", "Architect", "Design systems, create architecture docs, plan work"},
		{"qa_lead", "QA Lead", "Test, verify, ensure quality"},
		{"security_lead", "Security Lead", "Security review, vulnerability assessment"},
		{"product_manager", "Product Manager", "Define requirements, write PRDs"},
		{"project_manager", "Project Manager", "Track progress, manage workflow"},
		{"customer", "Customer", "Request features, report issues"},
	}
}

// NewWizardModel creates a wizard pre-loaded with providers and branches.
// wm may be nil if not in a git repository. client may be nil if API is unavailable.
func NewWizardModel(registry *ProviderRegistry, repoRoot string, wm *WorktreeManager, client *Client, defaultProject string, dirHistory []string, cfg *Config) WizardModel {
	// Build provider list.
	allProviders := registry.List()
	entries := make([]providerEntry, 0, len(allProviders))
	// We need keys too — get them from the registry.
	for _, key := range providerKeys(registry) {
		p, _ := registry.Get(key)
		entries = append(entries, providerEntry{
			key:       key,
			provider:  p,
			available: registry.IsAvailable(key),
		})
	}

	// Get git branches (local + remote tracking).
	branches := listGitBranches(repoRoot)
	if len(branches) == 0 {
		branches = []string{"main"}
	}
	// Prepend the "create new branch" option.
	branches = append([]string{"[+] Create new branch"}, branches...)

	// Build branch → worktree path map for existing worktrees.
	var existingWts map[string]string
	if wm != nil {
		existingWts = wm.BranchWorktreeMap()
	}

	// Fetch projects from API (best-effort).
	var projects []Project
	var projectErr string
	if client != nil {
		if fetched, err := client.ListProjects(); err == nil {
			projects = fetched
		} else {
			projectErr = fmt.Sprintf("Failed to fetch projects: %v", err)
		}
	}
	// Build initial filtered indices (all projects).
	filtered := make([]int, len(projects))
	for i := range projects {
		filtered[i] = i
	}

	// Build initial filtered branch indices (all branches).
	filteredBr := make([]int, len(branches))
	for i := range branches {
		filteredBr[i] = i
	}

	// Build directory options: "[+] Enter new path" + history entries.
	dirOpts := []string{"[+] Enter new path"}
	dirOpts = append(dirOpts, dirHistory...)

	return WizardModel{
		step:              StepWorkDir,
		sessionTypeOpts:   []string{"Vanilla", "VibeFlow"},
		projects:          projects,
		filteredProjects:  filtered,
		defaultProject:    defaultProject,
		projectErr:        projectErr,
		personas:          defaultPersonas(),
		providers:         entries,
		branches:          branches,
		filteredBranches:  filteredBr,
		existingWorktrees: existingWts,
		worktreeOpts:      []string{"New worktree", "Specify directory", "Current directory"},
		permissionOpts:    []string{"Skip permissions (autonomous)", "Keep permissions (interactive)"},
		dirHistory:        dirHistory,
		dirOpts:           dirOpts,
		repoRoot:          repoRoot,
		registry:          registry,
		client:            client,
		config:            cfg,
	}
}

// Done returns true when the wizard has completed.
func (w WizardModel) Done() bool { return w.done }

// Cancelled returns true when the user cancelled the wizard.
func (w WizardModel) Cancelled() bool { return w.cancelled }

// Result returns the wizard's selections.
func (w WizardModel) Result() WizardResult { return w.result }

// Update handles input for the wizard.
func (w WizardModel) Update(msg tea.Msg) (WizardModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		// Text input mode for working directory path.
		if w.editingWorkDir {
			switch msg.String() {
			case "enter":
				dir := w.workDirInput
				if dir == "" {
					w.workDirErr = "path cannot be empty"
					return w, nil
				}
				if strings.HasPrefix(dir, "~/") {
					if home, err := os.UserHomeDir(); err == nil {
						dir = filepath.Join(home, dir[2:])
						w.workDirInput = dir
					}
				}
				info, err := os.Stat(dir)
				if err != nil {
					w.workDirErr = "directory does not exist"
					return w, nil
				}
				if !info.IsDir() {
					w.workDirErr = "path is not a directory"
					return w, nil
				}
				if !isGitRepo(dir) {
					w.workDirErr = "not a git repository"
					return w, nil
				}
				w.workDirErr = ""
				w.editingWorkDir = false
				w.selectedWorkDir = dir
				w.reloadBranchesForDir(dir)
				w.step = StepSessionType
				w.cursor = 0
			case "esc":
				w.editingWorkDir = false
				w.workDirInput = ""
				w.workDirErr = ""
			case "backspace":
				if len(w.workDirInput) > 0 {
					w.workDirInput = w.workDirInput[:len(w.workDirInput)-1]
				}
				w.workDirErr = ""
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidPathChar(byte(r)) {
							w.workDirInput += string(r)
						}
					}
					w.workDirErr = ""
				}
			}
			return w, nil
		}

		// Text input mode for new branch name.
		if w.editingBranch {
			switch msg.String() {
			case "enter":
				if w.newBranchName != "" {
					w.editingBranch = false
					w.rebuildWorktreeOpts()
					w.step = StepWorktree
					w.cursor = 0
				}
			case "esc":
				w.editingBranch = false
				w.newBranchName = ""
				// Stay on branch step.
			case "backspace":
				if len(w.newBranchName) > 0 {
					w.newBranchName = w.newBranchName[:len(w.newBranchName)-1]
				}
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidBranchChar(byte(r)) {
							w.newBranchName += string(r)
						}
					}
				}
			}
			return w, nil
		}

		// Text input mode for binary path.
		if w.editingBinary {
			switch msg.String() {
			case "enter":
				if w.binaryPath != "" {
					if !isExecutable(w.binaryPath) {
						w.binaryPathErr = "file not found or not executable"
					} else {
						w.binaryPathErr = ""
						w.editingBinary = false
						// Update the provider entry to reflect availability.
						w.providers[w.selectedProvider].available = true
						w.step = StepBranch
						w.cursor = 0
					}
				}
			case "esc":
				w.editingBinary = false
				w.binaryPath = ""
				w.binaryPathErr = ""
				// Stay on provider step.
			case "backspace":
				if len(w.binaryPath) > 0 {
					w.binaryPath = w.binaryPath[:len(w.binaryPath)-1]
					w.binaryPathErr = ""
				}
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidPathChar(byte(r)) {
							w.binaryPath += string(r)
						}
					}
					w.binaryPathErr = ""
				}
			}
			return w, nil
		}

		// Text input mode for project filtering.
		if w.projectFilterActive {
			switch msg.String() {
			case "esc":
				if w.projectFilter != "" {
					w.projectFilter = ""
					w.rebuildProjectFilter()
				} else {
					w.projectFilterActive = false
				}
			case "enter":
				w.projectFilterActive = false
				if len(w.filteredProjects) > 0 {
					return w.advance()
				}
			case "backspace":
				if len(w.projectFilter) > 0 {
					w.projectFilter = w.projectFilter[:len(w.projectFilter)-1]
					w.rebuildProjectFilter()
					if w.cursor >= len(w.filteredProjects) {
						w.cursor = max(0, len(w.filteredProjects)-1)
					}
				}
			case "up", "k":
				if w.cursor > 0 {
					w.cursor--
				}
				return w, nil
			case "down", "j":
				w.cursor = min(w.cursor+1, len(w.filteredProjects)-1)
				return w, nil
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if r >= ' ' && r <= '~' {
							w.projectFilter += string(r)
						}
					}
					w.rebuildProjectFilter()
					w.cursor = 0
				}
			}
			return w, nil
		}

		// Text input mode for worktree name.
		if w.editingName {
			switch msg.String() {
			case "enter":
				w.editingName = false
				w.step = StepPermissions
				w.cursor = 0
			case "esc":
				w.editingName = false
				w.worktreeName = ""
				// Stay on worktree step.
			case "backspace":
				if len(w.worktreeName) > 0 {
					w.worktreeName = w.worktreeName[:len(w.worktreeName)-1]
				}
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidNameChar(byte(r)) {
							w.worktreeName += string(r)
						}
					}
				}
			}
			return w, nil
		}

		// Text input mode for custom worktree base directory.
		if w.editingCustomDir {
			switch msg.String() {
			case "enter":
				dir := w.customBaseDir
				if dir == "" {
					w.customDirErr = "path cannot be empty"
					return w, nil
				}
				// Validate directory exists and is writable.
				info, err := os.Stat(dir)
				if err != nil {
					w.customDirErr = "directory does not exist"
					return w, nil
				}
				if !info.IsDir() {
					w.customDirErr = "path is not a directory"
					return w, nil
				}
				w.customDirErr = ""
				w.editingCustomDir = false
				// Generate worktree name and proceed to permissions.
				pe := w.providers[w.selectedProvider]
				br := w.resolvedBranch()
				safeBr := strings.ReplaceAll(br, "/", "-")
				w.worktreeName = fmt.Sprintf("%s-%s", pe.key, safeBr)
				w.step = StepPermissions
				w.cursor = 0
			case "esc":
				w.editingCustomDir = false
				w.customBaseDir = ""
				w.customDirErr = ""
			case "backspace":
				if len(w.customBaseDir) > 0 {
					w.customBaseDir = w.customBaseDir[:len(w.customBaseDir)-1]
				}
				w.customDirErr = ""
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidPathChar(byte(r)) {
							w.customBaseDir += string(r)
						}
					}
					w.customDirErr = ""
				}
			}
			return w, nil
		}

		// Text input mode for specified working directory.
		if w.editingSpecWorkDir {
			switch msg.String() {
			case "enter":
				dir := w.specifiedWorkDir
				if dir == "" {
					w.specifiedWorkDirErr = "path cannot be empty"
					return w, nil
				}
				// Expand ~ to home directory.
				if strings.HasPrefix(dir, "~/") {
					if home, err := os.UserHomeDir(); err == nil {
						dir = filepath.Join(home, dir[2:])
						w.specifiedWorkDir = dir
					}
				}
				info, err := os.Stat(dir)
				if err != nil {
					w.specifiedWorkDirErr = "directory does not exist"
					return w, nil
				}
				if !info.IsDir() {
					w.specifiedWorkDirErr = "path is not a directory"
					return w, nil
				}
				w.specifiedWorkDirErr = ""
				w.editingSpecWorkDir = false
				w.step = StepPermissions
				w.cursor = 0
			case "esc":
				w.editingSpecWorkDir = false
				w.specifiedWorkDir = ""
				w.specifiedWorkDirErr = ""
			case "backspace":
				if len(w.specifiedWorkDir) > 0 {
					w.specifiedWorkDir = w.specifiedWorkDir[:len(w.specifiedWorkDir)-1]
				}
				w.specifiedWorkDirErr = ""
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if isValidPathChar(byte(r)) {
							w.specifiedWorkDir += string(r)
						}
					}
					w.specifiedWorkDirErr = ""
				}
			}
			return w, nil
		}

		// Text input mode for env token value.
		if w.editingEnvToken {
			switch msg.String() {
			case "enter":
				if w.envTokenValue != "" {
					w.editingEnvToken = false
					// Strip surrounding brackets/quotes that may have been pasted.
					w.envTokenValue = strings.Trim(w.envTokenValue, "[]\"' ")
					// Save to env vars and persist in config.
					if w.envVars == nil {
						w.envVars = make(map[string]string)
					}
					w.envVars[w.envTokenVarName] = w.envTokenValue
					if w.config != nil {
						if w.config.SavedEnvVars == nil {
							w.config.SavedEnvVars = make(map[string]string)
						}
						w.config.SavedEnvVars[w.envTokenVarName] = w.envTokenValue
						_ = SaveConfig(w.config, ConfigPath())
					}
					w.step = StepBranch
					w.cursor = 0
				}
			case "esc":
				w.editingEnvToken = false
				w.envTokenValue = ""
				w.step = StepProvider
				w.cursor = w.selectedProvider
			case "backspace":
				if len(w.envTokenValue) > 0 {
					w.envTokenValue = w.envTokenValue[:len(w.envTokenValue)-1]
				}
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if r >= ' ' && r <= '~' {
							w.envTokenValue += string(r)
						}
					}
				}
			}
			return w, nil
		}

		// Branch filtering mode (activated by "/" on StepBranch).
		if w.branchFilterActive {
			switch msg.String() {
			case "esc":
				if w.branchFilter != "" {
					w.branchFilter = ""
					w.rebuildBranchFilter()
				} else {
					w.branchFilterActive = false
				}
			case "enter":
				w.branchFilterActive = false
				if len(w.filteredBranches) > 0 {
					return w.advance()
				}
			case "backspace":
				if len(w.branchFilter) > 0 {
					w.branchFilter = w.branchFilter[:len(w.branchFilter)-1]
					w.rebuildBranchFilter()
					if w.cursor >= len(w.filteredBranches) {
						w.cursor = max(0, len(w.filteredBranches)-1)
					}
				}
			case "up", "k":
				if w.cursor > 0 {
					w.cursor--
				}
				return w, nil
			case "down", "j":
				w.cursor = min(w.cursor+1, len(w.filteredBranches)-1)
				return w, nil
			default:
				if msg.Type == tea.KeyRunes {
					for _, r := range msg.Runes {
						if r >= ' ' && r <= '~' {
							w.branchFilter += string(r)
						}
					}
					w.rebuildBranchFilter()
					w.cursor = 0
				}
			}
			return w, nil
		}

		switch msg.String() {
		case "up", "k":
			if w.cursor > 0 {
				w.cursor--
			}
		case "down", "j":
			w.cursor = min(w.cursor+1, w.listLen()-1)
		case "enter":
			return w.advance()
		case "esc":
			return w.goBack()
		case "/":
			// Activate search/filter on StepBranch.
			if w.step == StepBranch {
				w.branchFilterActive = true
				w.branchFilter = ""
				w.rebuildBranchFilter()
				w.cursor = 0
			}
		}
	}
	return w, nil
}

// isValidNameChar returns true for characters allowed in worktree directory names.
func isValidNameChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.'
}

// isValidBranchChar returns true for characters allowed in git branch names.
func isValidBranchChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' || c == '/'
}

// isValidPathChar returns true for characters allowed in file system paths.
func isValidPathChar(c byte) bool {
	return (c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') ||
		(c >= '0' && c <= '9') || c == '-' || c == '_' || c == '.' ||
		c == '/' || c == '~' || c == ' '
}

// View renders the current wizard step.
func (w WizardModel) View() string {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	b.WriteString(title.Render("New Session"))
	b.WriteString("\n\n")

	// Step indicator.
	steps := []string{"Directory", "Type", "Project", "Persona", "Provider", "Env", "Branch", "Worktree", "Permissions", "Confirm"}
	var stepLine strings.Builder
	for i, s := range steps {
		if WizardStep(i) == w.step {
			stepLine.WriteString(lipgloss.NewStyle().Bold(true).Foreground(accentColor).Render(fmt.Sprintf("[%s]", s)))
		} else if WizardStep(i) < w.step {
			stepLine.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" %s ", s)))
		} else {
			stepLine.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" %s ", s)))
		}
		if i < len(steps)-1 {
			stepLine.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(" > "))
		}
	}
	b.WriteString(stepLine.String())
	b.WriteString("\n\n")

	switch w.step {
	case StepWorkDir:
		if w.editingWorkDir {
			b.WriteString("Enter project directory path:\n\n")
			b.WriteString(fmt.Sprintf("  Path: %s", w.workDirInput))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			if w.workDirErr != "" {
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render("  " + w.workDirErr))
			}
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel  (supports ~/...)"))
		} else {
			b.WriteString("Select project directory:\n\n")
			for i, opt := range w.dirOpts {
				cursor := "  "
				if i == w.cursor {
					cursor = "> "
				}
				if i == 0 {
					// "[+] Enter new path" — render with accent color.
					b.WriteString(fmt.Sprintf("%s%s\n", cursor, lipgloss.NewStyle().Foreground(accentColor).Render(opt)))
				} else {
					// History entry — show directory path, check if valid.
					label := opt
					if !isGitRepo(opt) {
						label += lipgloss.NewStyle().Foreground(dimColor).Render(" (not found)")
					}
					b.WriteString(fmt.Sprintf("%s%s\n", cursor, label))
				}
			}
		}

	case StepSessionType:
		b.WriteString("Select session type:\n\n")
		descriptions := []string{
			"Plain coding session, no project management",
			"Managed session with persona, project tracking, and autonomous prompt",
		}
		for i, opt := range w.sessionTypeOpts {
			cursor := "  "
			if i == w.cursor {
				cursor = "> "
			}
			desc := lipgloss.NewStyle().Foreground(dimColor).Render(" — " + descriptions[i])
			b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, opt, desc))
		}

	case StepProject:
		header := "Select a project:"
		if w.projectFilter != "" {
			header += lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" (filter: %s)", w.projectFilter))
		}
		b.WriteString(header + "\n\n")
		if w.projectErr != "" {
			b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(w.projectErr))
			b.WriteString("\n")
		}
		if len(w.filteredProjects) == 0 {
			b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("  No projects found."))
			b.WriteString("\n")
		} else {
			for i, idx := range w.filteredProjects {
				cursor := "  "
				if i == w.cursor {
					cursor = "> "
				}
				p := w.projects[idx]
				status := lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" [%s]", p.Status))
				b.WriteString(fmt.Sprintf("%s%s%s\n", cursor, p.Name, status))
			}
		}

	case StepPersona:
		b.WriteString("Select your role:\n\n")
		for i, p := range w.personas {
			cursor := "  "
			if i == w.cursor {
				cursor = "> "
			}
			desc := lipgloss.NewStyle().Foreground(dimColor).Render(" — " + p.description)
			b.WriteString(fmt.Sprintf("%s%-16s%s\n", cursor, p.displayName, desc))
		}

	case StepProvider:
		if w.editingBinary {
			pe := w.providers[w.selectedProvider]
			b.WriteString(fmt.Sprintf("Binary %q not found. Enter full path:\n\n", pe.provider.Binary))
			b.WriteString(fmt.Sprintf("  Path: %s", w.binaryPath))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			if w.binaryPathErr != "" {
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render("  " + w.binaryPathErr))
			}
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel"))
		} else {
			b.WriteString("Select a provider:\n\n")
			for i, pe := range w.providers {
				cursor := "  "
				if i == w.cursor {
					cursor = "> "
				}
				name := pe.provider.Name
				if !pe.available {
					name = lipgloss.NewStyle().Foreground(dimColor).Render(name + " (not installed)")
				} else {
					color, ok := providerColors[pe.key]
					if !ok {
						color = accentColor
					}
					dot := lipgloss.NewStyle().Foreground(color).Render("●")
					name = fmt.Sprintf("%s %s", name, dot)
				}
				b.WriteString(fmt.Sprintf("%s%s\n", cursor, name))
			}
		}

	case StepEnvToken:
		b.WriteString(fmt.Sprintf("Enter value for %s:\n\n", w.envTokenVarName))
		b.WriteString(fmt.Sprintf("  %s: %s", w.envTokenVarName, w.envTokenValue))
		b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
		b.WriteString("\n\n")
		b.WriteString(helpStyle.Render("enter: confirm  esc: back"))

	case StepBranch:
		if w.editingBranch {
			b.WriteString("New branch name:\n\n")
			b.WriteString(fmt.Sprintf("  Branch: %s", w.newBranchName))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel  (a-z, 0-9, -, _, ., /)"))
		} else {
			// Count branches with worktrees for header annotation.
			wtCount := 0
			if w.existingWorktrees != nil {
				wtCount = len(w.existingWorktrees)
			}
			header := "Select a branch:"
			if wtCount > 0 {
				header += lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" (%d with worktrees)", wtCount))
			}
			b.WriteString(header)
			b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf(" [%d total]", len(w.branches)-1)))
			b.WriteString("\n")

			// Show filter input if active, or hint.
			if w.branchFilterActive {
				b.WriteString(fmt.Sprintf("  Filter: %s", w.branchFilter))
				b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
				if len(w.filteredBranches) <= 1 {
					b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("  (no matches)"))
				} else {
					b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("  (%d matches)", len(w.filteredBranches)-1)))
				}
			} else if w.branchFilter != "" {
				b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("  Filtered: %q (%d matches)", w.branchFilter, len(w.filteredBranches)-1)))
			}
			b.WriteString("\n")

			// Scrolling viewport: show at most 15 items centered on cursor.
			const maxVisible = 15
			total := len(w.filteredBranches)
			startIdx := 0
			endIdx := total
			if total > maxVisible {
				startIdx = w.cursor - maxVisible/2
				if startIdx < 0 {
					startIdx = 0
				}
				endIdx = startIdx + maxVisible
				if endIdx > total {
					endIdx = total
					startIdx = endIdx - maxVisible
				}
			}

			if startIdx > 0 {
				b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("  ▲ %d more above\n", startIdx)))
			}

			for vi := startIdx; vi < endIdx; vi++ {
				branchIdx := w.filteredBranches[vi]
				br := w.branches[branchIdx]
				cursor := "  "
				if vi == w.cursor {
					cursor = "> "
				}
				label := br
				if branchIdx == 0 {
					// First item is "[+] Create new branch" — render with accent color.
					label = lipgloss.NewStyle().Foreground(accentColor).Render(br)
				} else if wtPath := w.findWorktreeForBranch(br); wtPath != "" {
					// Annotate branch with existing worktree path.
					shortPath := wtPath
					if len(shortPath) > 30 {
						shortPath = "..." + shortPath[len(shortPath)-27:]
					}
					label += " " + lipgloss.NewStyle().Foreground(dimColor).Render("[wt: "+shortPath+"]")
				}
				b.WriteString(fmt.Sprintf("%s%s\n", cursor, label))
			}

			if endIdx < total {
				b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("  ▼ %d more below\n", total-endIdx)))
			}
		}

	case StepWorktree:
		if w.editingName {
			b.WriteString("Worktree name:\n\n")
			b.WriteString(fmt.Sprintf("  Name: %s", w.worktreeName))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel  (a-z, 0-9, -, _, .)"))
		} else if w.editingCustomDir {
			b.WriteString("Custom worktree base directory:\n\n")
			b.WriteString(fmt.Sprintf("  Path: %s", w.customBaseDir))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			if w.customDirErr != "" {
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render("  " + w.customDirErr))
			}
			b.WriteString("\n\n")
			br := w.resolvedBranch()
			safeBr := strings.ReplaceAll(br, "/", "-")
			pe := w.providers[w.selectedProvider]
			preview := fmt.Sprintf("%s/%s-%s", w.customBaseDir, pe.key, safeBr)
			if w.customBaseDir != "" {
				b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render(fmt.Sprintf("  Worktree will be at: %s", preview)))
				b.WriteString("\n")
			}
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel"))
		} else if w.editingSpecWorkDir {
			b.WriteString("Working directory path:\n\n")
			b.WriteString(fmt.Sprintf("  Path: %s", w.specifiedWorkDir))
			b.WriteString(lipgloss.NewStyle().Foreground(accentColor).Render("█"))
			if w.specifiedWorkDirErr != "" {
				b.WriteString("\n")
				b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render("  " + w.specifiedWorkDirErr))
			}
			b.WriteString("\n\n")
			b.WriteString(helpStyle.Render("enter: confirm  esc: cancel  (pre-filled with current directory)"))
		} else {
			b.WriteString("Worktree mode:\n\n")
			for i, opt := range w.worktreeOpts {
				cursor := "  "
				if i == w.cursor {
					cursor = "> "
				}
				b.WriteString(fmt.Sprintf("%s%s\n", cursor, opt))
			}
		}

	case StepPermissions:
		b.WriteString("Permission mode:\n\n")
		for i, opt := range w.permissionOpts {
			cursor := "  "
			if i == w.cursor {
				cursor = "> "
			}
			b.WriteString(fmt.Sprintf("%s%s\n", cursor, opt))
		}

	case StepConfirm:
		b.WriteString("Confirm session:\n\n")
		if w.selectedWorkDir != "" {
			b.WriteString(fmt.Sprintf("  Directory:     %s\n", w.selectedWorkDir))
		}
		sessionType := "Vanilla"
		if w.selectedSessionType == 1 {
			sessionType = "VibeFlow (managed)"
		}
		b.WriteString(fmt.Sprintf("  Session Type:  %s\n", sessionType))
		if w.selectedSessionType == 1 && w.selectedProject < len(w.projects) {
			b.WriteString(fmt.Sprintf("  Project:       %s\n", w.projects[w.selectedProject].Name))
		}
		if w.selectedSessionType == 1 && w.selectedPersona < len(w.personas) {
			b.WriteString(fmt.Sprintf("  Persona:       %s\n", w.personas[w.selectedPersona].displayName))
		}
		pe := w.providers[w.selectedProvider]
		b.WriteString(fmt.Sprintf("  Provider:      %s\n", pe.provider.Name))
		branchDisplay := w.resolvedBranch()
		if w.selectedBranch == 0 {
			branchDisplay += " (new)"
		}
		b.WriteString(fmt.Sprintf("  Branch:        %s\n", branchDisplay))
		wt := "Current directory"
		if w.selectedWorktree < len(w.worktreeOpts) {
			opt := w.worktreeOpts[w.selectedWorktree]
			switch {
			case strings.HasPrefix(opt, "Use existing:"):
				if path := w.findWorktreeForBranch(w.resolvedBranch()); path != "" {
					wt = fmt.Sprintf("Existing worktree (%s)", path)
				}
			case opt == "New worktree":
				wt = fmt.Sprintf("New worktree (%s)", w.worktreeName)
			case opt == "Custom location":
				resolvedPath := fmt.Sprintf("%s/%s", w.customBaseDir, w.worktreeName)
				wt = fmt.Sprintf("Custom (%s)", resolvedPath)
			case opt == "Specify directory":
				wt = fmt.Sprintf("Directory (%s)", w.specifiedWorkDir)
			}
		}
		b.WriteString(fmt.Sprintf("  Worktree:      %s\n", wt))
		perm := "Interactive"
		if w.selectedPermission == 0 {
			perm = "Skip permissions"
		}
		b.WriteString(fmt.Sprintf("  Permissions:   %s\n", perm))
		b.WriteString("\n")
		b.WriteString(helpStyle.Render("enter: create  esc: back"))
		return b.String()
	}

	b.WriteString("\n")
	if w.step == StepBranch && w.branchFilterActive {
		b.WriteString(helpStyle.Render("type to filter  enter: select  esc: clear filter  j/k: navigate"))
	} else if w.step == StepBranch {
		b.WriteString(helpStyle.Render("j/k: navigate  /: filter  enter: select  esc: back"))
	} else {
		b.WriteString(helpStyle.Render("j/k: navigate  enter: select  esc: back/cancel"))
	}
	return b.String()
}

func (w WizardModel) listLen() int {
	switch w.step {
	case StepWorkDir:
		return len(w.dirOpts)
	case StepSessionType:
		return len(w.sessionTypeOpts)
	case StepProject:
		return len(w.filteredProjects)
	case StepPersona:
		return len(w.personas)
	case StepProvider:
		return len(w.providers)
	case StepEnvToken:
		return 1
	case StepBranch:
		return len(w.filteredBranches)
	case StepWorktree:
		return len(w.worktreeOpts)
	case StepPermissions:
		return len(w.permissionOpts)
	case StepConfirm:
		return 1 // Single "Create" action; prevents cursor going negative.
	default:
		return 0
	}
}

func (w WizardModel) advance() (WizardModel, tea.Cmd) {
	switch w.step {
	case StepWorkDir:
		if w.cursor == 0 {
			// "[+] Enter new path" — open text input.
			cwd, _ := os.Getwd()
			w.workDirInput = cwd
			w.workDirErr = ""
			w.editingWorkDir = true
			return w, nil
		}
		// History entry selected — validate and advance.
		dir := w.dirOpts[w.cursor]
		if !isGitRepo(dir) {
			// Directory no longer valid — ignore selection.
			return w, nil
		}
		w.selectedWorkDir = dir
		w.reloadBranchesForDir(dir)
		w.step = StepSessionType
		w.cursor = 0
	case StepSessionType:
		w.selectedSessionType = w.cursor
		if w.cursor == 1 { // VibeFlow
			w.step = StepProject
			w.cursor = 0
			// Pre-select default project if configured.
			if w.defaultProject != "" {
				for i, idx := range w.filteredProjects {
					if w.projects[idx].Name == w.defaultProject {
						w.cursor = i
						break
					}
				}
			}
			// Activate filter mode for easy searching.
			w.projectFilterActive = true
		} else { // Vanilla
			w.step = StepProvider
			w.cursor = 0
		}
	case StepProject:
		if len(w.filteredProjects) > 0 && w.cursor < len(w.filteredProjects) {
			w.selectedProject = w.filteredProjects[w.cursor]
		}
		w.projectFilterActive = false
		w.step = StepPersona
		w.cursor = 0 // "developer" is index 0 (pre-selected default)
	case StepPersona:
		w.selectedPersona = w.cursor
		w.step = StepProvider
		w.cursor = 0
	case StepProvider:
		w.selectedProvider = w.cursor
		if w.cursor < len(w.providers) && !w.providers[w.cursor].available {
			// Provider binary not found — prompt for absolute path.
			w.binaryPath = ""
			w.binaryPathErr = ""
			w.editingBinary = true
			return w, nil
		}
		// Check if provider needs env token (e.g. codex bearer_token_env_var).
		pe := w.providers[w.cursor]
		env, missing := ResolveProviderEnvVars(w.config, pe.key)
		if missing != "" {
			w.envTokenVarName = missing
			w.envTokenValue = ""
			w.editingEnvToken = true
			w.envVars = env
			w.step = StepEnvToken
			return w, nil
		}
		w.envVars = env
		w.step = StepBranch
		w.cursor = 0
	case StepEnvToken:
		// Re-enter editing if not already done.
		w.editingEnvToken = true
		return w, nil
	case StepBranch:
		// Map cursor through filter to actual branch index.
		if w.cursor >= len(w.filteredBranches) {
			return w, nil
		}
		actualIdx := w.filteredBranches[w.cursor]
		w.selectedBranch = actualIdx
		if actualIdx == 0 {
			// "[+] Create new branch" selected — prompt for branch name.
			w.newBranchName = ""
			w.editingBranch = true
			return w, nil
		}
		// Clear filter state after selection.
		w.branchFilter = ""
		w.branchFilterActive = false
		// Rebuild worktree options based on selected branch.
		w.rebuildWorktreeOpts()
		w.step = StepWorktree
		w.cursor = 0
	case StepWorktree:
		w.selectedWorktree = w.cursor
		opt := w.worktreeOpts[w.cursor]
		switch {
		case strings.HasPrefix(opt, "Use existing:"):
			// Reuse existing worktree — skip to permissions.
			w.step = StepPermissions
			w.cursor = 0
		case opt == "New worktree":
			// Prompt for custom name.
			pe := w.providers[w.selectedProvider]
			br := w.resolvedBranch()
			safeBr := strings.ReplaceAll(br, "/", "-")
			w.worktreeName = fmt.Sprintf("%s-%s", pe.key, safeBr)
			w.editingName = true
			return w, nil
		case opt == "Custom location":
			// Prompt for custom base directory path.
			w.customBaseDir = ""
			w.customDirErr = ""
			w.editingCustomDir = true
			return w, nil
		case opt == "Specify directory":
			// Prompt for working directory path, pre-fill with CWD.
			cwd, _ := os.Getwd()
			w.specifiedWorkDir = cwd
			w.specifiedWorkDirErr = ""
			w.editingSpecWorkDir = true
			return w, nil
		default:
			// "Current directory"
			w.step = StepPermissions
			w.cursor = 0
		}
	case StepPermissions:
		w.selectedPermission = w.cursor
		w.step = StepConfirm
		w.cursor = 0
	case StepConfirm:
		pe := w.providers[w.selectedProvider]
		// Determine worktree choice from selected option text.
		wtChoice := WorktreeCurrent
		var existingPath string
		if w.selectedWorktree < len(w.worktreeOpts) {
			opt := w.worktreeOpts[w.selectedWorktree]
			switch {
			case strings.HasPrefix(opt, "Use existing:"):
				wtChoice = WorktreeExisting
				existingPath = w.findWorktreeForBranch(w.resolvedBranch())
			case opt == "New worktree":
				wtChoice = WorktreeNew
			case opt == "Custom location":
				wtChoice = WorktreeCustom
			case opt == "Specify directory":
				wtChoice = WorktreeSpecifyDir
			}
		}
		prov := pe.provider
		if w.binaryPath != "" {
			prov.Binary = w.binaryPath
		}
		sessionType := "vanilla"
		if w.selectedSessionType == 1 {
			sessionType = "vibeflow"
		}
		var projectID int64
		var projectName string
		if sessionType == "vibeflow" && w.selectedProject < len(w.projects) {
			projectID = w.projects[w.selectedProject].ID
			projectName = w.projects[w.selectedProject].Name
		}
		var persona string
		if sessionType == "vibeflow" && w.selectedPersona < len(w.personas) {
			persona = w.personas[w.selectedPersona].key
		}
		w.result = WizardResult{
			SessionType:          sessionType,
			ProjectID:            projectID,
			ProjectName:          projectName,
			Persona:              persona,
			Provider:             prov,
			ProviderKey:          pe.key,
			Branch:               w.resolvedBranch(),
			NewBranch:            w.selectedBranch == 0,
			WorktreeChoice:       wtChoice,
			SkipPermissions:      w.selectedPermission == 0,
			WorktreeName:         w.worktreeName,
			CustomBinaryPath:     w.binaryPath,
			ExistingWorktreePath: existingPath,
			CustomBaseDir:        w.customBaseDir,
			SpecifiedWorkDir:     w.specifiedWorkDir,
			WorkDir:              w.selectedWorkDir,
			EnvVars:              w.envVars,
		}
		w.done = true
	}
	return w, nil
}

// rebuildProjectFilter updates filteredProjects based on the current projectFilter text.
func (w *WizardModel) rebuildProjectFilter() {
	if w.projectFilter == "" {
		w.filteredProjects = make([]int, len(w.projects))
		for i := range w.projects {
			w.filteredProjects[i] = i
		}
		return
	}
	lower := strings.ToLower(w.projectFilter)
	w.filteredProjects = w.filteredProjects[:0]
	for i, p := range w.projects {
		if strings.Contains(strings.ToLower(p.Name), lower) {
			w.filteredProjects = append(w.filteredProjects, i)
		}
	}
}

// rebuildBranchFilter updates filteredBranches based on the current branchFilter text.
// Index 0 ("[+] Create new branch") is always included.
func (w *WizardModel) rebuildBranchFilter() {
	if w.branchFilter == "" {
		w.filteredBranches = make([]int, len(w.branches))
		for i := range w.branches {
			w.filteredBranches[i] = i
		}
		return
	}
	lower := strings.ToLower(w.branchFilter)
	w.filteredBranches = w.filteredBranches[:0]
	w.filteredBranches = append(w.filteredBranches, 0) // always keep "[+] Create new branch"
	for i := 1; i < len(w.branches); i++ {
		if strings.Contains(strings.ToLower(w.branches[i]), lower) {
			w.filteredBranches = append(w.filteredBranches, i)
		}
	}
}

// max returns the larger of a or b.
func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

// rebuildWorktreeOpts rebuilds the worktree options list based on whether the
// selected branch already has an existing worktree.
func (w *WizardModel) rebuildWorktreeOpts() {
	branch := w.resolvedBranch()
	if wtPath := w.findWorktreeForBranch(branch); wtPath != "" {
		// Shorten path for display.
		display := wtPath
		if len(display) > 40 {
			display = "..." + display[len(display)-37:]
		}
		w.worktreeOpts = []string{
			fmt.Sprintf("Use existing: %s", display),
			"New worktree",
			"Custom location",
			"Specify directory",
			"Current directory",
		}
	} else {
		w.worktreeOpts = []string{"New worktree", "Custom location", "Specify directory", "Current directory"}
	}
}

// findWorktreeForBranch returns the worktree path for a branch, checking both
// the exact name and without a remote prefix (e.g. "origin/feature" → "feature").
func (w WizardModel) findWorktreeForBranch(branch string) string {
	if w.existingWorktrees == nil {
		return ""
	}
	if path, ok := w.existingWorktrees[branch]; ok {
		return path
	}
	// Strip remote prefix (e.g. "origin/feature" → "feature").
	if idx := strings.Index(branch, "/"); idx >= 0 {
		short := branch[idx+1:]
		if path, ok := w.existingWorktrees[short]; ok {
			return path
		}
	}
	return ""
}

func (w WizardModel) goBack() (WizardModel, tea.Cmd) {
	switch w.step {
	case StepWorkDir:
		w.cancelled = true
	case StepSessionType:
		w.step = StepWorkDir
		w.cursor = 0
	case StepProject:
		w.projectFilterActive = false
		w.projectFilter = ""
		w.rebuildProjectFilter()
		w.step = StepSessionType
		w.cursor = w.selectedSessionType
	case StepPersona:
		w.step = StepProject
		w.cursor = 0
		w.projectFilterActive = true
	case StepProvider:
		if w.selectedSessionType == 1 { // VibeFlow — go back to persona
			w.step = StepPersona
			w.cursor = w.selectedPersona
		} else { // Vanilla — go back to session type
			w.step = StepSessionType
			w.cursor = w.selectedSessionType
		}
	case StepEnvToken:
		w.step = StepProvider
		w.cursor = w.selectedProvider
	case StepBranch:
		w.step = StepProvider
		w.cursor = w.selectedProvider
	case StepWorktree:
		w.step = StepBranch
		// Restore cursor to the position in the filtered list.
		w.cursor = 0
		for i, idx := range w.filteredBranches {
			if idx == w.selectedBranch {
				w.cursor = i
				break
			}
		}
	case StepPermissions:
		w.step = StepWorktree
		w.cursor = w.selectedWorktree
	case StepConfirm:
		w.step = StepPermissions
		w.cursor = w.selectedPermission
	}
	return w, nil
}

// resolvedBranch returns the actual branch name — either the new branch name
// typed by the user or the selected existing branch.
func (w WizardModel) resolvedBranch() string {
	if w.selectedBranch == 0 {
		return w.newBranchName
	}
	return w.branches[w.selectedBranch]
}

// reloadBranchesForDir re-fetches git branches and worktree info for a new directory.
func (w *WizardModel) reloadBranchesForDir(dir string) {
	branches := listGitBranches(dir)
	if len(branches) == 0 {
		branches = []string{"main"}
	}
	branches = append([]string{"[+] Create new branch"}, branches...)
	w.branches = branches

	// Reset branch filter.
	w.branchFilter = ""
	w.branchFilterActive = false
	w.filteredBranches = make([]int, len(branches))
	for i := range branches {
		w.filteredBranches[i] = i
	}

	// Rebuild worktree map for the new directory.
	wm, err := NewWorktreeManager(dir, ".claude/worktrees")
	if err == nil && wm != nil {
		w.existingWorktrees = wm.BranchWorktreeMap()
	} else {
		w.existingWorktrees = nil
	}
}

// isGitRepo checks whether the given directory is inside a git repository.
func isGitRepo(dir string) bool {
	cmd := exec.Command("git", "-C", dir, "rev-parse", "--is-inside-work-tree")
	return cmd.Run() == nil
}

// providerKeys returns sorted provider keys from the registry.
func providerKeys(r *ProviderRegistry) []string {
	list := r.List()
	keys := make([]string, 0, len(list))
	// List() returns sorted by name; we need keys.
	// Re-derive by matching names — or just iterate the map via a method.
	// Since ProviderRegistry doesn't expose keys directly, we check known keys.
	// Better approach: iterate all and collect.
	seen := make(map[string]bool)
	for _, p := range list {
		for _, candidate := range []string{"claude", "codex", "gemini"} {
			if got, ok := r.Get(candidate); ok && got.Name == p.Name && !seen[candidate] {
				keys = append(keys, candidate)
				seen[candidate] = true
				break
			}
		}
		// Fallback for custom providers — use name as key.
		if !seen[p.Name] {
			// Try lowercase name.
			lower := strings.ToLower(p.Name)
			if _, ok := r.Get(lower); ok && !seen[lower] {
				keys = append(keys, lower)
				seen[lower] = true
			}
		}
	}
	return keys
}

// listGitBranches returns local and unique remote branch names via git CLI.
func listGitBranches(repoRoot string) []string {
	// Get local branches.
	cmd := exec.Command("git", "-C", repoRoot, "branch", "--format=%(refname:short)")
	out, err := cmd.Output()
	if err != nil {
		return nil
	}
	seen := make(map[string]bool)
	var branches []string
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line = strings.TrimSpace(line); line != "" {
			branches = append(branches, line)
			seen[line] = true
		}
	}

	// Get remote branches and add those not already in local.
	cmd2 := exec.Command("git", "-C", repoRoot, "branch", "-r", "--format=%(refname:short)")
	out2, _ := cmd2.Output()
	for _, line := range strings.Split(strings.TrimSpace(string(out2)), "\n") {
		if line = strings.TrimSpace(line); line == "" {
			continue
		}
		// Skip HEAD pointer (e.g. "origin/HEAD").
		if strings.HasSuffix(line, "/HEAD") {
			continue
		}
		// Strip remote prefix for display (e.g. "origin/feature" → "feature").
		short := line
		if idx := strings.Index(line, "/"); idx >= 0 {
			short = line[idx+1:]
		}
		if !seen[short] {
			branches = append(branches, line)
			seen[short] = true
		}
	}

	return branches
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
