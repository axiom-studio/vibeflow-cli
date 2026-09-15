package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type reviewStartupChoice struct{ Label, Value string }
type reviewStartupInput struct {
	Field, Message string
	Choices        []reviewStartupChoice
}

// Numeric selections are IDs in both the ordinary TUI and review setup.
func reviewProjectMatches(project Project, selection string) bool {
	if id, err := strconv.ParseInt(selection, 10, 64); err == nil {
		return id > 0 && project.ID == id
	}
	return selection == project.Name
}

func resolveReviewStartup(ctx context.Context, cfg *Config, o reviewWatchOptions) (reviewWatchOptions, *reviewStartupInput, error) {
	if err := ctx.Err(); err != nil {
		return o, nil, err
	}
	if cfg == nil || cfg.ServerURL == "" || strings.TrimSpace(cfg.APIToken) == "" {
		return o, nil, fmt.Errorf("connect VibeFlow before starting a review runner")
	}
	if o.Project == "" {
		o.Project = cfg.DefaultProject
	}
	if o.Provider == "" {
		o.Provider = cfg.DefaultProvider
	}
	if o.Repository == "" {
		o.Repository = cfg.DefaultWorkDir
	}
	if o.Repository == "" {
		o.Repository, _ = os.Getwd()
	}
	if o.Kind == "" {
		o.Kind = "local"
	}
	if o.Name == "" {
		o.Name, _ = os.Hostname()
	}
	if !reviewStartupText(o.Name, 100) {
		o.Name = "Review runner"
	}
	if o.PollInterval == 0 {
		o.PollInterval = 5 * time.Second
	}
	if o.Timeout == 0 {
		o.Timeout = 15 * time.Minute
	}

	client := NewClient(cfg.ServerURL, cfg.APIToken)
	var projects []Project
	if err := client.reviewRequest(ctx, "GET", "/projects", nil, &projects); err != nil {
		return o, nil, fmt.Errorf("could not load review projects: %w", err)
	}
	if len(projects) == 0 {
		return o, nil, fmt.Errorf("no accessible projects; create a project or request project access in VibeFlow before starting a review runner")
	}
	var matches []Project
	var choices []reviewStartupChoice
	for _, project := range projects {
		if project.ID <= 0 || !reviewStartupText(project.Name, 300) {
			return o, nil, fmt.Errorf("invalid review project response")
		}
		value := strconv.FormatInt(project.ID, 10)
		choices = append(choices, reviewStartupChoice{Label: fmt.Sprintf("%s (%s)", project.Name, value), Value: value})
		if o.Project == "" || reviewProjectMatches(project, o.Project) {
			matches = append(matches, project)
		}
	}
	if len(matches) != 1 {
		o.ProjectID = 0
		if len(matches) > 1 && o.Project != "" {
			choices = nil
			for _, project := range matches {
				value := strconv.FormatInt(project.ID, 10)
				choices = append(choices, reviewStartupChoice{Label: fmt.Sprintf("%s (%s)", project.Name, value), Value: value})
			}
		}
		return o, &reviewStartupInput{Field: "project", Message: "Choose the project for this review runner.", Choices: choices}, nil
	}
	o.ProjectID, o.Project = matches[0].ID, strconv.FormatInt(matches[0].ID, 10)
	if o.Provider != "claude" && o.Provider != "codex" {
		return o, &reviewStartupInput{Field: "provider", Message: "Choose a supported review provider.", Choices: []reviewStartupChoice{{Label: "Claude", Value: "claude"}, {Label: "Codex", Value: "codex"}}}, nil
	}
	if cfg.Providers[o.Provider].Binary == "" {
		return o, nil, fmt.Errorf("selected review provider is not configured; set its binary in config.yaml")
	}
	missingRepo := &reviewStartupInput{Field: "repository", Message: "Enter the local checkout path for a repository linked to this project."}
	if o.Repository == "" || !reviewStartupText(o.Repository, 4096) {
		return o, missingRepo, nil
	}
	repository, err := filepath.Abs(o.Repository)
	if err != nil {
		return o, missingRepo, nil
	}
	o.Repository = repository
	remote, err := reviewGit(ctx, repository, "remote", "get-url", "origin")
	if ctx.Err() != nil {
		return o, nil, ctx.Err()
	}
	if err != nil {
		return o, missingRepo, nil
	}
	host, name, err := reviewRemoteIdentity(strings.TrimSpace(string(remote)))
	if err != nil {
		return o, missingRepo, nil
	}
	var linked struct {
		Repositories []struct {
			Provider string `json:"provider"`
			Host     string `json:"provider_host"`
			ID       int64  `json:"repository_link_id"`
			Name     string `json:"repository_name"`
		} `json:"repositories"`
	}
	if err = client.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-repositories", o.ProjectID), nil, &linked); err != nil {
		return o, nil, fmt.Errorf("could not load linked review repositories: %w", err)
	}
	if linked.Repositories == nil {
		return o, nil, fmt.Errorf("invalid linked review repository response")
	}
	choices = nil
	selected := ""
	for _, repo := range linked.Repositories {
		repoHost, repoName, parseErr := reviewRemoteIdentity("https://" + repo.Host + "/" + repo.Name)
		owner, repositoryName, _ := strings.Cut(repo.Name, "/")
		if repo.ID <= 0 || !reviewStartupText(repo.Host, 253) || !reviewStartupText(repo.Name, 512) || parseErr != nil || owner == "." || repositoryName == "." || repoHost != strings.ToLower(repo.Host) || repoName != strings.ToLower(repo.Name) || (repo.Provider != "github" && repo.Provider != "bitbucket") || (repo.Provider == "bitbucket" && repoHost != "bitbucket.org") {
			return o, nil, fmt.Errorf("invalid linked review repository response")
		}
		if repoHost != host || repoName != name {
			continue
		}
		value := fmt.Sprintf("%s:%d", repo.Provider, repo.ID)
		choices = append(choices, reviewStartupChoice{Label: fmt.Sprintf("%s/%s (%s, link %d)", repo.Host, repo.Name, repo.Provider, repo.ID), Value: value})
		if repo.ID == o.RepositoryLinkID && repo.Provider == o.GitProvider {
			selected = value
		}
	}
	if len(choices) == 0 {
		return o, missingRepo, nil
	}
	if selected == "" && len(choices) == 1 && o.RepositoryLinkID == 0 {
		selected = choices[0].Value
	}
	if selected == "" {
		return o, &reviewStartupInput{Field: "repository_link", Message: "Choose the repository link for this checkout.", Choices: choices}, nil
	}
	provider, id, _ := strings.Cut(selected, ":")
	o.GitProvider, o.RepositoryLinkID = provider, 0
	o.RepositoryLinkID, _ = strconv.ParseInt(id, 10, 64)
	if (cfg.LLMGatewayEnabled && strings.TrimSpace(o.Model) == "") || (o.Model != "" && !reviewStartupText(o.Model, 200)) {
		return o, &reviewStartupInput{Field: "model", Message: "Enter the model to use for PR reviews."}, nil
	}
	return o, nil, nil
}

func reviewStartupText(value string, limit int) bool {
	if strings.TrimSpace(value) == "" || len(value) > limit {
		return false
	}
	for _, r := range value {
		if !unicode.IsPrint(r) {
			return false
		}
	}
	return true
}

// These are reusable inputs only. Consent and credentials never enter this file.
type reviewStartupPreferences struct {
	Context          reviewStartupPreferenceContext `json:"context"`
	Project          string                         `json:"project"`
	Provider         string                         `json:"provider"`
	Repository       string                         `json:"repository"`
	GitProvider      string                         `json:"git_provider"`
	RepositoryLinkID int64                          `json:"repository_link_id"`
	Model            string                         `json:"model,omitempty"`
}

type reviewStartupPreferenceContext struct {
	ServerURL  string `json:"server_url"`
	ConfigPath string `json:"config_path"`
	Project    string `json:"default_project"`
	Provider   string `json:"default_provider"`
	WorkDir    string `json:"default_work_dir"`
	CWD        string `json:"cwd,omitempty"`
	Gateway    bool   `json:"gateway"`
}

func reviewStartupContext(cfg *Config, configPath, cwd string) (reviewStartupPreferenceContext, error) {
	path, err := filepath.Abs(configPath)
	if err != nil || cfg == nil {
		return reviewStartupPreferenceContext{}, fmt.Errorf("could not resolve review preference context")
	}
	if filepath.IsAbs(cfg.DefaultWorkDir) {
		cwd = ""
	} else if cwd, err = filepath.Abs(cwd); err != nil {
		return reviewStartupPreferenceContext{}, fmt.Errorf("could not resolve review working directory")
	}
	return reviewStartupPreferenceContext{ServerURL: cfg.ServerURL, ConfigPath: path, Project: cfg.DefaultProject, Provider: cfg.DefaultProvider, WorkDir: cfg.DefaultWorkDir, CWD: cwd, Gateway: cfg.LLMGatewayEnabled}, nil
}

func reviewStartupOptions(cfg *Config, configPath, cwd string) reviewWatchOptions {
	o := reviewWatchOptions{Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute}
	if cfg == nil {
		return o
	}
	o.Project, o.Provider, o.Repository = cfg.DefaultProject, cfg.DefaultProvider, cfg.DefaultWorkDir
	if o.Repository == "" {
		o.Repository = cwd
	}
	o.Name, _ = os.Hostname()
	if !reviewStartupText(o.Name, 100) {
		o.Name = "Review runner"
	}
	key, err := reviewStartupContext(cfg, configPath, cwd)
	if err != nil {
		return o
	}
	data, err := os.ReadFile(filepath.Join(RootDir(), "review-runner-preferences.json"))
	var prefs reviewStartupPreferences
	if err != nil || len(data) > 64<<10 || json.Unmarshal(data, &prefs) != nil || prefs.Context != key {
		return o
	}
	o.Project, o.Provider, o.Repository = prefs.Project, prefs.Provider, prefs.Repository
	o.GitProvider, o.RepositoryLinkID, o.Model = prefs.GitProvider, prefs.RepositoryLinkID, prefs.Model
	return o
}

func saveReviewStartupOptions(cfg *Config, configPath string, o reviewWatchOptions) error {
	cwd, err := os.Getwd()
	if err != nil {
		return fmt.Errorf("could not resolve review working directory")
	}
	key, err := reviewStartupContext(cfg, configPath, cwd)
	if err != nil {
		return err
	}
	server, err := url.Parse(key.ServerURL)
	if err != nil || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return fmt.Errorf("invalid review server URL")
	}
	if !filepath.IsAbs(o.Repository) || o.ProjectID <= 0 || o.RepositoryLinkID <= 0 || (o.GitProvider != "github" && o.GitProvider != "bitbucket") || (o.Provider != "claude" && o.Provider != "codex") {
		return fmt.Errorf("resolve review inputs before saving preferences")
	}
	prefs := reviewStartupPreferences{Context: key, Project: strconv.FormatInt(o.ProjectID, 10), Provider: o.Provider, Repository: o.Repository, GitProvider: o.GitProvider, RepositoryLinkID: o.RepositoryLinkID, Model: o.Model}
	if err = os.MkdirAll(RootDir(), 0700); err != nil {
		return fmt.Errorf("could not save review preferences")
	}
	if err = saveReviewJSON(filepath.Join(RootDir(), "review-runner-preferences.json"), prefs); err != nil {
		return fmt.Errorf("could not save review preferences")
	}
	return nil
}
