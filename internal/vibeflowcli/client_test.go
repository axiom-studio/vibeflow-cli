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
	"strconv"
	"strings"
	"testing"
	"time"
	"unicode"
	"unicode/utf8"
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

// TestClient_HTTPErrorBodyIsDisplaySafe covers #4617: error bodies are quoted
// into errors that reach the terminal, so escape sequences from a hostile or
// MITM'd server must not survive, on both the GET and POST paths.
func TestClient_HTTPErrorBodyIsDisplaySafe(t *testing.T) {
	const hostile = "\x1b[2J\x1b[3J\x1b[H forged \x1b]0;pwned\x07 clip\x1b]52;c;ZXZpbA==\x07 ‮dlrow\x9b31m\nline2\x00end"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		w.Write([]byte(hostile))
	}))
	defer srv.Close()
	c := NewClient(srv.URL, "")

	_, getErr := c.ListProjects()
	_, postErr := c.CreateProject("p")
	for name, err := range map[string]error{"get": getErr, "post": postErr} {
		if err == nil {
			t.Fatalf("%s: expected an error for a 500 response", name)
		}
		msg := err.Error()
		for _, r := range msg {
			if unicode.IsControl(r) || unicode.Is(unicode.Cf, r) {
				t.Errorf("%s: control/format rune %U survived in %q", name, r, msg)
			}
		}
		if strings.Contains(msg, "pwned") || strings.Contains(msg, "ZXZpbA==") {
			t.Errorf("%s: OSC payload survived: %q", name, msg)
		}
		if !strings.Contains(msg, "HTTP 500") || !strings.Contains(msg, "forged") || !strings.Contains(msg, "line2") {
			t.Errorf("%s: readable text lost: %q", name, msg)
		}
	}
}

// TestClient_HTTPErrorBodyIsBounded proves a huge error body is neither fully
// buffered into the error nor quoted beyond httpErrorTextMaxRunes.
func TestClient_HTTPErrorBodyIsBounded(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		w.Write([]byte(strings.Repeat("é", 1<<20))) // 2 MiB of multibyte text
	}))
	defer srv.Close()

	_, err := NewClient(srv.URL, "").ListProjects()
	if err == nil {
		t.Fatal("expected an error for a 502 response")
	}
	_, quoted, ok := strings.Cut(err.Error(), "HTTP 502: ")
	if !ok {
		t.Fatalf("error lacks the HTTP status: %v", err)
	}
	if got := len([]rune(quoted)); got != httpErrorTextMaxRunes+1 { // cap + ellipsis
		t.Errorf("quoted %d runes, want %d", got, httpErrorTextMaxRunes+1)
	}
	if !strings.HasSuffix(quoted, "…") || strings.ContainsRune(quoted, utf8.RuneError) {
		t.Errorf("truncation must end in an ellipsis on a rune boundary: %q", quoted[len(quoted)-12:])
	}
}

// TestClient_HTTPErrorBodyReadIsBounded proves the client stops READING a
// huge error body, not just quoting less of it (#7688, the CWE-770 half of
// #4617): the server streams 64 MiB and counts what it could write before the
// client hung up. Without the read bound the client drains all of it.
func TestClient_HTTPErrorBodyReadIsBounded(t *testing.T) {
	const body = 64 << 20
	written := make(chan int, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(body))
		w.WriteHeader(http.StatusInternalServerError)
		chunk := []byte(strings.Repeat("x", 32<<10))
		n := 0
		for n < body {
			m, err := w.Write(chunk)
			n += m
			if err != nil {
				break
			}
		}
		written <- n
	}))
	defer srv.Close()

	if _, err := NewClient(srv.URL, "").ListProjects(); err == nil {
		t.Fatal("expected an error for a 500 response")
	}
	select {
	case n := <-written:
		// Socket and transport buffers absorb some writes after the client
		// stops; a client that read everything lets all 64 MiB through.
		if n >= 16<<20 {
			t.Errorf("server wrote %d of %d bytes before the client hung up; the client is reading the whole error body", n, body)
		}
	case <-time.After(30 * time.Second):
		t.Fatal("server never finished writing: the client neither read nor closed the body")
	}
}
