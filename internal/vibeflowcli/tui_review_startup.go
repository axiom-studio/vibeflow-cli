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
		ctx, cancel := context.WithTimeout(m.ctx, 20*time.Second)
		defer cancel()
		o, input, err := resolveReviewStartup(ctx, m.cfg, m.options, m.repositorySelected)
		return reviewStartupResolvedMsg{options: o, input: input, err: err}
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
		project := m.options.Project
		if project == "" && m.cfg != nil {
			project = m.cfg.DefaultProject
		}
		if reviewStartupText(project, 256) {
			b.WriteString(ansi.Truncate("Project: "+project, min(contentWidth, 30), "…") + "\n")
		} else {
			b.WriteString("Uses your project, repository and provider.\n")
		}
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
