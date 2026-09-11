package vibeflowcli

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/lipgloss"
)

func TestCloudChatRESTFlow(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing authentication")
		}
		switch r.URL.Path {
		case "/rest/v1/vibeflow/sessions/active":
			fmt.Fprint(w, `{"sessions":[{"session_id":"session-pe","project_id":13,"persona_key":"principal_engineer","active":true}]}`)
		case "/rest/v1/vibeflow/projects/13/prompts":
			if r.Method == http.MethodPost {
				w.WriteHeader(http.StatusCreated)
				fmt.Fprint(w, `{"id":1,"prompt_text":"hello","source":"user","created_at":"2026-09-11T12:00:00Z"}`)
			} else {
				fmt.Fprint(w, `{"prompts":[{"id":1,"prompt_text":"hello","response_text":"hello back","source":"user","created_at":"2026-09-11T12:00:00Z","responded_at":"2026-09-11T12:01:00Z"}],"page":{"has_more":false}}`)
			}
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := NewCloudChatModelWithClient(NewClient(srv.URL, "test-token"), 13)
	var cmd tea.Cmd
	m, cmd = m.Update(m.loadPersonaSessionsCmd()())
	if m.err != "" || cmd == nil {
		t.Fatalf("open cloud: error=%q command=%v", m.err, cmd != nil)
	}
	m, _ = m.Update(cmd())
	if !strings.Contains(m.renderHistory("principal_engineer", 60), "hello back") {
		t.Fatalf("agent response missing: %s", m.renderHistory("principal_engineer", 60))
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Text: "hello"})
	m, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("no send command")
	}
	m, cmd = m.Update(cmd())
	if m.err != "" {
		t.Fatalf("send: %s", m.err)
	}
	if cmd != nil {
		m, _ = m.Update(cmd())
	}
}

func TestCloudChatUntrustedRendering(t *testing.T) {
	m := focusInput(NewCloudChatModel())
	m, _ = m.Update(tea.PasteMsg{Content: "hello\x1b]52;c;Y2xpcGJvYXJk\a"})
	// Check both keyboard input and server error paths through Update and View.
	m, _ = m.Update(tea.KeyPressMsg{Text: "\x1b]52;c;Y2xpcGJvYXJk\a"})
	if strings.Contains(m.View(80, 18), "\x1b]52") {
		t.Error("composer emits OSC clipboard escape")
	}
	m, _ = m.Update(cloudPersonaSessionsMsg{err: fmt.Errorf("denied\x1b]52;c;Y2xpcGJvYXJk\a")})
	if strings.Contains(m.View(80, 18), "\x1b]52") {
		t.Error("HTTP error emits OSC clipboard escape")
	}
}

func TestCloudChatFitsTerminal(t *testing.T) {
	for _, width := range []int{80, 100, 200} {
		m := Model{activeView: ViewCloudChat, width: width, height: 24, cloudChat: NewCloudChatModel()}
		m.cloudChat.focus = CloudFocusInput
		m.cloudChat.input = strings.Repeat("wide 界\n", 100)
		m.cloudChat.err = "a server error with a long message " + strings.Repeat("x", 200)
		view := m.renderCloudChat()
		if got := lipgloss.Width(view); got > width {
			t.Errorf("width=%d rendered=%d", width, got)
		}
		if got := lipgloss.Height(view); got > 24 {
			t.Errorf("width=%d height=%d", width, got)
		}
	}
}

func TestCloudChatPollAndSendOrdering(t *testing.T) {
	fake := &fakeCloudChatBackend{}
	m := NewCloudChatModelWithClient(fake, 13)
	m.sessionsByPersona["principal_engineer"] = &Session{ID: "session-pe"}
	m = focusInput(m)
	m, _ = m.Update(tea.PasteMsg{Content: "hello"})
	var send tea.Cmd
	m, send = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(tea.KeyPressMsg{Text: "second draft"})
	var duplicate tea.Cmd
	m, duplicate = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if send == nil || duplicate != nil {
		t.Fatal("expected one in-flight send")
	}
	first := m.loadMessagesCmd("principal_engineer")
	if first == nil || m.loadMessagesCmd("principal_engineer") != nil {
		t.Fatal("expected one in-flight read")
	}
	fake.messages = []SessionMessage{{ID: "1:prompt", Kind: "user", Text: "hello"}}
	m, _ = m.Update(first())
	if len(m.Messages("principal_engineer")) != 2 {
		t.Fatal("poll dropped pending send")
	}
	fake.sentMessage = &fake.messages[0]
	m, _ = m.Update(send())
	if messages := m.Messages("principal_engineer"); len(messages) != 1 || messages[0].Pending {
		t.Fatalf("ack duplicated the polled message: %+v", messages)
	}
	m.mergeMessages("principal_engineer", []SessionMessage{{ID: "1:response", Kind: "agent", Text: "partial"}})
	m.mergeMessages("principal_engineer", []SessionMessage{{ID: "1:response", Kind: "agent", Text: "complete"}})
	if messages := m.Messages("principal_engineer"); len(messages) != 2 || messages[1].Text != "complete" {
		t.Fatalf("response update lost: %+v", messages)
	}
	if m.input != "second draft" {
		t.Fatalf("send ack overwrote newer input: %q", m.input)
	}
}

func TestCloudChatDropsPreviousSessionHistory(t *testing.T) {
	fake := &fakeCloudChatBackend{messages: []SessionMessage{{ID: "old", Text: "old session"}}}
	m := NewCloudChatModelWithClient(fake, 13)
	m.sessionsByPersona["principal_engineer"] = &Session{ID: "old"}
	oldRead := m.loadMessagesCmd("principal_engineer")
	m.appendMessage("principal_engineer", CloudChatMessage{Text: "old history"})
	var newRead tea.Cmd
	m, newRead = m.Update(cloudPersonaSessionsMsg{sessions: map[string]*Session{"principal_engineer": {ID: "new"}}})
	if newRead == nil {
		t.Fatal("new session did not load")
	}
	m, _ = m.Update(oldRead())
	if len(m.Messages("principal_engineer")) != 0 {
		t.Fatal("late old-session reply contaminated new history")
	}
	if m.loading["principal_engineer"] != "new" {
		t.Fatal("stale reply cleared newer request")
	}
}

func TestCloudChatDraftAndFailedSend(t *testing.T) {
	m := NewCloudChatModelWithClient(&fakeCloudChatBackend{}, 13)
	m.sessionsByPersona["principal_engineer"] = &Session{ID: "session-pe"}
	m = focusInput(m)
	m, _ = m.Update(tea.PasteMsg{Content: "keep my draft"})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
	if m.input != "" {
		t.Fatal("draft leaked to another persona")
	}
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyUp})
	if m.input != "keep my draft" {
		t.Fatal("draft lost on persona switch")
	}
	m = focusInput(m)
	m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	m, _ = m.Update(cloudPromptSentMsg{personaKey: "principal_engineer", sessionID: "session-pe", text: "keep my draft", err: fmt.Errorf("offline")})
	if m.input != "keep my draft" {
		t.Fatal("failed send lost draft")
	}
	if msg := m.Messages("principal_engineer")[0]; msg.Pending || !msg.Failed {
		t.Fatalf("failed send still appears pending: %+v", msg)
	}
}

func TestCloudChatParentKeepsResponsesAfterLeaving(t *testing.T) {
	child := NewCloudChatModelWithClient(&fakeCloudChatBackend{}, 13)
	child.sessionsByPersona["principal_engineer"] = &Session{ID: "session-pe"}
	child.loading["principal_engineer"] = "session-pe"
	m := Model{activeView: ViewSessions, cloudChat: child}
	updated, _ := m.Update(cloudSessionMessagesMsg{sessionID: "session-pe", personaKey: "principal_engineer", messages: []SessionMessage{{ID: "1", Text: "reply"}}})
	got := updated.(Model)
	if len(got.cloudChat.Messages("principal_engineer")) != 1 || got.cloudChat.loading["principal_engineer"] != "" {
		t.Fatal("background completion lost when leaving cloud view")
	}
}

func TestCloudChatAnswersQuestionDespiteStalePoll(t *testing.T) {
	replied := false
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer test-token" {
			t.Error("missing reply authentication")
		}
		switch {
		case r.Method == http.MethodPut && r.URL.Path == "/rest/v1/vibeflow/projects/13/prompts/question-one/respond":
			var body map[string]string
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			if body["response_text"] != "yes" {
				t.Errorf("wrong response: %+v", body)
			}
			replied = true
			fmt.Fprint(w, `{"status":"ok","prompt_id":"question-one"}`)
		case r.Method == http.MethodGet && r.URL.Path == "/rest/v1/vibeflow/projects/13/prompts":
			fmt.Fprint(w, `{"prompts":[{"id":1,"prompt_id":"question-one","prompt_text":"First question?","source":"agent","status":"pending"},{"id":2,"prompt_id":"question-two","prompt_text":"Second question?","source":"agent","status":"pending"}]}`)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	m := NewCloudChatModelWithClient(NewClient(srv.URL, "test-token"), 13)
	m.sessionsByPersona["principal_engineer"] = &Session{ID: "session-pe"}
	m, _ = m.Update(m.loadMessagesCmd("principal_engineer")())
	staleRead := m.loadMessagesCmd("principal_engineer")()
	m = focusInput(m)
	if !strings.Contains(m.renderChatPane(60, 16), "First question?") {
		t.Fatal("reply target not displayed")
	}
	m, _ = m.Update(tea.KeyPressMsg{Text: "yes"})
	var reply tea.Cmd
	m, reply = m.Update(tea.KeyPressMsg{Code: 'r', Mod: tea.ModCtrl})
	if reply == nil {
		t.Fatal("Ctrl+R did not submit a reply")
	}
	m, _ = m.Update(reply())
	if !replied || m.err != "" {
		t.Fatalf("reply failed: %s", m.err)
	}
	m, _ = m.Update(staleRead)
	if next := m.oldestQuestion("principal_engineer"); next.ReplyPromptID != "question-two" {
		t.Fatalf("stale poll resurrected answered question: %+v", next)
	}
	messages := m.Messages("principal_engineer")
	if len(messages) != 3 || messages[2].ID != "1:response" || messages[2].Pending {
		t.Fatalf("reply echo wasn't acknowledged: %+v", messages)
	}
}

func TestCloudChatFailedSendSurvivesSessionRefresh(t *testing.T) {
	for _, offscreen := range []bool{false, true} {
		for _, newerDraft := range []bool{false, true} {
			t.Run(fmt.Sprintf("offscreen=%v/newer=%v", offscreen, newerDraft), func(t *testing.T) {
				m := NewCloudChatModelWithClient(&fakeCloudChatBackend{}, 13)
				m.sessionsByPersona["principal_engineer"] = &Session{ID: "old"}
				m = focusInput(m)
				m, _ = m.Update(tea.KeyPressMsg{Text: "recover this text"})
				m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
				if newerDraft {
					m, _ = m.Update(tea.KeyPressMsg{Text: "newer draft"})
				}
				if offscreen {
					m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyEsc})
					m, _ = m.Update(tea.KeyPressMsg{Code: tea.KeyDown})
				}
				m, _ = m.Update(cloudPersonaSessionsMsg{sessions: map[string]*Session{"principal_engineer": {ID: "new"}}})
				m, _ = m.Update(cloudPromptSentMsg{personaKey: "principal_engineer", sessionID: "old", text: "recover this text", err: fmt.Errorf("offline")})
				draft := m.input
				if offscreen {
					draft = m.drafts["principal_engineer"]
				}
				if !newerDraft && draft != "recover this text" {
					t.Fatalf("failed text lost: %q", draft)
				}
				if newerDraft {
					if draft != "newer draft" {
						t.Fatalf("newer draft overwritten: %q", draft)
					}
					messages := m.Messages("principal_engineer")
					if len(messages) != 1 || messages[0].Text != "recover this text" || !messages[0].Failed {
						t.Fatalf("failed text lost from local history: %+v", messages)
					}
				}
			})
		}
	}
}
