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
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// Client wraps the VibeFlow REST API.
type Client struct {
	baseURL    string
	token      string
	httpClient *http.Client
}

// NewClient creates a new VibeFlow API client.
func NewClient(baseURL, token string) *Client {
	return &Client{
		baseURL: baseURL,
		token:   token,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
		},
	}
}

// Project represents a VibeFlow project.
type Project struct {
	ID          int64     `json:"id"`
	Name        string    `json:"name"`
	Description string    `json:"description"`
	Status      string    `json:"status"`
	CreatedAt   time.Time `json:"created_at"`
}

// Session represents a VibeFlow session.
type Session struct {
	ID               string    `json:"session_id"`
	ProjectID        int64     `json:"project_id"`
	PersonaKey       string    `json:"persona_key"`
	Active           bool      `json:"active"`
	Stale            bool      `json:"stale"`
	AgentType        string    `json:"agent_type"`
	GitBranch        string    `json:"git_branch"`
	WorkingDirectory string    `json:"working_directory"`
	Status           string    `json:"status"`
	LastHeartbeat    time.Time `json:"last_heartbeat"`
}

// SessionMessage is a chat/log entry returned for a cloud agent session.
type SessionMessage struct {
	ReplyPromptID string    `json:"-"`
	ID            string    `json:"id"`
	Sender        string    `json:"sender"`
	Text          string    `json:"text"`
	Kind          string    `json:"kind"`
	Timestamp     time.Time `json:"timestamp"`
	Pending       bool      `json:"pending,omitempty"`
}

// WorkItem represents a todo or issue from polling.
type WorkItem struct {
	Type        string `json:"type"`
	ID          int64  `json:"id"`
	Title       string `json:"title"`
	Status      string `json:"status"`
	Priority    string `json:"priority"`
	ProjectID   int64  `json:"project_id"`
	FeatureID   int64  `json:"feature_id,omitempty"`
	FeatureName string `json:"feature_name,omitempty"`
}

// PollResult holds the response from polling for work.
type PollResult struct {
	StuckTodos  []WorkItem `json:"stuck_todos"`
	StuckIssues []WorkItem `json:"stuck_issues"`
	ReadyTodos  []WorkItem `json:"ready_todos"`
	ReadyIssues []WorkItem `json:"ready_issues"`
}

type DispatchQueueItem struct {
	ID           int64           `json:"id"`
	ProjectID    int64           `json:"project_id"`
	SessionID    string          `json:"session_id"`
	PersonaKey   string          `json:"persona_key"`
	GitBranch    string          `json:"git_branch"`
	Kind         string          `json:"kind"`
	WorkItemType string          `json:"work_item_type,omitempty"`
	WorkItemID   *int64          `json:"work_item_id,omitempty"`
	PromptID     string          `json:"prompt_id,omitempty"`
	Payload      json.RawMessage `json:"payload"`
	State        string          `json:"state"`
}

type DispatchNextRequest struct {
	SessionID       string `json:"session_id"`
	ProjectID       int64  `json:"project_id"`
	PersonaKey      string `json:"persona_key"`
	GitBranch       string `json:"git_branch"`
	LeaseOwner      string `json:"lease_owner"`
	LeaseTTLSeconds int    `json:"lease_ttl_seconds"`
}

type DispatchNextResponse struct {
	Status   string             `json:"status"`
	Dispatch *DispatchQueueItem `json:"dispatch"`
}

// ListProjects returns all non-archived projects.
func (c *Client) ListProjects() ([]Project, error) {
	var projects []Project
	if err := c.get("/rest/v1/vibeflow/projects", &projects); err != nil {
		return nil, fmt.Errorf("list projects: %w", err)
	}
	return projects, nil
}

// CreateProject creates a new project and returns it.
func (c *Client) CreateProject(name string) (*Project, error) {
	body := map[string]string{"name": name}
	var project Project
	if err := c.post("/rest/v1/vibeflow/projects", body, &project); err != nil {
		return nil, fmt.Errorf("create project: %w", err)
	}
	return &project, nil
}

// ListSessions returns all sessions for a project.
func (c *Client) ListSessions(projectID int64) ([]Session, error) {
	var sessions []Session
	if err := c.get(fmt.Sprintf("/rest/v1/vibeflow/projects/%d/sessions", projectID), &sessions); err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	return sessions, nil
}

// ListPersonaSessions returns the most recent active session per persona.
func (c *Client) ListPersonaSessions(projectID int64) (map[string]*Session, error) {
	var result struct {
		Sessions []struct {
			Session
			Heartbeat string `json:"last_heartbeat"`
		} `json:"sessions"`
	}
	if err := c.get(fmt.Sprintf("/rest/v1/vibeflow/sessions/active?project_id=%d", projectID), &result); err != nil {
		return nil, fmt.Errorf("list active sessions: %w", err)
	}
	byPersona := make(map[string]*Session)
	for _, entry := range result.Sessions {
		session := entry.Session
		if session.ID == "" || session.ProjectID != projectID || session.PersonaKey == "" || !session.Active || session.Stale {
			continue
		}
		// Stale sessions can contain malformed heartbeat strings; filter them before parsing.
		session.LastHeartbeat, _ = time.Parse(time.RFC3339Nano, entry.Heartbeat)
		current := byPersona[session.PersonaKey]
		if current == nil || session.LastHeartbeat.After(current.LastHeartbeat) {
			byPersona[session.PersonaKey] = &session
		}
	}
	return byPersona, nil
}

type sessionPrompt struct {
	PromptID    string     `json:"prompt_id"`
	Status      string     `json:"status"`
	ID          int64      `json:"id"`
	Text        string     `json:"prompt_text"`
	Response    string     `json:"response_text"`
	Source      string     `json:"source"`
	CreatedAt   time.Time  `json:"created_at"`
	RespondedAt *time.Time `json:"responded_at"`
}

func (p sessionPrompt) messages() []SessionMessage {
	kind, replyKind := "user", "agent"
	if p.Source == "agent" {
		kind, replyKind = "agent", "user"
	}
	messages := []SessionMessage{{ID: fmt.Sprintf("%d:prompt", p.ID), Text: p.Text, Kind: kind, Timestamp: p.CreatedAt}}
	if p.Source == "agent" && p.Status == "pending" {
		messages[0].ReplyPromptID = p.PromptID
	}
	if p.Response != "" {
		timestamp := p.CreatedAt
		if p.RespondedAt != nil {
			timestamp = *p.RespondedAt
		}
		messages = append(messages, SessionMessage{ID: fmt.Sprintf("%d:response", p.ID), Text: p.Response, Kind: replyKind, Timestamp: timestamp})
	}
	return messages
}

// GetSessionMessages refreshes recent prompts, including responses added to existing records.
func (c *Client) GetSessionMessages(projectID int64, sessionID string) ([]SessionMessage, error) {
	// ponytail: retain the latest 200 prompts; add before_id pagination when older-history browsing is needed.
	path := fmt.Sprintf("/rest/v1/vibeflow/projects/%d/prompts?session_id=%s&limit=200", projectID, url.QueryEscape(sessionID))
	var result struct {
		Prompts []sessionPrompt `json:"prompts"`
	}
	if err := c.get(path, &result); err != nil {
		return nil, fmt.Errorf("get session messages: %w", err)
	}
	var messages []SessionMessage
	for _, prompt := range result.Prompts {
		messages = append(messages, prompt.messages()...)
	}
	sort.SliceStable(messages, func(i, j int) bool { return messages[i].Timestamp.Before(messages[j].Timestamp) })
	return messages, nil
}

// SendSessionPrompt enqueues a user prompt on the project's existing prompt endpoint.
func (c *Client) SendSessionPrompt(projectID int64, sessionID, text string) (*SessionMessage, error) {
	body := map[string]string{"session_id": sessionID, "text": text}
	var prompt sessionPrompt
	if err := c.post(fmt.Sprintf("/rest/v1/vibeflow/projects/%d/prompts", projectID), body, &prompt); err != nil {
		return nil, fmt.Errorf("send session prompt: %w", err)
	}
	message := prompt.messages()[0]
	return &message, nil
}

// RespondSessionPrompt answers a specific agent question and wakes its waiting session.
func (c *Client) RespondSessionPrompt(projectID int64, promptID, text string) error {
	path := fmt.Sprintf("/rest/v1/vibeflow/projects/%d/prompts/%s/respond", projectID, url.PathEscape(promptID))
	return c.writeJSON(http.MethodPut, path, map[string]string{"response_text": text}, nil)
}

// PollPendingWork returns ready and stuck work items for a project.
func (c *Client) PollPendingWork(projectID int64) (*PollResult, error) {
	var result PollResult
	if err := c.get(fmt.Sprintf("/rest/v1/vibeflow/projects/%d/poll", projectID), &result); err != nil {
		return nil, fmt.Errorf("poll pending work: %w", err)
	}
	return &result, nil
}

// SessionInitRequest holds the parameters for initialising a vibeflow session.
type SessionInitRequest struct {
	ProjectName      string `json:"project_name"`
	SessionID        string `json:"session_id,omitempty"`
	Persona          string `json:"persona,omitempty"`
	GitBranch        string `json:"git_branch"`
	WorkingDirectory string `json:"working_directory"`
	AgentType        string `json:"agent_type,omitempty"`
	AgentModel       string `json:"agent_model,omitempty"`
}

// SessionInitResult holds the response from session_init.
type SessionInitResult struct {
	SessionID     string `json:"session_id"`
	ProjectID     int64  `json:"project_id"`
	ProjectName   string `json:"project_name"`
	Prompt        string `json:"prompt"`
	SessionReused bool   `json:"session_reused"`
}

// SessionInit initialises a vibeflow agent session and returns the server-
// generated session ID and agent prompt.
func (c *Client) SessionInit(req SessionInitRequest) (*SessionInitResult, error) {
	var result SessionInitResult
	if err := c.post("/rest/v1/vibeflow/sessions/init", req, &result); err != nil {
		return nil, fmt.Errorf("session init: %w", err)
	}
	return &result, nil
}

// SessionRegisterRequest holds the parameters for registering a session.
type SessionRegisterRequest struct {
	SessionID        string `json:"session_id"`
	ProjectID        int64  `json:"project_id"`
	WorkingDirectory string `json:"working_directory"`
	GitBranch        string `json:"git_branch"`
	GitWorktreePath  string `json:"git_worktree_path,omitempty"`
	GitRemoteURL     string `json:"git_remote_url,omitempty"`
}

// SessionRegister persists a session in the vibeflow database so it appears
// in the web UI.
func (c *Client) SessionRegister(req SessionRegisterRequest) error {
	var discard json.RawMessage
	if err := c.post("/rest/v1/vibeflow/sessions/register", req, &discard); err != nil {
		return fmt.Errorf("session register: %w", err)
	}
	return nil
}

func (c *Client) DispatchNext(req DispatchNextRequest) (*DispatchNextResponse, error) {
	var result DispatchNextResponse
	if err := c.post("/rest/v1/vibeflow/dispatch/next", req, &result); err != nil {
		return nil, fmt.Errorf("dispatch next: %w", err)
	}
	return &result, nil
}

func (c *Client) DispatchAck(id int64, leaseOwner string) error {
	var discard json.RawMessage
	if err := c.post(fmt.Sprintf("/rest/v1/vibeflow/dispatch/%d/ack", id), map[string]string{"lease_owner": leaseOwner}, &discard); err != nil {
		return fmt.Errorf("dispatch ack: %w", err)
	}
	return nil
}

func (c *Client) DispatchNack(id int64, leaseOwner, reason string) error {
	var discard json.RawMessage
	body := map[string]string{"lease_owner": leaseOwner, "reason": reason}
	if err := c.post(fmt.Sprintf("/rest/v1/vibeflow/dispatch/%d/nack", id), body, &discard); err != nil {
		return fmt.Errorf("dispatch nack: %w", err)
	}
	return nil
}

func (c *Client) get(path string, result interface{}) error {
	req, err := http.NewRequest("GET", c.baseURL+path, nil)
	if err != nil {
		return err
	}

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(body))
	}

	return json.NewDecoder(resp.Body).Decode(result)
}

func (c *Client) post(path string, body interface{}, result interface{}) error {
	return c.writeJSON(http.MethodPost, path, body, result)
}

func (c *Client) writeJSON(method, path string, body interface{}, result interface{}) error {
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest(method, c.baseURL+path, bytes.NewReader(data))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")

	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.httpClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		respBody, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("HTTP %d: %s", resp.StatusCode, string(respBody))
	}

	if result != nil {
		return json.NewDecoder(resp.Body).Decode(result)
	}
	return nil
}
