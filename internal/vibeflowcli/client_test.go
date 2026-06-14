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
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

func TestNewClient(t *testing.T) {
	c := NewClient("http://localhost:7080", "tok123")
	if c.baseURL != "http://localhost:7080" {
		t.Errorf("baseURL = %q", c.baseURL)
	}
	if c.token != "tok123" {
		t.Errorf("token = %q", c.token)
	}
	if c.httpClient == nil {
		t.Error("httpClient should not be nil")
	}
}

func TestClient_ListProjects(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "GET" {
			t.Errorf("expected GET, got %s", r.Method)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Project{
			{ID: 1, Name: "project-a", Status: "active"},
			{ID: 2, Name: "project-b", Status: "done"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	projects, err := c.ListProjects()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(projects) != 2 {
		t.Fatalf("expected 2 projects, got %d", len(projects))
	}
	if projects[0].Name != "project-a" {
		t.Errorf("projects[0].Name = %q", projects[0].Name)
	}
}

func TestClient_ListProjects_HTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte("internal error"))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	_, err := c.ListProjects()
	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "HTTP 500") {
		t.Errorf("expected HTTP 500 in error, got: %v", err)
	}
}

func TestClient_CreateProject(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/projects" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		// Verify request body.
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		json.Unmarshal(body, &req)
		if req["name"] != "new-project" {
			t.Errorf("body name = %q, want new-project", req["name"])
		}

		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Project{ID: 42, Name: "new-project", Status: "active"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	project, err := c.CreateProject("new-project")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if project.ID != 42 {
		t.Errorf("ID = %d, want 42", project.ID)
	}
	if project.Name != "new-project" {
		t.Errorf("Name = %q", project.Name)
	}
}

func TestClient_ListSessions(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/projects/13/sessions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Session{
			{ID: "session-1", ProjectID: 13, AgentType: "claude"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	sessions, err := c.ListSessions(13)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("expected 1 session, got %d", len(sessions))
	}
	if sessions[0].ID != "session-1" {
		t.Errorf("session ID = %q", sessions[0].ID)
	}
}

func TestClient_ListPersonaSessions_GroupsMostRecentActiveByPersona(t *testing.T) {
	now := time.Now()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/projects/13/sessions" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Session{
			{ID: "old-architect", ProjectID: 13, PersonaKey: "architect", Status: "running", LastHeartbeat: now.Add(-5 * time.Minute)},
			{ID: "new-architect", ProjectID: 13, PersonaKey: "architect", Status: "running", LastHeartbeat: now},
			{ID: "done-dev", ProjectID: 13, PersonaKey: "developer", Status: "done", LastHeartbeat: now},
			{ID: "missing-persona", ProjectID: 13, Status: "running", LastHeartbeat: now},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	sessions, err := c.ListPersonaSessions(13)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(sessions) != 1 {
		t.Fatalf("grouped sessions len = %d, want 1", len(sessions))
	}
	if got := sessions["architect"].ID; got != "new-architect" {
		t.Errorf("architect session = %q, want new-architect", got)
	}
	if _, ok := sessions["developer"]; ok {
		t.Errorf("done developer session should not be returned")
	}
}

func TestClient_GetSessionMessages(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/sessions/session-abc/messages" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if got := r.URL.Query().Get("since"); got != "2026-06-14T13:00:00Z" {
			t.Errorf("since query = %q", got)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]SessionMessage{
			{Sender: "you", Text: "hello", Kind: "user"},
			{Sender: "Architect", Text: "hi", Kind: "agent"},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	messages, err := c.GetSessionMessages("session-abc", "2026-06-14T13:00:00Z")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(messages) != 2 {
		t.Fatalf("messages len = %d, want 2", len(messages))
	}
	if messages[1].Text != "hi" {
		t.Errorf("second message text = %q, want hi", messages[1].Text)
	}
}

func TestClient_SendSessionPrompt(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/sessions/session-abc/prompts" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("method = %s, want POST", r.Method)
		}
		body, _ := io.ReadAll(r.Body)
		var req map[string]string
		json.Unmarshal(body, &req)
		if got := req["text"]; got != "ship it" {
			t.Errorf("text = %q, want ship it", got)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(SessionMessage{Sender: "you", Text: "ship it", Kind: "user"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	message, err := c.SendSessionPrompt("session-abc", "ship it")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if message.Text != "ship it" {
		t.Errorf("message text = %q, want ship it", message.Text)
	}
}

func TestClient_PollPendingWork(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/projects/13/poll" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(PollResult{
			StuckTodos:  []WorkItem{{ID: 1, Title: "stuck-todo", Type: "todo"}},
			ReadyIssues: []WorkItem{{ID: 2, Title: "ready-issue", Type: "issue"}},
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	result, err := c.PollPendingWork(13)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(result.StuckTodos) != 1 {
		t.Errorf("expected 1 stuck todo, got %d", len(result.StuckTodos))
	}
	if len(result.ReadyIssues) != 1 {
		t.Errorf("expected 1 ready issue, got %d", len(result.ReadyIssues))
	}
	if result.StuckTodos[0].Title != "stuck-todo" {
		t.Errorf("stuck todo title = %q", result.StuckTodos[0].Title)
	}
}

func TestClient_SessionInit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/sessions/init" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		body, _ := io.ReadAll(r.Body)
		var req SessionInitRequest
		json.Unmarshal(body, &req)
		if req.ProjectName != "my-project" {
			t.Errorf("project_name = %q", req.ProjectName)
		}
		if req.GitBranch != "main" {
			t.Errorf("git_branch = %q", req.GitBranch)
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(SessionInitResult{
			SessionID:   "session-abc",
			ProjectID:   13,
			ProjectName: "my-project",
			Prompt:      "You are an agent...",
		})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	result, err := c.SessionInit(SessionInitRequest{
		ProjectName:      "my-project",
		GitBranch:        "main",
		WorkingDirectory: "/work",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if result.SessionID != "session-abc" {
		t.Errorf("SessionID = %q", result.SessionID)
	}
	if result.Prompt != "You are an agent..." {
		t.Errorf("Prompt = %q", result.Prompt)
	}
}

func TestClient_SessionRegister(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/rest/v1/vibeflow/sessions/register" {
			t.Errorf("unexpected path: %s", r.URL.Path)
		}
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}

		body, _ := io.ReadAll(r.Body)
		var req SessionRegisterRequest
		json.Unmarshal(body, &req)
		if req.SessionID != "session-abc" {
			t.Errorf("session_id = %q", req.SessionID)
		}

		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(`{"status":"ok"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	err := c.SessionRegister(SessionRegisterRequest{
		SessionID:        "session-abc",
		ProjectID:        13,
		WorkingDirectory: "/work",
		GitBranch:        "main",
	})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestClient_AuthHeaderWithToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "Bearer my-secret-token" {
			t.Errorf("Authorization = %q, want Bearer my-secret-token", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Project{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "my-secret-token")
	c.ListProjects()
}

func TestClient_AuthHeaderWithoutToken(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth := r.Header.Get("Authorization")
		if auth != "" {
			t.Errorf("expected no Authorization header, got %q", auth)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode([]Project{})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "")
	c.ListProjects()
}

func TestClient_NonOKResponse(t *testing.T) {
	tests := []struct {
		name       string
		statusCode int
		body       string
	}{
		{"bad request", 400, "invalid input"},
		{"unauthorized", 401, "not authorized"},
		{"not found", 404, "resource not found"},
		{"server error", 500, "internal error"},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.statusCode)
				w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "")
			_, err := c.ListProjects()
			if err == nil {
				t.Fatal("expected error")
			}
			if !strings.Contains(err.Error(), tc.body) {
				t.Errorf("expected error to contain %q, got: %v", tc.body, err)
			}
		})
	}
}

func TestClient_PostContentType(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ct := r.Header.Get("Content-Type")
		if ct != "application/json" {
			t.Errorf("Content-Type = %q, want application/json", ct)
		}
		w.WriteHeader(http.StatusCreated)
		json.NewEncoder(w).Encode(Project{ID: 1, Name: "test"})
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "tok")
	c.CreateProject("test")
}
