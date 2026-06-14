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
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

func TestCloudPersonas_CompleteAndOrdered(t *testing.T) {
	// Sidebar order is part of the UX contract — assert it explicitly so
	// future re-orderings are deliberate.
	wantOrder := []string{
		"principal_engineer",
		"architect",
		"developer",
		"ux_designer",
		"qa_lead",
		"security_lead",
		"product_manager",
		"project_manager",
		"customer",
	}
	if len(CloudPersonas) != len(wantOrder) {
		t.Fatalf("CloudPersonas length = %d, want %d", len(CloudPersonas), len(wantOrder))
	}
	for i, key := range wantOrder {
		if CloudPersonas[i].Key != key {
			t.Errorf("CloudPersonas[%d].Key = %q, want %q", i, CloudPersonas[i].Key, key)
		}
		if CloudPersonas[i].DisplayName == "" {
			t.Errorf("CloudPersonas[%d].DisplayName is empty for key %q", i, key)
		}
		if PersonaCompactIcon(key) == "" {
			t.Errorf("persona %q has no compact icon", key)
		}
		if PersonaLargeIcon(key) == "" {
			t.Errorf("persona %q has no large icon", key)
		}
	}
}

func TestCloudChatModel_NewIsAtFirstPersonaWithSidebarFocus(t *testing.T) {
	m := NewCloudChatModel()
	if got := m.SelectedPersona().Key; got != "principal_engineer" {
		t.Errorf("starting persona = %q, want principal_engineer", got)
	}
	if m.focus != CloudFocusSidebar {
		t.Errorf("starting focus = %v, want CloudFocusSidebar", m.focus)
	}
	if len(m.Messages("principal_engineer")) != 0 {
		t.Errorf("expected empty history for fresh model")
	}
}

func TestCloudChatModel_NavigationDownWraps(t *testing.T) {
	m := NewCloudChatModel()
	last := len(CloudPersonas) - 1
	for i := 0; i < last; i++ {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	}
	if m.cursor != last {
		t.Fatalf("after %d ↓ presses cursor = %d, want %d", last, m.cursor, last)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyDown})
	if m.cursor != 0 {
		t.Errorf("↓ at last persona did not wrap to 0 (got %d)", m.cursor)
	}
}

func TestCloudChatModel_NavigationUpWraps(t *testing.T) {
	m := NewCloudChatModel()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyUp})
	want := len(CloudPersonas) - 1
	if m.cursor != want {
		t.Errorf("↑ at first persona did not wrap to %d (got %d)", want, m.cursor)
	}
}

func TestCloudChatModel_EnterFocusesInputAndEscReturns(t *testing.T) {
	m := NewCloudChatModel()
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if m.focus != CloudFocusInput {
		t.Fatalf("Enter from sidebar should focus input (got focus=%v)", m.focus)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if m.focus != CloudFocusSidebar {
		t.Errorf("Esc from input should return to sidebar (got focus=%v)", m.focus)
	}
}

func TestCloudChatModel_TypingAppendsToInputBuffer(t *testing.T) {
	m := focusInput(NewCloudChatModel())
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	if m.input != "hello" {
		t.Errorf("input = %q, want \"hello\"", m.input)
	}
}

func TestCloudChatModel_BackspaceTrimsLastRune(t *testing.T) {
	m := focusInput(NewCloudChatModel())
	for _, r := range "ab" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyBackspace})
	if m.input != "a" {
		t.Errorf("after backspace input = %q, want %q", m.input, "a")
	}
}

func TestCloudChatModel_EnterWithActiveSessionAppendsPendingUserMessage(t *testing.T) {
	m := focusInput(NewCloudChatModelWithClient(&fakeCloudChatBackend{}, 13))
	m.sessionsByPersona = map[string]*Session{
		"principal_engineer": {ID: "session-pe", PersonaKey: "principal_engineer"},
	}
	for _, r := range "hi" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}
	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatalf("expected backend send command")
	}

	msgs := m.Messages("principal_engineer")
	if len(msgs) != 1 {
		t.Fatalf("expected 1 pending user message, got %d", len(msgs))
	}
	if msgs[0].Sender != "you" || msgs[0].Text != "hi" {
		t.Errorf("user message wrong: sender=%q text=%q", msgs[0].Sender, msgs[0].Text)
	}
	if !msgs[0].Pending {
		t.Errorf("user message should be marked Pending until backend acknowledges it")
	}
	if m.input != "" {
		t.Errorf("input not cleared after send: %q", m.input)
	}
}

func TestCloudChatModel_EnterOnEmptyInputDoesNotSend(t *testing.T) {
	m := focusInput(NewCloudChatModel())
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if len(m.Messages("principal_engineer")) != 0 {
		t.Errorf("blank input should not produce a message")
	}
}

func TestCloudChatModel_HistoryIsPerPersona(t *testing.T) {
	m := NewCloudChatModel()
	m.appendMessage("principal_engineer", CloudChatMessage{Sender: "you", Text: "first"})
	m.appendMessage("principal_engineer", CloudChatMessage{Sender: "Principal Eng", Text: "reply"})
	m.appendMessage("developer", CloudChatMessage{Sender: "you", Text: "second"})
	m.appendMessage("developer", CloudChatMessage{Sender: "Developer", Text: "reply"})

	if got := len(m.Messages("principal_engineer")); got != 2 {
		t.Errorf("principal_engineer history len = %d, want 2", got)
	}
	if got := len(m.Messages("developer")); got != 2 {
		t.Errorf("developer history len = %d, want 2", got)
	}
	if m.Messages("principal_engineer")[0].Text != "first" {
		t.Errorf("principal_engineer first message = %q", m.Messages("principal_engineer")[0].Text)
	}
	if m.Messages("developer")[0].Text != "second" {
		t.Errorf("developer first message = %q", m.Messages("developer")[0].Text)
	}
}

func TestCloudChatModel_LoadPersonaSessionsFetchesSelectedHistory(t *testing.T) {
	fake := &fakeCloudChatBackend{
		sessions: map[string]*Session{
			"principal_engineer": {ID: "session-pe", PersonaKey: "principal_engineer"},
		},
		messages: []SessionMessage{
			{Sender: "Principal Eng", Text: "ready", Kind: "agent"},
		},
	}
	m := NewCloudChatModelWithClient(fake, 13)

	cmd := m.loadPersonaSessionsCmd()
	if cmd == nil {
		t.Fatal("expected loadPersonaSessionsCmd to return a command")
	}
	var nextCmd tea.Cmd
	m, nextCmd = m.Update(cmd())
	if fake.listProjectID != 13 {
		t.Errorf("ListPersonaSessions projectID = %d, want 13", fake.listProjectID)
	}
	if nextCmd == nil {
		t.Fatal("expected persona session load to trigger selected history fetch")
	}
	m, _ = m.Update(nextCmd())

	if fake.messagesSessionID != "session-pe" {
		t.Errorf("GetSessionMessages sessionID = %q, want session-pe", fake.messagesSessionID)
	}
	msgs := m.Messages("principal_engineer")
	if len(msgs) != 1 {
		t.Fatalf("history len = %d, want 1", len(msgs))
	}
	if msgs[0].Text != "ready" {
		t.Errorf("message text = %q, want ready", msgs[0].Text)
	}
}

func TestCloudChatModel_SendPromptUsesBackendSession(t *testing.T) {
	fake := &fakeCloudChatBackend{
		sentMessage: &SessionMessage{Sender: "you", Text: "hello", Kind: "user"},
	}
	m := NewCloudChatModelWithClient(fake, 13)
	m.sessionsByPersona = map[string]*Session{
		"principal_engineer": {ID: "session-pe", PersonaKey: "principal_engineer"},
	}
	m = focusInput(m)
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("expected send command")
	}
	if fake.sentText != "" {
		t.Fatalf("backend should not be called until command executes")
	}
	if msgs := m.Messages("principal_engineer"); len(msgs) != 1 || !msgs[0].Pending {
		t.Fatalf("expected one pending local message, got %#v", msgs)
	}
	m, _ = m.Update(cmd())

	if fake.sentSessionID != "session-pe" {
		t.Errorf("SendSessionPrompt sessionID = %q, want session-pe", fake.sentSessionID)
	}
	if fake.sentText != "hello" {
		t.Errorf("SendSessionPrompt text = %q, want hello", fake.sentText)
	}
	msgs := m.Messages("principal_engineer")
	if len(msgs) != 1 {
		t.Fatalf("history len = %d, want 1", len(msgs))
	}
	if msgs[0].Pending {
		t.Errorf("sent message should no longer be pending")
	}
}

func TestCloudChatModel_SendPromptWithoutActiveSessionShowsError(t *testing.T) {
	m := focusInput(NewCloudChatModel())
	for _, r := range "hello" {
		m, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{r}})
	}

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd != nil {
		t.Fatal("expected no command without an active session")
	}
	if m.err == "" || !strings.Contains(m.err, "No active") {
		t.Fatalf("expected no-active-session error, got %q", m.err)
	}
	if m.input != "hello" {
		t.Errorf("input should be preserved on send failure, got %q", m.input)
	}
}

func TestCloudChatModel_NoActiveSessionStateRendersAfterSessionLoad(t *testing.T) {
	m := NewCloudChatModel()
	m.sessionsLoaded = true

	out := m.renderHistory("principal_engineer", 80)
	if !strings.Contains(out, "No active Principal Eng session") {
		t.Fatalf("no-active state missing persona-specific text: %q", out)
	}
	if !strings.Contains(out, "r to refresh") {
		t.Errorf("no-active state missing refresh hint: %q", out)
	}
}

func TestCloudChatModel_NoActiveSessionStartHintSetsGuidance(t *testing.T) {
	m := NewCloudChatModelWithClient(&fakeCloudChatBackend{}, 13)
	m.sessionsLoaded = true

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	if cmd != nil {
		t.Fatal("start hint should not spawn a backend command")
	}
	if !strings.Contains(m.err, "Start a Principal Eng cloud agent") {
		t.Fatalf("start guidance error = %q", m.err)
	}
}

func TestCloudChatModel_RefreshKeyReloadsPersonaSessions(t *testing.T) {
	fake := &fakeCloudChatBackend{}
	m := NewCloudChatModelWithClient(fake, 13)

	var cmd tea.Cmd
	m, cmd = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})
	if cmd == nil {
		t.Fatal("refresh key should return load sessions command")
	}
	m, _ = m.Update(cmd())
	if fake.listProjectID != 13 {
		t.Errorf("refresh ListPersonaSessions projectID = %d, want 13", fake.listProjectID)
	}
}

func TestCloudChatModel_AppendMessageSanitizesANSIAndControlChars(t *testing.T) {
	m := NewCloudChatModel()
	m.appendMessage("architect", CloudChatMessage{
		Sender: "Architect\x1b[31m\x00",
		Text:   "hello\x1b[2J\x1b[Hworld\x07",
	})

	msgs := m.Messages("architect")
	if len(msgs) != 1 {
		t.Fatalf("history len = %d, want 1", len(msgs))
	}
	if strings.Contains(msgs[0].Text, "\x1b") || strings.Contains(msgs[0].Text, "\x07") {
		t.Fatalf("message text still contains control sequences: %q", msgs[0].Text)
	}
	if got, want := msgs[0].Text, "helloworld"; got != want {
		t.Errorf("sanitized text = %q, want %q", got, want)
	}
	if strings.Contains(msgs[0].Sender, "\x1b") || strings.Contains(msgs[0].Sender, "\x00") {
		t.Errorf("sender still contains control sequences: %q", msgs[0].Sender)
	}
}

func TestCloudChatModel_AppendMessageTruncatesLongText(t *testing.T) {
	m := NewCloudChatModel()
	m.appendMessage("architect", CloudChatMessage{
		Sender: "Architect",
		Text:   strings.Repeat("x", cloudChatMaxMessageRunes+1),
	})

	msg := m.Messages("architect")[0]
	if got := []rune(msg.Text); len(got) != cloudChatMaxMessageRunes+1 {
		t.Fatalf("truncated rune len = %d, want %d", len(got), cloudChatMaxMessageRunes+1)
	}
	if !strings.HasSuffix(msg.Text, "…") {
		t.Errorf("truncated text missing ellipsis suffix")
	}
}

func TestCloudChatModel_AppendMessageCapsHistoryPerPersona(t *testing.T) {
	m := NewCloudChatModel()
	for i := 0; i < cloudChatMaxMessagesPerPersona+5; i++ {
		m.appendMessage("architect", CloudChatMessage{
			Sender: "Architect",
			Text:   fmt.Sprintf("msg-%03d", i),
		})
	}

	msgs := m.Messages("architect")
	if len(msgs) != cloudChatMaxMessagesPerPersona {
		t.Fatalf("history len = %d, want %d", len(msgs), cloudChatMaxMessagesPerPersona)
	}
	if got, want := msgs[0].Text, "msg-005"; got != want {
		t.Errorf("oldest retained message = %q, want %q", got, want)
	}
	if got, want := msgs[len(msgs)-1].Text, "msg-504"; got != want {
		t.Errorf("newest retained message = %q, want %q", got, want)
	}
}

func TestCloudChatModel_ViewMatchesShellFrame(t *testing.T) {
	m := NewCloudChatModel()
	out := m.View(100, 30)
	if !strings.Contains(out, "Personas") {
		t.Errorf("View missing 'Personas' header; output:\n%s", out)
	}
	if !strings.Contains(out, "Principal Eng") {
		t.Errorf("View missing selected persona display name; output:\n%s", out)
	}
	// Selection idiom: "> " prefix on the selected sidebar row (matches the
	// sessions view's selectedStyle.Render("> " + ...) convention).
	if !strings.Contains(out, "> ") {
		t.Errorf("View missing '> ' selection prefix; output:\n%s", out)
	}
}

func TestCloudChatModel_HelpKeysSwitchByFocus(t *testing.T) {
	m := NewCloudChatModel()
	sidebarHelp := m.CloudChatHelpKeys()
	if !strings.Contains(sidebarHelp, "persona") {
		t.Errorf("sidebar help keys missing 'persona'; got: %q", sidebarHelp)
	}
	m, _ = m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	inputHelp := m.CloudChatHelpKeys()
	if !strings.Contains(inputHelp, "send") {
		t.Errorf("input-focus help keys missing 'send'; got: %q", inputHelp)
	}
}

func TestTrimToHeight(t *testing.T) {
	cases := []struct {
		name   string
		in     string
		height int
		want   string
	}{
		{"under cap returns input", "a\nb", 5, "a\nb"},
		{"exactly cap returns input", "a\nb\nc", 3, "a\nb\nc"},
		{"over cap drops leading lines", "a\nb\nc\nd", 2, "c\nd"},
		{"zero height returns empty", "a\nb", 0, ""},
		{"negative height returns empty", "a\nb", -1, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := trimToHeight(tc.in, tc.height); got != tc.want {
				t.Errorf("trimToHeight(%q, %d) = %q, want %q", tc.in, tc.height, got, tc.want)
			}
		})
	}
}

// --- helpers ---

func focusInput(m CloudChatModel) CloudChatModel {
	next, _ := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	return next
}

type fakeCloudChatBackend struct {
	sessions    map[string]*Session
	messages    []SessionMessage
	sentMessage *SessionMessage

	listProjectID     int64
	messagesSessionID string
	sentSessionID     string
	sentText          string
}

func (f *fakeCloudChatBackend) ListPersonaSessions(projectID int64) (map[string]*Session, error) {
	f.listProjectID = projectID
	return f.sessions, nil
}

func (f *fakeCloudChatBackend) GetSessionMessages(sessionID string, sinceISO string) ([]SessionMessage, error) {
	f.messagesSessionID = sessionID
	return f.messages, nil
}

func (f *fakeCloudChatBackend) SendSessionPrompt(sessionID string, text string) (*SessionMessage, error) {
	f.sentSessionID = sessionID
	f.sentText = text
	return f.sentMessage, nil
}
