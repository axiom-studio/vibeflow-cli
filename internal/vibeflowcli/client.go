package vibeflowcli

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
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
	AgentType        string    `json:"agent_type"`
	GitBranch        string    `json:"git_branch"`
	WorkingDirectory string    `json:"working_directory"`
	Status           string    `json:"status"`
	LastHeartbeat    time.Time `json:"last_heartbeat"`
}

// WorkItem represents a todo or issue from polling.
type WorkItem struct {
	Type       string `json:"type"`
	ID         int64  `json:"id"`
	Title      string `json:"title"`
	Status     string `json:"status"`
	Priority   string `json:"priority"`
	ProjectID  int64  `json:"project_id"`
	FeatureID  int64  `json:"feature_id,omitempty"`
	FeatureName string `json:"feature_name,omitempty"`
}

// PollResult holds the response from polling for work.
type PollResult struct {
	StuckTodos  []WorkItem `json:"stuck_todos"`
	StuckIssues []WorkItem `json:"stuck_issues"`
	ReadyTodos  []WorkItem `json:"ready_todos"`
	ReadyIssues []WorkItem `json:"ready_issues"`
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
	data, err := json.Marshal(body)
	if err != nil {
		return err
	}

	req, err := http.NewRequest("POST", c.baseURL+path, bytes.NewReader(data))
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
