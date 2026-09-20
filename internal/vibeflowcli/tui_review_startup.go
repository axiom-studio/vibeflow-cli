package vibeflowcli

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
)

// Session consent deliberately lives only in this model, never in Config.
type reviewStartupModel struct {
	ctx        context.Context
	cfg        *Config
	configPath string
	options    reviewWatchOptions
	input      *reviewStartupInput
	text       string
	cursor     int
	yes        bool
	busy       bool
	done       bool
	quit       bool
	err        error
	runner     *reviewOwnedRunner
	width      int
	height     int
}

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

func (m reviewStartupModel) Init() tea.Cmd { return nil }

func (m reviewStartupModel) resolve() tea.Cmd {
	return func() tea.Msg {
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		o, input, err := resolveReviewStartup(ctx, m.cfg, m.options)
		return reviewStartupResolvedMsg{options: o, input: input, err: err}
	}
}

func (m reviewStartupModel) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	if paste, ok := msg.(tea.PasteMsg); ok {
		msg = tea.KeyPressMsg{Text: paste.Content}
	}
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width, m.height = msg.Width, msg.Height
	case reviewStartupResolvedMsg:
		m.busy, m.err = false, msg.err
		m.options, m.input = msg.options, msg.input
		m.cursor, m.text = 0, ""
		if m.err == nil && m.input == nil {
			m.busy = true
			return m, func() tea.Msg {
				runner, err := startReviewOwned(m.ctx, m.cfg, m.configPath, m.options)
				return reviewStartupReadyMsg{runner: runner, err: err}
			}
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
			switch m.input.Field {
			case "project":
				m.options.Project, m.options.ProjectID = value, 0
				m.options.RepositoryLinkID = 0
			case "provider":
				m.options.Provider, m.options.Model = value, ""
			case "repository":
				m.options.Repository, m.options.RepositoryLinkID = value, 0
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
	contentWidth := max(1, popupWidth-4)
	title := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	dim := lipgloss.NewStyle().Foreground(dimColor)
	var b strings.Builder
	b.WriteString(title.Render("Launch PR review runner for this session?"))
	b.WriteString("\n\n")
	switch {
	case m.busy:
		b.WriteString("Connecting review runner...\n\nCtrl+C: quit")
	case m.err != nil:
		b.WriteString(lipgloss.NewStyle().Foreground(errorColor).Render(m.err.Error()))
		b.WriteString("\n\nEnter: continue without runner  r: retry")
	case m.input != nil:
		b.WriteString(m.input.Message + "\n\n")
		if len(m.input.Choices) == 0 {
			text := ansi.TruncateLeft(m.text, max(0, lipgloss.Width(m.text)-contentWidth+1), "")
			b.WriteString(text + "█\n")
		} else {
			// Keep the current choice visible even with many projects.
			start := max(0, m.cursor-2)
			end := min(len(m.input.Choices), start+5)
			for i := start; i < end; i++ {
				prefix := "  "
				if i == m.cursor {
					prefix = "> "
				}
				b.WriteString(ansi.Truncate(prefix+m.input.Choices[i].Label, contentWidth, "…") + "\n")
			}
			b.WriteString(dim.Render(fmt.Sprintf("%d of %d", m.cursor+1, len(m.input.Choices))) + "\n")
		}
		b.WriteString("\nEnter: continue  Esc: skip runner")
	default:
		b.WriteString("Uses your project, repository and model provider.\n")
		b.WriteString("Each review starts a fresh Principal Engineer.\n")
		b.WriteString("The runner stops when this CLI closes.\n\n")
		if m.yes {
			b.WriteString(title.Render("> Yes") + "    No")
		} else {
			b.WriteString("  Yes    " + title.Render("> No"))
		}
		b.WriteString("\n\n" + dim.Render("y/n: choose  Enter: confirm  Ctrl+C: quit"))
	}
	popup := lipgloss.NewStyle().Width(popupWidth).Border(oceanBorder()).BorderForeground(accentColor).Padding(1, 2).Render(b.String())
	v := tea.NewView(lipgloss.Place(width, height, lipgloss.Center, lipgloss.Center, popup))
	v.AltScreen = true
	return v
}
