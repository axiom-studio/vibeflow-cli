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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// CloudPersona names the persona keys + display labels shown in the cloud chat
// sidebar. Order is intentional (engineering-first → managerial → customer);
// the renderer iterates this slice so layout is deterministic across runs.
type CloudPersona struct {
	Key         string
	DisplayName string
}

// CloudPersonas is the canonical persona list rendered in the sidebar.
// Keys match those in persona_icons.go and the server-side persona registry.
var CloudPersonas = []CloudPersona{
	{Key: "principal_engineer", DisplayName: "Principal Eng"},
	{Key: "architect", DisplayName: "Architect"},
	{Key: "developer", DisplayName: "Developer"},
	{Key: "ux_designer", DisplayName: "UX Designer"},
	{Key: "qa_lead", DisplayName: "QA Lead"},
	{Key: "security_lead", DisplayName: "Security Lead"},
	{Key: "product_manager", DisplayName: "Product Mgr"},
	{Key: "project_manager", DisplayName: "Project Mgr"},
	{Key: "customer", DisplayName: "Customer"},
}

// CloudChatFocus controls which pane receives keyboard input.
type CloudChatFocus int

const (
	CloudFocusSidebar CloudChatFocus = iota
	CloudFocusInput
)

// CloudChatMessage is one entry in the chat history pane.
type CloudChatMessage struct {
	Sender    string    // "you" or a persona display name
	Text      string
	Timestamp time.Time
	Pending   bool // true when no backend has acknowledged the send yet
}

// CloudChatModel is the sub-model rendered when Model.activeView == ViewCloudChat.
// Layout mirrors the main TUI: left column (persona list) + right column
// (selected persona's chat view) using the same rounded borders + dimColor
// frame as the sessions view.
type CloudChatModel struct {
	personas []CloudPersona
	cursor   int // selected persona index in personas

	// Per-persona chat history, keyed by persona key. Lazily initialized.
	history map[string][]CloudChatMessage

	focus CloudChatFocus
	input string // current text in the composer
}

// NewCloudChatModel constructs an empty cloud chat model. The persona list
// defaults to CloudPersonas; tests may overwrite the field directly.
func NewCloudChatModel() CloudChatModel {
	return CloudChatModel{
		personas: CloudPersonas,
		cursor:   0,
		history:  make(map[string][]CloudChatMessage),
		focus:    CloudFocusSidebar,
	}
}

// SelectedPersona returns the persona currently highlighted in the sidebar.
func (m CloudChatModel) SelectedPersona() CloudPersona {
	if m.cursor < 0 || m.cursor >= len(m.personas) {
		return CloudPersona{}
	}
	return m.personas[m.cursor]
}

// Update handles keyboard input. tea.WindowSizeMsg is intentionally not handled
// here — the parent Model owns width/height and passes them to View().
func (m CloudChatModel) Update(msg tea.Msg) (CloudChatModel, tea.Cmd) {
	if keyMsg, ok := msg.(tea.KeyMsg); ok {
		return m.handleKey(keyMsg)
	}
	return m, nil
}

func (m CloudChatModel) handleKey(msg tea.KeyMsg) (CloudChatModel, tea.Cmd) {
	if m.focus == CloudFocusInput {
		return m.handleInputKey(msg)
	}
	return m.handleSidebarKey(msg)
}

func (m CloudChatModel) handleSidebarKey(msg tea.KeyMsg) (CloudChatModel, tea.Cmd) {
	switch msg.String() {
	case "up", "k":
		if m.cursor == 0 {
			m.cursor = len(m.personas) - 1
		} else {
			m.cursor--
		}
	case "down", "j":
		m.cursor = (m.cursor + 1) % len(m.personas)
	case "enter", "i":
		m.focus = CloudFocusInput
	}
	return m, nil
}

func (m CloudChatModel) handleInputKey(msg tea.KeyMsg) (CloudChatModel, tea.Cmd) {
	switch msg.Type {
	case tea.KeyEsc:
		m.focus = CloudFocusSidebar
		return m, nil
	case tea.KeyEnter:
		text := strings.TrimSpace(m.input)
		if text == "" {
			return m, nil
		}
		persona := m.SelectedPersona()
		m.appendMessage(persona.Key, CloudChatMessage{
			Sender:    "you",
			Text:      text,
			Timestamp: time.Now(),
		})
		m.appendMessage(persona.Key, CloudChatMessage{
			Sender:    persona.DisplayName,
			Text:      "(backend not yet wired — message held locally; see feature 412 follow-ups)",
			Timestamp: time.Now(),
			Pending:   true,
		})
		m.input = ""
		return m, nil
	case tea.KeyBackspace:
		if len(m.input) > 0 {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
		}
		return m, nil
	case tea.KeyRunes:
		m.input += string(msg.Runes)
		return m, nil
	case tea.KeySpace:
		m.input += " "
		return m, nil
	}
	return m, nil
}

// appendMessage records a message under the given persona key.
func (m *CloudChatModel) appendMessage(personaKey string, msg CloudChatMessage) {
	if m.history == nil {
		m.history = make(map[string][]CloudChatMessage)
	}
	m.history[personaKey] = append(m.history[personaKey], msg)
}

// Messages returns the message history for the given persona (read-only view
// used by tests; the renderer accesses m.history directly).
func (m CloudChatModel) Messages(personaKey string) []CloudChatMessage {
	return m.history[personaKey]
}

// --- rendering ---
//
// View renders the cloud chat sub-view inside the same column frame the main
// TUI uses for ViewSessions. The parent Model passes the same `width` and
// `colHeight` it computes for the sessions view so vertical proportions match.

func (m CloudChatModel) View(width, colHeight int) string {
	if width < 40 {
		width = 80
	}
	if colHeight < 6 {
		colHeight = 12
	}

	leftWidth := width * 35 / 100
	rightWidth := width - leftWidth
	if leftWidth < 20 {
		leftWidth = 20
	}
	if rightWidth < 20 {
		rightWidth = 20
	}

	leftContentW := leftWidth - 4
	rightContentW := rightWidth - 4
	contentH := colHeight - 2
	if leftContentW < 10 {
		leftContentW = 10
	}
	if rightContentW < 10 {
		rightContentW = 10
	}
	if contentH < 4 {
		contentH = 4
	}

	leftContent := m.renderPersonaList(leftContentW)
	rightContent := m.renderChatPane(rightContentW, contentH)

	borderStyle := lipgloss.RoundedBorder()
	leftStyle := lipgloss.NewStyle().
		Width(leftWidth).
		Height(colHeight).
		Border(borderStyle).
		BorderForeground(dimColor).
		Padding(0, 1)
	rightStyle := lipgloss.NewStyle().
		Width(rightWidth).
		Height(colHeight).
		Border(borderStyle).
		BorderForeground(dimColor).
		Padding(0, 1)

	return lipgloss.JoinHorizontal(lipgloss.Top,
		leftStyle.Render(leftContent),
		rightStyle.Render(rightContent),
	)
}

// renderPersonaList renders the left column with one row per persona.
// The selected row uses the same selectedStyle + "> " prefix as the sessions
// view so the focus indicator looks identical across views.
func (m CloudChatModel) renderPersonaList(width int) string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	b.WriteString(headerStyle.Render("Personas"))
	b.WriteString("\n")

	for i, p := range m.personas {
		icon := PersonaCompactIcon(p.Key)
		// Pad icon to a fixed display width so multi-glyph icons (e.g. ⟨⟩)
		// don't shift the column — same lesson learned in issue #1982.
		paddedIcon := lipgloss.NewStyle().Width(2).Render(icon)
		nameStyle := lipgloss.NewStyle().Foreground(PersonaColor(p.Key))
		line := fmt.Sprintf("%s %s", paddedIcon, nameStyle.Render(p.DisplayName))

		if i == m.cursor {
			b.WriteString(selectedStyle.Width(width).Render("> " + line))
		} else {
			b.WriteString("  " + line)
		}
		b.WriteString("\n")
	}

	return strings.TrimRight(b.String(), "\n")
}

// renderChatPane renders the right column: persona header, large icon, chat
// history, and the input bar.
func (m CloudChatModel) renderChatPane(width, height int) string {
	p := m.SelectedPersona()

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(PersonaColor(p.Key))
	header := headerStyle.Render(p.DisplayName)

	iconStyle := lipgloss.NewStyle().Foreground(PersonaColor(p.Key))
	iconBlock := iconStyle.Render(PersonaLargeIcon(p.Key))

	history := m.renderHistory(p.Key, width)
	inputBar := m.renderInputBar(width)

	// Reserve lines: header (1) + blank (1) + icon (5) + blank (1) + inputBar (2 incl. border).
	historyHeight := height - (1 + 1 + 5 + 1 + 2)
	if historyHeight < 2 {
		historyHeight = 2
	}
	historyTrimmed := trimToHeight(history, historyHeight)

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		"",
		iconBlock,
		"",
		historyTrimmed,
		inputBar,
	)
}

func (m CloudChatModel) renderHistory(personaKey string, width int) string {
	msgs := m.history[personaKey]
	if len(msgs) == 0 {
		return helpStyle.Render("No messages yet. Press Enter to compose.")
	}
	var b strings.Builder
	for _, msg := range msgs {
		senderStyle := lipgloss.NewStyle().Bold(true)
		if msg.Pending {
			senderStyle = senderStyle.Foreground(dimColor)
		}
		line := senderStyle.Render(msg.Sender+":") + " " + msg.Text
		// Soft-wrap by lipgloss width — defensive against very wide messages.
		b.WriteString(lipgloss.NewStyle().Width(width).Render(line))
		b.WriteString("\n")
	}
	return strings.TrimRight(b.String(), "\n")
}

func (m CloudChatModel) renderInputBar(width int) string {
	const prompt = "> "
	style := lipgloss.NewStyle().
		Border(lipgloss.NormalBorder(), true, false, false, false).
		BorderForeground(dimColor).
		Width(width)
	if m.focus == CloudFocusInput {
		cursor := lipgloss.NewStyle().Reverse(true).Render(" ")
		return style.Render(prompt + m.input + cursor)
	}
	hint := helpStyle.Render("(press Enter to compose)")
	return style.Render(prompt + hint)
}

// trimToHeight drops leading lines if the input has more than `height` lines
// — the chat history scrolls so the newest entries stay visible.
func trimToHeight(s string, height int) string {
	if height <= 0 {
		return ""
	}
	lines := strings.Split(s, "\n")
	if len(lines) <= height {
		return s
	}
	return strings.Join(lines[len(lines)-height:], "\n")
}

// CloudChatHelpKeys returns the context-sensitive key hint string for the
// help bar at the bottom of the main TUI.
func (m CloudChatModel) CloudChatHelpKeys() string {
	if m.focus == CloudFocusInput {
		return "esc: back  enter: send  ctrl+c: quit"
	}
	return "↑/↓: persona  enter: compose  esc: back to sessions  q: quit"
}
