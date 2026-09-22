package vibeflowcli

import (
	"context"
	"errors"
	"fmt"
	"os/exec"
	"sort"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

type reviewDiscoveryRefreshMsg struct{}
type reviewDiscoveryTickMsg struct{}
type reviewDiscoveryMsg struct {
	statuses     []reviewRunnerStatus
	warning, key string
}
type reviewRunnerSnapshotMsg struct{ statuses []reviewRunnerStatus }
type reviewCheckoutSavedMsg struct {
	id, path string
	err      error
}

func reviewDiscoveryTickCmd() tea.Cmd {
	return tea.Tick(time.Minute, func(time.Time) tea.Msg { return reviewDiscoveryTickMsg{} })
}
func copyReviewPreferences(prefs map[string]string) map[string]string {
	result := map[string]string{}
	for k, v := range prefs {
		result[k] = v
	}
	return result
}

func (m Model) reviewPathsKey() string {
	paths := append([]string(nil), m.reviewPaths...)
	for id, path := range m.reviewPreferences {
		paths = append(paths, id+"="+path)
	}
	sort.Strings(paths)
	var unique []string
	for _, path := range paths {
		if len(unique) == 0 || unique[len(unique)-1] != path {
			unique = append(unique, path)
		}
	}
	return strings.Join(unique, "\x00")
}

func (m *Model) requestReviewDiscovery() tea.Cmd {
	if m.reviewSupervisor == nil {
		return nil
	}
	if m.reviewDiscoveryBusy {
		m.reviewDiscoveryAgain = true
		return nil
	}
	m.reviewDiscoveryBusy = true
	s := m.reviewSupervisor
	paths, prefs := append([]string(nil), m.reviewPaths...), copyReviewPreferences(m.reviewPreferences)
	key := m.reviewPathsKey()
	m.reviewDiscoveryKey = key
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(s.ctx, time.Minute)
		defer cancel()
		d, err := discoverReviewBindings(ctx, s.cfg, paths, prefs)
		warning := d.Warning
		if err != nil {
			warning = err.Error()
			// A failed page never authorizes removal of partially enumerated scope.
			var response *reviewHTTPError
			d.Complete = errors.As(err, &response) && (response.Status == 401 || response.Status == 403)
			if !d.Complete {
				d.Projects = nil
				d.Bindings = nil
			}
		}
		var ids []int64
		for id := range d.Problems {
			ids = append(ids, id)
		}
		sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
		for _, id := range ids {
			if warning != "" {
				warning += "; "
			}
			warning += fmt.Sprintf("Project %d: %s", id, d.Problems[id])
		}
		return reviewDiscoveryMsg{statuses: s.Reconcile(d), warning: warning, key: key}
	}
}

func (m Model) reviewSnapshotCmd() tea.Cmd {
	if m.reviewSupervisor == nil {
		return nil
	}
	s := m.reviewSupervisor
	return func() tea.Msg { return reviewRunnerSnapshotMsg{statuses: s.Snapshot()} }
}

func (m Model) craReviewSessions() tea.Cmd {
	if !m.craEnabled {
		return nil
	}
	return m.refreshReviewSessions
}

func (m Model) updateReviewRunners(msg tea.Msg) (tea.Model, tea.Cmd) {
	if size, ok := msg.(tea.WindowSizeMsg); ok && m.reviewCheckout != nil {
		editor := *m.reviewCheckout
		editor.width, editor.height = size.Width, size.Height
		m.reviewCheckout = &editor
	}
	if m.reviewCheckout != nil {
		if key, ok := msg.(tea.KeyPressMsg); ok {
			if key.String() == "esc" {
				m.reviewCheckout = nil
				m.reviewCheckoutError = ""
				return m, nil
			}
			if key.String() == "ctrl+c" {
				m.quitting = true
				return m, tea.Quit
			}
		}
		if m.reviewCheckout.busy {
			return m, nil
		}
		next, _ := m.reviewCheckout.Update(msg)
		editor := next.(reviewStartupModel)
		m.reviewCheckout = &editor
		// The shared editor resolves only UI input here; checkout validation and
		// persistence run as one command, with immutable binding/config inputs.
		if editor.busy {
			var binding reviewBinding
			for _, row := range m.reviewStatuses {
				if row.BindingID == m.reviewCheckoutID {
					binding = row.Binding
				}
			}
			path, id, s := editor.options.Repository, m.reviewCheckoutID, m.reviewSupervisor
			prefs := copyReviewPreferences(m.reviewPreferences)
			return m, func() tea.Msg {
				ctx, cancel := context.WithTimeout(s.ctx, 20*time.Second)
				defer cancel()
				checkout := findReviewStartupCheckout(ctx, path, []reviewStartupRepository{binding.Repository})
				if checkout == nil {
					return reviewCheckoutSavedMsg{err: fmt.Errorf("Path must match %s/%s. Enter another path or Esc.", binding.Repository.Host, binding.Repository.Name)}
				}
				prefs[id] = checkout.Path
				err := saveReviewGroupPreferences(s.cfg, s.configPath, s.options, prefs)
				return reviewCheckoutSavedMsg{id: id, path: checkout.Path, err: err}
			}
		}
		return m, nil
	}
	key, ok := msg.(tea.KeyPressMsg)
	if !ok {
		return m, nil
	}
	switch key.String() {
	case "esc", "R":
		m.activeView = ViewSessions
	case "ctrl+c":
		m.quitting = true
		return m, tea.Quit
	case "up", "k":
		if m.reviewRunnerCursor > 0 {
			m.reviewRunnerCursor--
		}
	case "down", "j":
		if m.reviewRunnerCursor+1 < len(m.reviewStatuses) {
			m.reviewRunnerCursor++
		}
	case "r":
		cmd := m.requestReviewDiscovery()
		return m, cmd
	case "enter":
		if m.reviewSupervisor == nil || m.reviewRunnerCursor >= len(m.reviewStatuses) {
			return m, nil
		}
		row := m.reviewStatuses[m.reviewRunnerCursor]
		if row.State != "needs_checkout" && row.State != "failed" {
			return m, nil
		}
		editor := newReviewStartupModel(m.reviewSupervisor.ctx, m.reviewSupervisor.cfg, m.reviewSupervisor.configPath, row.Binding.Options)
		editor.width, editor.height = m.width, m.height
		message := fmt.Sprintf("%s: enter a checkout for %s/%s.", row.Binding.ProjectName, row.Binding.Repository.Host, row.Binding.Repository.Name)
		editor.input = &reviewStartupInput{Field: "repository", Message: message}
		if len(row.Binding.Checkouts) > 0 {
			editor.input = &reviewStartupInput{Field: "repository_choice", Message: "Choose a local checkout for " + row.Binding.Repository.Name + ".", ManualMessage: message}
			for _, checkout := range row.Binding.Checkouts {
				editor.input.Choices = append(editor.input.Choices, reviewStartupChoice{Label: checkout.Path, Value: checkout.Path})
			}
			editor.input.Choices = append(editor.input.Choices, reviewStartupChoice{Label: "Enter another path", Value: "manual"})
		}
		m.reviewCheckout, m.reviewCheckoutID = &editor, row.BindingID
	}
	return m, nil
}

func (m Model) viewReviewRunners() string {
	if m.reviewCheckout != nil {
		editor := *m.reviewCheckout
		if m.reviewCheckoutError != "" && editor.input != nil {
			input := *editor.input
			input.Message = m.reviewCheckoutError
			editor.input = &input
		}
		return strings.Replace(editor.View().Content, "Run PR reviews while this CLI is open?", "Choose review checkout", 1)
	}
	width, height := max(20, m.width), max(8, m.height)
	var b strings.Builder
	b.WriteString("PR review runners\n\n")
	if m.reviewSupervisor == nil {
		b.WriteString("Reviews were declined for this CLI session.\n")
	}
	if m.reviewDiscoveryWarning != "" {
		b.WriteString(ansi.Truncate(m.reviewDiscoveryWarning, width, "…") + "\n")
	}
	if m.reviewDiscoveryBusy {
		b.WriteString("Discovering linked repositories...\n")
	}
	visible := max(1, (height-9)/2)
	start := max(0, m.reviewRunnerCursor-visible/2)
	for i := start; i < min(len(m.reviewStatuses), start+visible); i++ {
		row := m.reviewStatuses[i]
		prefix := "  "
		if i == m.reviewRunnerCursor {
			prefix = "> "
		}
		b.WriteString(ansi.Truncate(fmt.Sprintf("%s%s / %s [%s]", prefix, row.Binding.ProjectName, row.Binding.Repository.Name, row.State), width, "…") + "\n")
		message := row.Message
		if message == "" {
			message = row.Binding.Options.Repository
		}
		b.WriteString(ansi.Truncate("  "+message, width, "…") + "\n")
	}
	if len(m.reviewStatuses) == 0 && m.reviewSupervisor != nil && !m.reviewDiscoveryBusy {
		b.WriteString("No linked review repositories discovered.\n")
	}
	b.WriteString("\nEnter: checkout  r: refresh  Esc: sessions\n")
	return b.String()
}

// Session consent deliberately lives only in this model, never in Config.
type reviewStartupModel struct {
	ctx                context.Context
	cfg                *Config
	configPath         string
	options            reviewWatchOptions
	input              *reviewStartupInput
	text               string
	cursor             int
	yes                bool
	busy               bool
	done               bool
	enabled            bool
	quit               bool
	err                error
	runner             *reviewOwnedRunner
	width              int
	height             int
	owlFrame           int
	owlPaused          bool
	repositorySelected bool
}

type reviewOwlTickMsg struct{}

type reviewStartupResolvedMsg struct {
	options reviewWatchOptions
	input   *reviewStartupInput
	err     error
}

type reviewStartupReadyMsg struct {
	runner *reviewOwnedRunner
	err    error
}

func newReviewStartupModel(ctx context.Context, cfg *Config, configPath string, options reviewWatchOptions) reviewStartupModel {
	return reviewStartupModel{ctx: ctx, cfg: cfg, configPath: configPath, options: options}
}

func (m reviewStartupModel) Init() tea.Cmd { return reviewOwlTick() }

func reviewOwlTick() tea.Cmd {
	return tea.Tick(250*time.Millisecond, func(time.Time) tea.Msg { return reviewOwlTickMsg{} })
}

func (m reviewStartupModel) resolve() tea.Cmd {
	return func() tea.Msg {
		o := m.options
		var choices []reviewStartupChoice
		for _, name := range []string{"claude", "codex"} {
			if binary := m.cfg.Providers[name].Binary; binary != "" {
				if _, err := exec.LookPath(binary); err == nil {
					choices = append(choices, reviewStartupChoice{Label: name, Value: name})
				}
			}
		}
		available := false
		for _, choice := range choices {
			available = available || choice.Value == o.Provider
		}
		if !available {
			if len(choices) == 0 {
				return reviewStartupResolvedMsg{options: o, err: fmt.Errorf("install Claude or Codex and configure its binary in config.yaml, then press r")}
			}
			return reviewStartupResolvedMsg{options: o, input: &reviewStartupInput{Field: "provider", Message: "Choose an installed provider for all PR review runners.", Choices: choices}}
		}
		if (m.cfg.LLMGatewayEnabled && strings.TrimSpace(o.Model) == "") || (o.Model != "" && !reviewStartupText(o.Model, 200)) {
			return reviewStartupResolvedMsg{options: o, input: &reviewStartupInput{Field: "model", Message: "Enter the model for all PR review runners."}}
		}
		return reviewStartupResolvedMsg{options: o}
	}
}

func (m reviewStartupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if paste, ok := msg.(tea.PasteMsg); ok {
		msg = tea.KeyPressMsg{Text: paste.Content}
	}
	switch msg := msg.(type) {
	case reviewOwlTickMsg:
		if m.busy || m.input != nil || m.err != nil || m.done || m.quit {
			return m, nil
		}
		if !m.owlPaused {
			m.owlFrame = (m.owlFrame + 1) % 80
		}
		return m, reviewOwlTick()
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case reviewStartupResolvedMsg:
		m.busy, m.err = false, msg.err
		m.options, m.input = msg.options, msg.input
		m.cursor, m.text = 0, ""
		if m.err == nil && m.input == nil {
			m.enabled, m.done = true, true
			return m, tea.Quit
		}
	case reviewStartupReadyMsg:
		m.busy, m.err, m.runner = false, msg.err, msg.runner
		if msg.err == nil && msg.runner != nil {
			// This file holds only reusable inputs. Failing to remember them must
			// not stop a healthy runner or rewrite authentication configuration.
			_ = saveReviewStartupOptions(m.cfg, m.configPath, m.options)
			m.done = true
			return m, tea.Quit
		}
	case tea.KeyPressMsg:
		key := msg.String()
		if key == "ctrl+c" {
			m.quit = true
			return m, tea.Quit
		}
		if m.busy {
			return m, nil
		}
		if key == "esc" || (m.err != nil && key == "enter") {
			m.done = true
			return m, tea.Quit
		}
		if m.err != nil {
			if key == "r" {
				m.err, m.busy = nil, true
				return m, m.resolve()
			}
			return m, nil
		}
		if m.input == nil {
			switch key {
			case "space":
				m.owlPaused = !m.owlPaused
				return m, nil
			case "y":
				m.yes = true
			case "n":
				m.done = true
				return m, tea.Quit
			case "left", "right", "up", "down", "tab":
				m.yes = !m.yes
				return m, nil
			case "enter":
				if !m.yes {
					m.done = true
					return m, tea.Quit
				}
			default:
				return m, nil
			}
			m.busy = true
			return m, m.resolve()
		}
		if len(m.input.Choices) > 0 {
			switch key {
			case "up", "k":
				if m.cursor > 0 {
					m.cursor--
				}
			case "down", "j":
				if m.cursor+1 < len(m.input.Choices) {
					m.cursor++
				}
			case "enter":
				m.text = m.input.Choices[m.cursor].Value
			}
		} else {
			switch key {
			case "backspace":
				runes := []rune(m.text)
				if len(runes) > 0 {
					m.text = string(runes[:len(runes)-1])
				}
			default:
				for _, r := range msg.Text {
					if unicode.IsPrint(r) && len(m.text) < 4096 {
						m.text += string(r)
					}
				}
			}
		}
		if key == "enter" && strings.TrimSpace(m.text) != "" {
			value := strings.TrimSpace(m.text)
			if len(m.input.Choices) > 0 {
				value = m.input.Choices[m.cursor].Value
			}
			switch m.input.Field {
			case "project":
				m.options.Project, m.options.ProjectID = value, 0
				m.options.RepositoryLinkID = 0
				m.repositorySelected = false
			case "provider":
				m.options.Provider, m.options.Model = value, ""
			case "repository_choice":
				if value == "manual" {
					m.input = &reviewStartupInput{Field: "repository", Message: m.input.ManualMessage}
					m.cursor, m.text = 0, ""
					return m, nil
				}
				m.options.Repository, m.options.RepositoryLinkID = value, 0
				m.repositorySelected = true
			case "repository":
				m.options.Repository, m.options.RepositoryLinkID = value, 0
				m.repositorySelected = true
			case "model":
				m.options.Model = value
			case "repository_link":
				provider, id, _ := strings.Cut(value, ":")
				m.options.GitProvider = provider
				m.options.RepositoryLinkID, _ = strconv.ParseInt(id, 10, 64)
			default:
				m.err = fmt.Errorf("unsupported review setup field")
				return m, nil
			}
			m.busy = true
			return m, m.resolve()
		}
	}
	return m, nil
}

func (m reviewStartupModel) View() tea.View {
	width, height := m.width, m.height
	if width == 0 {
		width = 80
	}
	if height == 0 {
		height = 24
	}
	popupWidth := max(1, min(64, width-6))
	consent := !m.busy && m.err == nil && m.input == nil && !m.done && !m.quit
	if consent {
		popupWidth = max(1, min(92, width-6))
	}
	contentWidth := max(1, popupWidth-4)
	title := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	dim := lipgloss.NewStyle().Foreground(dimColor)
	var b strings.Builder
	b.WriteString(title.Render("Run PR reviews while this CLI is open?"))
	b.WriteString("\n\n")
	switch {
	case m.busy:
		b.WriteString("Connecting review runner...\n\nCtrl+C: quit")
	case m.err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(m.err.Error()))
		b.WriteString("\n\nEnter: continue without runner  r: retry")
	case m.input != nil:
		message := strings.Split(lipgloss.NewStyle().Width(contentWidth).Render(m.input.Message), "\n")
		messageRows := max(1, min(4, height-14))
		if len(message) > messageRows {
			message = message[:messageRows]
			message[messageRows-1] = ansi.Truncate(message[messageRows-1], contentWidth-1, "") + "…"
		}
		b.WriteString(strings.Join(message, "\n") + "\n\n")
		if len(m.input.Choices) == 0 {
			text := ansi.TruncateLeft(m.text, max(0, lipgloss.Width(m.text)-contentWidth+1), "")
			b.WriteString(text + "█\n")
		} else {
			// Keep the current choice visible even with many projects.
			headingRows := lipgloss.Height(lipgloss.NewStyle().Width(contentWidth).Render(b.String()))
			footerRows := lipgloss.Height(lipgloss.NewStyle().Width(contentWidth).Render("Enter: continue  Esc: skip runner"))
			// Reserve border/padding, count, selected path, and footer spacing.
			visible := max(1, min(5, height-headingRows-footerRows-6))
			start := max(0, m.cursor-visible/2)
			end := min(len(m.input.Choices), start+visible)
			for i := start; i < end; i++ {
				prefix := "  "
				if i == m.cursor {
					prefix = "> "
				}
				b.WriteString(ansi.Truncate(prefix+m.input.Choices[i].Label, contentWidth, "…") + "\n")
			}
			b.WriteString(dim.Render(fmt.Sprintf("%d of %d", m.cursor+1, len(m.input.Choices))) + "\n")
			if m.input.Field == "repository_choice" {
				path := m.input.Choices[m.cursor].Value
				if path != "manual" {
					b.WriteString(ansi.TruncateLeft(path, max(0, lipgloss.Width(path)-contentWidth), "") + "\n")
				}
			}
		}
		b.WriteString("\nEnter: continue  Esc: skip runner")
	default:
		b.WriteString("All accessible projects, known checkouts.\n")
		b.WriteString("Fresh Principal Engineer for each review.\n")
		b.WriteString("Stops when this CLI closes.\n\n")
		if m.yes {
			b.WriteString(title.Render("> Run reviews") + "    Not now")
		} else {
			b.WriteString("  Run reviews    " + title.Render("> Not now"))
		}
		motion := "pause"
		if m.owlPaused {
			motion = "play "
		}
		b.WriteString("\n\n" + dim.Render("y/n: choose  Enter: confirm\nSpace: "+motion+" owl  Ctrl+C: quit"))
	}
	body := b.String()
	if consent {
		owl := reviewStartupOwl(m.owlFrame, contentWidth < 65 || height < 18)
		textWidth := contentWidth - lipgloss.Width(owl) - 3
		if textWidth >= 30 {
			body = lipgloss.JoinHorizontal(lipgloss.Center, owl, "   ", lipgloss.NewStyle().Width(textWidth).Render(body))
		} else {
			body = lipgloss.JoinVertical(lipgloss.Center, owl, "", lipgloss.NewStyle().Width(contentWidth).Render(body))
		}
	}
	popup := lipgloss.NewStyle().Width(popupWidth).Border(oceanBorder()).BorderForeground(accentColor).Padding(1, 2).Render(body)
	v := tea.NewView(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup))
	v.AltScreen = true
	return v
}

// Original artwork, rendered with ASCII cells rather than emoji or external assets.
// The fixed canvas keeps the prompt and controls still across every animation frame.
func reviewStartupOwl(frame int, compact bool) string {
	blink := frame%20 == 19
	if compact {
		eyes := "(o,o)"
		if blink {
			eyes = "(-,-)"
		}
		return lipgloss.NewStyle().Foreground(accentColor).Render(" ,_,  \n" + eyes + " \n/)__) \n \" \"  ")
	}
	rows := []string{
		" P        P ",
		" PP      PP ",
		" PLLLLLLLLP ",
		"PPWWWLLWWWPP",
		"PPGKGLLGKGPP",
		"PPWWWGGWWWPP",
		"TPPLLGGLLPPT",
		"TTPPLLLLPPTT",
		"TDTPPPPPPTDT",
		" TTPLLLLPTT ",
		"  PPLLLLPP  ",
		"   GG  GG   ",
	}
	if blink {
		rows[3], rows[4], rows[5] = "PPLLLLLLLLPP", "PPKKKLLKKKPP", "PPLLLGGLLLPP"
	}
	if phase := frame % 16; phase >= 6 && phase < 10 {
		rows[7], rows[8] = "TTPLLLLLLPTT", "TDPLLLLLLPDT"
	}
	pixel := func(color, fallback string) string {
		c := lipgloss.Color(color)
		return lipgloss.NewStyle().Foreground(c).Background(c).Render(fallback)
	}
	// Matching foreground/background colors make solid pixels; the glyphs retain
	// the owl's silhouette and eyes when terminal colors are unavailable.
	palette := map[byte]string{
		' ': "  ",
		'P': pixel("#7554BD", "##"),
		'L': pixel("#B692F6", "++"),
		'W': pixel("#FFF0CB", ".."),
		'G': pixel("#FFC857", "oo"),
		'K': pixel("#252B46", "@@"),
		'T': pixel("#3CE0CF", "//"),
		'D': pixel("#157E89", "\\\\"),
	}
	var b strings.Builder
	for y, row := range rows {
		if y > 0 {
			b.WriteByte('\n')
		}
		shift := 0
		if frame >= 48 && frame < 52 && y < 3 {
			shift = 1
		}
		b.WriteString(strings.Repeat("  ", 1+shift))
		for x := range len(row) {
			b.WriteString(palette[row[x]])
		}
		b.WriteString(strings.Repeat("  ", 1-shift))
	}
	return b.String()
}
