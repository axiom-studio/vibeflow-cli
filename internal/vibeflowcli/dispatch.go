package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/gorilla/websocket"
	"github.com/spf13/cobra"
)

func dispatchCmd() *cobra.Command {
	var sessionName string
	cmd := &cobra.Command{
		Use:    "dispatch",
		Short:  "Run the cloud-dispatch background loop for a session",
		Hidden: true,
		RunE: func(cmd *cobra.Command, args []string) error {
			if sessionName == "" {
				return fmt.Errorf("--session is required")
			}
			cfgPath, _ := cmd.Flags().GetString("config")
			return RunCloudDispatch(context.Background(), cfgPath, sessionName)
		},
	}
	cmd.Flags().StringVar(&sessionName, "session", "", "Session name to dispatch into")
	return cmd
}

func StartCloudDispatchProcess(cfgPath, sessionName string) error {
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	if cfgPath == "" {
		cfgPath = ConfigPath()
	}
	logDir := filepath.Join(RootDir(), "logs")
	if err := os.MkdirAll(logDir, 0755); err != nil {
		return err
	}
	logFile, err := os.OpenFile(filepath.Join(logDir, "cloud-dispatch.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	args := []string{"--root", RootDir(), "--config", cfgPath, "dispatch", "--session", sessionName}
	cmd := exec.Command(exe, args...)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// Detach the child into its own process group so it survives the parent
	// (Unix). No-op on Windows — see dispatch_unix.go / dispatch_windows.go.
	setProcessGroup(cmd)
	if err := cmd.Start(); err != nil {
		_ = logFile.Close()
		return err
	}
	return cmd.Process.Release()
}

func RunCloudDispatch(ctx context.Context, cfgPath, sessionName string) error {
	cfg, tmux, store, _, _, err := loadComponents(cfgPath)
	if err != nil {
		return err
	}
	logger := NewLogger()
	defer logger.Close()

	meta, ok, err := store.Get(sessionName)
	if err != nil {
		return err
	}
	if !ok {
		return fmt.Errorf("session %q not found", sessionName)
	}
	if meta.VibeFlowSessionID == "" {
		meta.VibeFlowSessionID = meta.Name
	}
	if meta.ProjectID == 0 {
		project, err := ensureCloudDispatchProject(cfg, meta.Project)
		if err != nil {
			return err
		}
		meta.ProjectID = project.ID
		_ = store.Add(meta)
	}
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	leaseOwner := "vibeflow-cli:" + meta.VibeFlowSessionID
	req := DispatchNextRequest{
		SessionID:       meta.VibeFlowSessionID,
		ProjectID:       meta.ProjectID,
		PersonaKey:      meta.Persona,
		GitBranch:       meta.Branch,
		LeaseOwner:      leaseOwner,
		LeaseTTLSeconds: 120,
	}

	for ctx.Err() == nil {
		if err := runDispatchWebSocket(ctx, cfg, client, tmux, meta, req, logger); err != nil {
			logger.Warn("cloud dispatch websocket unavailable for %s: %v", meta.Name, err)
		}
		deadline := time.Now().Add(time.Minute)
		for ctx.Err() == nil && time.Now().Before(deadline) {
			if err := pollDispatchOnce(ctx, client, tmux, meta, req, logger); err != nil {
				logger.Warn("cloud dispatch REST poll failed for %s: %v", meta.Name, err)
			}
			sleep := time.Duration(cfg.PollInterval) * time.Second
			if sleep <= 0 {
				sleep = 5 * time.Second
			}
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(sleep):
			}
		}
	}
	return ctx.Err()
}

func ensureCloudDispatchProject(cfg *Config, projectName string) (*Project, error) {
	if projectName == "" {
		projectName = cfg.DefaultProject
	}
	if projectName == "" {
		projectName = "Default"
	}
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	projects, err := client.ListProjects()
	if err != nil {
		return nil, err
	}
	for i := range projects {
		if projects[i].Name == projectName {
			return &projects[i], nil
		}
	}
	return client.CreateProject(projectName)
}

func runDispatchWebSocket(ctx context.Context, cfg *Config, client *Client, tmux *TmuxManager, meta SessionMeta, req DispatchNextRequest, logger *Logger) error {
	wsURL, err := dispatchWebSocketURL(cfg.ServerURL)
	if err != nil {
		return err
	}
	header := http.Header{}
	if cfg.APIToken != "" {
		header.Set("Authorization", "Bearer "+cfg.APIToken)
	}
	conn, _, err := websocket.DefaultDialer.DialContext(ctx, wsURL, header)
	if err != nil {
		return err
	}
	defer conn.Close()
	if err := conn.WriteJSON(req); err != nil {
		return err
	}
	for ctx.Err() == nil {
		var msg struct {
			Type     string             `json:"type"`
			Status   string             `json:"status"`
			Dispatch *DispatchQueueItem `json:"dispatch"`
		}
		if err := conn.ReadJSON(&msg); err != nil {
			return err
		}
		if msg.Dispatch != nil {
			if err := handleDispatchItem(client, tmux, meta, req.LeaseOwner, msg.Dispatch, logger); err != nil {
				return err
			}
		}
	}
	return ctx.Err()
}

func dispatchWebSocketURL(base string) (string, error) {
	u, err := url.Parse(strings.TrimRight(base, "/") + "/rest/v1/vibeflow/dispatch/ws")
	if err != nil {
		return "", err
	}
	switch u.Scheme {
	case "http":
		u.Scheme = "ws"
	case "https":
		u.Scheme = "wss"
	}
	return u.String(), nil
}

func pollDispatchOnce(ctx context.Context, client *Client, tmux *TmuxManager, meta SessionMeta, req DispatchNextRequest, logger *Logger) error {
	select {
	case <-ctx.Done():
		return ctx.Err()
	default:
	}
	resp, err := client.DispatchNext(req)
	if err != nil {
		return err
	}
	if resp != nil && resp.Dispatch != nil {
		return handleDispatchItem(client, tmux, meta, req.LeaseOwner, resp.Dispatch, logger)
	}
	return nil
}

func handleDispatchItem(client *Client, tmux *TmuxManager, meta SessionMeta, leaseOwner string, item *DispatchQueueItem, logger *Logger) error {
	prompt := formatDispatchPrompt(item)
	if err := tmux.SendKeys(meta.TmuxSession, prompt); err != nil {
		_ = client.DispatchNack(item.ID, leaseOwner, err.Error())
		return err
	}
	if err := client.DispatchAck(item.ID, leaseOwner); err != nil {
		return err
	}
	logger.Info("delivered cloud dispatch %d to %s (%s)", item.ID, meta.Name, item.Kind)
	return nil
}

func formatDispatchPrompt(item *DispatchQueueItem) string {
	payload := strings.TrimSpace(string(item.Payload))
	if payload == "" {
		payload = "{}"
	}
	body, _ := json.MarshalIndent(json.RawMessage(payload), "", "  ")
	return fmt.Sprintf(`VIBEFLOW_DISPATCH
dispatch_id: %d
kind: %s
project_id: %d
session_id: %s
persona: %s
git_branch: %s
work_item_type: %s
work_item_id: %s
prompt_id: %s

Use the VibeFlow MCP tools to inspect and process this exact dispatch. Do not call wait_for_work. Claim or respond to the referenced item as appropriate, execute the work end-to-end, update VibeFlow status/progress, and then return to idle for the next VIBEFLOW_DISPATCH handoff.

payload:
%s`, item.ID, item.Kind, item.ProjectID, item.SessionID, item.PersonaKey, item.GitBranch, item.WorkItemType, formatOptionalID(item.WorkItemID), item.PromptID, string(body))
}

func formatOptionalID(id *int64) string {
	if id == nil {
		return ""
	}
	return fmt.Sprintf("%d", *id)
}
