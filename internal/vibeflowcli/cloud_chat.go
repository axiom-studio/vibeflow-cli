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
	"sort"
	"strings"
	"time"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
	"github.com/charmbracelet/x/ansi"
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

const (
	cloudChatMaxMessagesPerPersona = 500
	cloudChatMaxMessageRunes       = 8 * 1024
	cloudChatMaxSenderRunes        = 120
	cloudChatPollInterval          = 2 * time.Second
)

type cloudChatBackend interface {
	ListPersonaSessions(projectID int64) (map[string]*Session, error)
	GetSessionMessages(projectID int64, sessionID string) ([]SessionMessage, error)
	SendSessionPrompt(projectID int64, sessionID string, text string) (*SessionMessage, error)
	RespondSessionPrompt(projectID int64, promptID, text string) error
}

type cloudPersonaSessionsMsg struct {
	sessions map[string]*Session
	err      error
}

type cloudSessionMessagesMsg struct {
	sessionID  string
	personaKey string
	messages   []SessionMessage
	err        error
}

type cloudPromptSentMsg struct {
	questionID string
	sessionID  string
	personaKey string
	text       string
	message    *SessionMessage
	err        error
}

type cloudChatPollTickMsg time.Time

// cloudChatPollStartMsg asks the model to arm the poll tick chain. Starting via
// a message rather than calling cloudChatPollCmd directly keeps the "is a chain
// already live?" check inside Update, where the resulting state change actually
// persists — Model.Init has a value receiver and returns only a tea.Cmd, so a
// direct call there could never record that a chain was started.
type cloudChatPollStartMsg struct{}

// CloudChatMessage is one entry in the chat history pane.
type CloudChatMessage struct {
	ReplyPromptID string
	ID            string
	Failed        bool
	Sender        string // "you" or a persona display name
	Text          string
	Timestamp     time.Time
	Pending       bool // true when no backend has acknowledged the send yet
}

// CloudChatModel is the sub-model rendered when Model.activeView == ViewCloudChat.
// Layout mirrors the main TUI: left column (persona list) + right column
// (selected persona's chat view) using the same rounded borders + dimColor
// frame as the sessions view.
type CloudChatModel struct {
	personas []CloudPersona
	cursor   int // selected persona index in personas

	// Per-persona chat history, keyed by persona key. Lazily initialized.
	history           map[string][]CloudChatMessage
	sessionsByPersona map[string]*Session
	sessionsLoaded    bool
	loading           map[string]string
	sending           map[string]bool
	drafts            map[string]string

	focus CloudChatFocus
	input string // current text in the composer
	err   string

	client    cloudChatBackend
	projectID int64

	// polling is true while exactly one poll tick chain is live. active mirrors
	// the parent's activeView == ViewCloudChat and is refreshed by the parent
	// each time it forwards a tick.
	polling bool
	active  bool
}

// NewCloudChatModel constructs an empty cloud chat model. The persona list
// defaults to CloudPersonas; tests may overwrite the field directly.
func NewCloudChatModel() CloudChatModel {
	return CloudChatModel{
		personas:          CloudPersonas,
		cursor:            0,
		history:           make(map[string][]CloudChatMessage),
		loading:           make(map[string]string),
		sending:           make(map[string]bool),
		drafts:            make(map[string]string),
		sessionsByPersona: make(map[string]*Session),
		focus:             CloudFocusSidebar,
	}
}

func NewCloudChatModelWithClient(client cloudChatBackend, projectID int64) CloudChatModel {
	m := NewCloudChatModel()
	m.client = client
	m.projectID = projectID
	return m
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
	switch msg := msg.(type) {
	case tea.PasteMsg:
		if m.focus == CloudFocusInput {
			m.input = sanitizeCloudChatText(m.input+msg.Content, cloudChatMaxMessageRunes)
		}
		return m, nil
	case cloudPersonaSessionsMsg:
		delete(m.loading, "")
		if msg.err != nil {
			m.err = fmt.Sprintf("Cloud sessions unavailable: %v", msg.err)
			return m, nil
		}
		m.err = ""
		for key, old := range m.sessionsByPersona {
			next := msg.sessions[key]
			if next == nil || old.ID != next.ID {
				delete(m.history, key)
				delete(m.loading, key)
			}
		}
		m.sessionsByPersona = msg.sessions
		m.sessionsLoaded = true
		return m, m.loadSelectedMessagesCmd()
	case cloudSessionMessagesMsg:
		if msg.sessionID != "" {
			if m.loading[msg.personaKey] == msg.sessionID {
				delete(m.loading, msg.personaKey)
			}
			session := m.sessionForPersona(msg.personaKey)
			if session == nil || session.ID != msg.sessionID {
				return m, nil
			}
		}
		if msg.err != nil {
			m.err = fmt.Sprintf("Messages unavailable: %v", msg.err)
			return m, nil
		}
		m.err = ""
		m.mergeMessages(msg.personaKey, msg.messages)
		return m, nil
	case cloudPromptSentMsg:
		delete(m.sending, msg.personaKey)
		session := m.sessionForPersona(msg.personaKey)
		stale := msg.sessionID != "" && (session == nil || session.ID != msg.sessionID)
		if msg.err != nil {
			m.err = fmt.Sprintf("Send failed: %v", msg.err)
			for i := range m.history[msg.personaKey] {
				if m.history[msg.personaKey][i].Pending {
					m.history[msg.personaKey][i].Pending = false
					m.history[msg.personaKey][i].Failed = true
				}
			}
			if m.SelectedPersona().Key == msg.personaKey {
				if m.input == "" {
					m.input = msg.text
				}
			} else if m.drafts[msg.personaKey] == "" {
				m.drafts[msg.personaKey] = msg.text
			}
			if stale {
				m.appendMessage(msg.personaKey, CloudChatMessage{Sender: "unsent to previous session", Text: msg.text, Timestamp: time.Now(), Failed: true})
			}
			return m, nil
		}
		if stale {
			return m, nil
		}
		m.err = ""
		for i := range m.history[msg.personaKey] {
			if m.history[msg.personaKey][i].ID == msg.questionID {
				m.history[msg.personaKey][i].ReplyPromptID = ""
			}
		}
		m.replaceLastPendingUserMessage(msg.personaKey, msg.text, msg.message)
		return m, m.loadMessagesCmd(msg.personaKey)
	case cloudChatPollStartMsg:
		// Idempotent: a chain already in flight keeps serving this view, so a
		// second entry must not add one.
		if m.polling || m.client == nil {
			return m, nil
		}
		m.polling = true
		return m, cloudChatPollCmd()
	case cloudChatPollTickMsg:
		// tea.Tick has no cancel handle, so a chain is stopped by simply not
		// re-arming it. Dropping the tick here ends this chain and clears the
		// flag so the next view entry starts exactly one fresh chain.
		if !m.active || m.client == nil {
			m.polling = false
			return m, nil
		}
		m.polling = true
		return m, tea.Batch(
			m.loadMessagesCmd(m.SelectedPersona().Key),
			cloudChatPollCmd(),
		)
	case tea.KeyPressMsg:
		return m.handleKey(msg)
	}
	return m, nil
}

func (m CloudChatModel) handleKey(msg tea.KeyPressMsg) (CloudChatModel, tea.Cmd) {
	if m.focus == CloudFocusInput {
		return m.handleInputKey(msg)
	}
	return m.handleSidebarKey(msg)
}

func (m CloudChatModel) handleSidebarKey(msg tea.KeyPressMsg) (CloudChatModel, tea.Cmd) {
	before := m.SelectedPersona().Key
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
	case "r":
		return m, m.loadPersonaSessionsCmd()
	case "s":
		persona := m.SelectedPersona()
		if m.sessionsLoaded && m.sessionForPersona(persona.Key) == nil {
			m.err = fmt.Sprintf("Start a %s cloud agent in Axiom Cloud, then press r to refresh.", persona.DisplayName)
		}
	}
	if after := m.SelectedPersona().Key; after != "" && after != before {
		m.drafts[before] = m.input
		m.input = m.drafts[after]
		return m, m.loadMessagesCmd(after)
	}
	return m, nil
}

func (m CloudChatModel) handleInputKey(msg tea.KeyPressMsg) (CloudChatModel, tea.Cmd) {
	switch msg.String() {
	case "esc":
		m.focus = CloudFocusSidebar
		return m, nil
	case "enter", "ctrl+r":
		text := strings.TrimSpace(m.input)
		if text == "" {
			return m, nil
		}
		persona := m.SelectedPersona()
		if m.sending[persona.Key] {
			return m, nil
		}
		session := m.sessionForPersona(persona.Key)
		if session == nil {
			m.err = fmt.Sprintf("No active %s session", persona.DisplayName)
			return m, nil
		}
		var question CloudChatMessage
		if msg.String() == "ctrl+r" {
			question = m.oldestQuestion(persona.Key)
			if question.ReplyPromptID == "" {
				m.err = "No unanswered agent question"
				return m, nil
			}
		}
		m.appendMessage(persona.Key, CloudChatMessage{
			Sender:    "you",
			Text:      text,
			Timestamp: time.Now(),
			Pending:   true,
		})
		m.sending[persona.Key] = true
		m.input = ""
		m.err = ""
		return m, m.sendPromptCmd(persona.Key, session.ID, text, question)
	case "backspace":
		if len(m.input) > 0 {
			r := []rune(m.input)
			m.input = string(r[:len(r)-1])
		}
		return m, nil
	default:
		m.input = sanitizeCloudChatText(m.input+msg.Text, cloudChatMaxMessageRunes)
	}
	return m, nil
}

func (m CloudChatModel) loadPersonaSessionsCmd() tea.Cmd {
	if m.projectID == 0 {
		return func() tea.Msg {
			return cloudPersonaSessionsMsg{err: fmt.Errorf("select a project with --project or config default_project")}
		}
	}
	if m.client == nil || m.loading[""] != "" {
		return nil
	}
	m.loading[""] = "sessions"
	return func() tea.Msg {
		sessions, err := m.client.ListPersonaSessions(m.projectID)
		return cloudPersonaSessionsMsg{sessions: sessions, err: err}
	}
}

func (m CloudChatModel) loadSelectedMessagesCmd() tea.Cmd {
	persona := m.SelectedPersona()
	return m.loadMessagesCmd(persona.Key)
}

func (m CloudChatModel) loadMessagesCmd(personaKey string) tea.Cmd {
	session := m.sessionForPersona(personaKey)
	if m.client == nil || session == nil || m.loading[personaKey] == session.ID {
		return nil
	}
	m.loading[personaKey] = session.ID
	return func() tea.Msg {
		messages, err := m.client.GetSessionMessages(m.projectID, session.ID)
		return cloudSessionMessagesMsg{personaKey: personaKey, sessionID: session.ID, messages: messages, err: err}
	}
}

func (m CloudChatModel) sendPromptCmd(personaKey string, sessionID string, text string, question CloudChatMessage) tea.Cmd {
	if m.client == nil {
		return nil
	}
	return func() tea.Msg {
		if question.ReplyPromptID != "" {
			err := m.client.RespondSessionPrompt(m.projectID, question.ReplyPromptID, text)
			message := &SessionMessage{ID: strings.TrimSuffix(question.ID, ":prompt") + ":response", Kind: "user", Text: text, Timestamp: time.Now()}
			return cloudPromptSentMsg{personaKey: personaKey, sessionID: sessionID, questionID: question.ID, text: text, message: message, err: err}
		}
		message, err := m.client.SendSessionPrompt(m.projectID, sessionID, text)
		return cloudPromptSentMsg{personaKey: personaKey, sessionID: sessionID, text: text, message: message, err: err}
	}
}

func (m CloudChatModel) sessionForPersona(personaKey string) *Session {
	if m.sessionsByPersona == nil {
		return nil
	}
	return m.sessionsByPersona[personaKey]
}

// appendMessage records a message under the given persona key.
//
// The sanitize below covers hand-built CloudChatMessage values that never pass
// through sessionMessageToCloudChatMessage — notably the local pending "you"
// echo in handleInputKey, whose Text is the raw input buffer and can carry
// pasted escape sequences. Server-derived messages are already sanitized by
// sessionMessageToCloudChatMessage; re-running it here is a no-op because
// sanitizeCloudChatText strips every control rune (including bare ESC) and
// truncateRunes is idempotent.
func (m *CloudChatModel) appendMessage(personaKey string, msg CloudChatMessage) {
	if m.history == nil {
		m.history = make(map[string][]CloudChatMessage)
	}
	msg.Sender = sanitizeCloudChatText(msg.Sender, cloudChatMaxSenderRunes)
	msg.Text = sanitizeCloudChatText(msg.Text, cloudChatMaxMessageRunes)

	history := append(m.history[personaKey], msg)
	if len(history) > cloudChatMaxMessagesPerPersona {
		history = history[len(history)-cloudChatMaxMessagesPerPersona:]
	}
	m.history[personaKey] = history
}

func (m *CloudChatModel) mergeMessages(personaKey string, messages []SessionMessage) {
	persona := cloudPersonaByKey(m.personas, personaKey)
	for _, message := range messages {
		converted := sessionMessageToCloudChatMessage(message, persona.DisplayName)
		found := false
		for i, old := range m.history[personaKey] {
			if converted.ID != "" && old.ID == converted.ID {
				// A stale poll must not reopen a question acknowledged by a newer reply.
				if old.ReplyPromptID == "" {
					converted.ReplyPromptID = ""
				}
				m.history[personaKey][i] = converted
				found = true
				break
			}
		}
		if !found && !m.hasMessage(personaKey, converted) {
			m.appendMessage(personaKey, converted)
		}
	}
	sort.SliceStable(m.history[personaKey], func(i, j int) bool {
		return m.history[personaKey][i].Timestamp.Before(m.history[personaKey][j].Timestamp)
	})
}

func (m CloudChatModel) hasMessage(personaKey string, msg CloudChatMessage) bool {
	for _, existing := range m.history[personaKey] {
		if existing.Sender == msg.Sender &&
			existing.Text == msg.Text &&
			existing.Pending == msg.Pending &&
			existing.Timestamp.Equal(msg.Timestamp) {
			return true
		}
	}
	return false
}

func (m *CloudChatModel) replaceLastPendingUserMessage(personaKey string, text string, message *SessionMessage) {
	history := m.history[personaKey]
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Sender == "you" && history[i].Pending && history[i].Text == sanitizeCloudChatText(text, cloudChatMaxMessageRunes) {
			if message != nil {
				if message.ID != "" {
					for j, existing := range history {
						if j != i && existing.ID == message.ID {
							m.history[personaKey] = append(history[:i], history[i+1:]...)
							m.mergeMessages(personaKey, []SessionMessage{*message})
							return
						}
					}
				}
				persona := cloudPersonaByKey(m.personas, personaKey)
				history[i] = sessionMessageToCloudChatMessage(*message, persona.DisplayName)
			} else {
				history[i].Pending = false
			}
			m.history[personaKey] = history
			return
		}
	}
	if message != nil {
		persona := cloudPersonaByKey(m.personas, personaKey)
		m.appendMessage(personaKey, sessionMessageToCloudChatMessage(*message, persona.DisplayName))
	}
}

// sessionMessageToCloudChatMessage converts a backend message into a render
// history entry. Sanitizing here (rather than only in appendMessage) is what
// keeps server-controlled text out of the terminal on every construction site,
// including replaceLastPendingUserMessage's direct slice assignment which does
// not go through appendMessage.
func sessionMessageToCloudChatMessage(msg SessionMessage, personaDisplayName string) CloudChatMessage {
	sender := strings.TrimSpace(msg.Sender)
	if sender == "" {
		if msg.Kind == "user" {
			sender = "you"
		} else if personaDisplayName != "" {
			sender = personaDisplayName
		} else {
			sender = "agent"
		}
	}
	timestamp := msg.Timestamp
	return CloudChatMessage{
		ID:            msg.ID,
		ReplyPromptID: msg.ReplyPromptID,
		Sender:        sanitizeCloudChatText(sender, cloudChatMaxSenderRunes),
		Text:          sanitizeCloudChatText(msg.Text, cloudChatMaxMessageRunes),
		Timestamp:     timestamp,
		Pending:       msg.Pending,
	}
}

func (m CloudChatModel) oldestQuestion(personaKey string) CloudChatMessage {
	for _, message := range m.history[personaKey] {
		if message.ReplyPromptID != "" {
			return message
		}
	}
	return CloudChatMessage{}
}

func cloudPersonaByKey(personas []CloudPersona, key string) CloudPersona {
	for _, persona := range personas {
		if persona.Key == key {
			return persona
		}
	}
	return CloudPersona{}
}

// Messages returns the message history for the given persona (read-only view
// used by tests; the renderer accesses m.history directly).
func (m CloudChatModel) Messages(personaKey string) []CloudChatMessage {
	return m.history[personaKey]
}

func sanitizeCloudChatText(s string, maxRunes int) string {
	s = stripANSI(s)
	s = strings.Map(func(r rune) rune {
		if r == '\n' || r == '\t' {
			return r
		}
		if unicode.IsControl(r) {
			return -1
		}
		return r
	}, s)
	return truncateRunes(s, maxRunes)
}

func truncateRunes(s string, maxRunes int) string {
	if maxRunes <= 0 {
		return ""
	}
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + "…"
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
		Width(leftWidth-2).
		Height(colHeight-2).
		Border(borderStyle).
		BorderForeground(dimColor).
		Padding(0, 1)
	rightStyle := lipgloss.NewStyle().
		Width(rightWidth-2).
		Height(colHeight-2).
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
	header := lipgloss.NewStyle().Bold(true).Foreground(PersonaColor(p.Key)).Render(p.DisplayName)
	parts := []string{header}
	if question := m.oldestQuestion(p.Key); question.ReplyPromptID != "" {
		preview := strings.NewReplacer("\n", " ", "\t", " ").Replace(question.Text)
		parts = append(parts, helpStyle.Render(ansi.Truncate("Ctrl+R replies to: "+preview, width, "…")))
	}
	if height >= 20 {
		parts = append(parts, "", lipgloss.NewStyle().Foreground(PersonaColor(p.Key)).Render(PersonaLargeIcon(p.Key)), "")
	}
	inputBar := m.renderInputBar(width)
	historyHeight := height - lipgloss.Height(strings.Join(parts, "\n")) - lipgloss.Height(inputBar)
	if m.err != "" {
		warning := ansi.Truncate(strings.ReplaceAll(sanitizeCloudChatText(m.err, cloudChatMaxMessageRunes), "\n", " "), width, "…")
		parts = append(parts, lipgloss.NewStyle().Foreground(warningColor).Render(warning))
		historyHeight--
	}
	parts = append(parts, trimToHeight(m.renderHistory(p.Key, width), max(1, historyHeight)), inputBar)
	return lipgloss.JoinVertical(lipgloss.Left, parts...)
}

func (m CloudChatModel) renderHistory(personaKey string, width int) string {
	persona := cloudPersonaByKey(m.personas, personaKey)
	if m.sessionsLoaded && m.sessionForPersona(personaKey) == nil {
		lines := []string{
			fmt.Sprintf("No active %s session.", persona.DisplayName),
			"Press s for start guidance, or r to refresh.",
		}
		return helpStyle.Render(strings.Join(lines, "\n"))
	}
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
		if msg.Pending {
			line += " (sending)"
		}
		if msg.Failed {
			line += " (failed)"
		}
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
		input := strings.NewReplacer("\n", " ", "\t", " ").Replace(sanitizeCloudChatText(m.input, cloudChatMaxMessageRunes))
		input = ansi.Cut(input, max(0, lipgloss.Width(input)-(width-3)), lipgloss.Width(input))
		return style.Render(prompt + input + cursor)
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
		if m.oldestQuestion(m.SelectedPersona().Key).ReplyPromptID != "" {
			return "esc: back  enter: send new  ctrl+r: reply  ctrl+c: quit"
		}
		return "esc: back  enter: send  ctrl+c: quit"
	}
	return "↑/↓: persona  enter: compose  r: refresh  s: start  esc: back  q: quit"
}

// cloudChatPollStartCmd asks the cloud chat model to arm the poll tick chain if
// one is not already running. Safe to call on every entry into ViewCloudChat.
func cloudChatPollStartCmd() tea.Cmd {
	return func() tea.Msg { return cloudChatPollStartMsg{} }
}

func cloudChatPollCmd() tea.Cmd {
	return tea.Tick(cloudChatPollInterval, func(t time.Time) tea.Msg {
		return cloudChatPollTickMsg(t)
	})
}
