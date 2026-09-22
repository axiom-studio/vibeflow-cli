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
	ManualMessage  string
	Choices        []reviewStartupChoice
}

type reviewStartupRepository struct {
	Provider string `json:"provider"`
	Host     string `json:"provider_host"`
	ID       int64  `json:"repository_link_id"`
	Name     string `json:"repository_name"`
}

// Numeric selections are IDs in both the ordinary TUI and review setup.
func reviewProjectMatches(project Project, selection string) bool {
	if id, err := strconv.ParseInt(selection, 10, 64); err == nil {
		return id > 0 && project.ID == id
	}
	return selection == project.Name
}

func resolveReviewStartup(ctx context.Context, cfg *Config, o reviewWatchOptions, repositorySelected bool) (reviewWatchOptions, *reviewStartupInput, error) {
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
	var linked struct {
		Repositories []reviewStartupRepository `json:"repositories"`
	}
	if err := client.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-repositories", o.ProjectID), nil, &linked); err != nil {
		return o, nil, fmt.Errorf("could not load linked review repositories: %w", err)
	}
	if linked.Repositories == nil {
		return o, nil, fmt.Errorf("invalid linked review repository response")
	}
	projectLabel := fmt.Sprintf("%s (%d)", matches[0].Name, o.ProjectID)
	if len(linked.Repositories) == 0 {
		return o, nil, fmt.Errorf("%s has no linked repositories; link a GitHub or Bitbucket repository in VibeFlow project settings before starting a review runner", projectLabel)
	}
	var expected []string
	for _, repo := range linked.Repositories {
		repoHost, repoName, parseErr := reviewRemoteIdentity("https://" + repo.Host + "/" + repo.Name)
		owner, repositoryName, _ := strings.Cut(repo.Name, "/")
		if repo.ID <= 0 || !reviewStartupText(repo.Host, 253) || !reviewStartupText(repo.Name, 512) || parseErr != nil || owner == "." || repositoryName == "." || repoHost != strings.ToLower(repo.Host) || repoName != strings.ToLower(repo.Name) || (repo.Provider != "github" && repo.Provider != "bitbucket") || (repo.Provider == "bitbucket" && repoHost != "bitbucket.org") || (repo.Provider == "github" && repoHost == "bitbucket.org") {
			return o, nil, fmt.Errorf("invalid linked review repository response")
		}
		if len(expected) < 5 {
			expected = append(expected, repo.Host+"/"+repo.Name)
		}
	}
	if len(linked.Repositories) > len(expected) {
		expected = append(expected, fmt.Sprintf("and %d more linked repositories", len(linked.Repositories)-len(expected)))
	}
	missingRepo := &reviewStartupInput{Field: "repository", Message: fmt.Sprintf("Project: %s\nEnter a local checkout path matching: %s.", projectLabel, strings.Join(expected, ", "))}
	checkout := findReviewStartupCheckout(ctx, o.Repository, linked.Repositories)
	if checkout == nil && !repositorySelected {
		cwd, _ := os.Getwd()
		paths := append([]string{cfg.DefaultWorkDir, cwd}, cfg.DirectoryHistory...)
		// Session metadata suggests paths only. The selected project's linked
		// remote identities remain authoritative, including mislabeled sessions.
		if sessions, err := NewStore().readFile(); err == nil {
			for _, session := range sessions {
				paths = append(paths, session.WorkingDir, session.WorktreePath)
			}
		}
		var candidates []*reviewStartupCheckout
		seenPaths, seenCheckouts := map[string]bool{}, map[string]bool{}
		for _, path := range paths {
			if err := ctx.Err(); err != nil {
				return o, nil, err
			}
			canonical, err := filepath.EvalSymlinks(path)
			if err != nil || seenPaths[canonical] {
				continue
			}
			seenPaths[canonical] = true
			candidate := findReviewStartupCheckout(ctx, path, linked.Repositories)
			if candidate == nil || seenCheckouts[candidate.Identity] {
				continue
			}
			seenCheckouts[candidate.Identity] = true
			candidates = append(candidates, candidate)
		}
		if err := ctx.Err(); err != nil {
			return o, nil, err
		}
		if len(candidates) == 1 {
			checkout = candidates[0]
		} else if len(candidates) > 1 {
			choices = nil
			for _, candidate := range candidates {
				choices = append(choices, reviewStartupChoice{Label: candidate.Name + " - " + candidate.Path, Value: candidate.Path})
			}
			choices = append(choices, reviewStartupChoice{Label: "Enter another path", Value: "manual"})
			return o, &reviewStartupInput{Field: "repository_choice", Message: fmt.Sprintf("Choose a local checkout for %s.", projectLabel), ManualMessage: missingRepo.Message, Choices: choices}, nil
		}
	}
	if err := ctx.Err(); err != nil {
		return o, nil, err
	}
	if checkout == nil {
		return o, missingRepo, nil
	}
	o.Repository = checkout.Path
	choices = checkout.Links
	selected := ""
	for _, choice := range choices {
		if choice.Value == fmt.Sprintf("%s:%d", o.GitProvider, o.RepositoryLinkID) {
			selected = choice.Value
		}
	}
	if selected == "" && len(choices) == 1 && o.RepositoryLinkID == 0 {
		selected = choices[0].Value
	}
	if selected == "" {
		return o, &reviewStartupInput{Field: "repository_link", Message: fmt.Sprintf("Choose the repository link for %s in %s.", checkout.Name, projectLabel), Choices: choices}, nil
	}
	provider, id, _ := strings.Cut(selected, ":")
	o.GitProvider, o.RepositoryLinkID = provider, 0
	o.RepositoryLinkID, _ = strconv.ParseInt(id, 10, 64)
	if (cfg.LLMGatewayEnabled && strings.TrimSpace(o.Model) == "") || (o.Model != "" && !reviewStartupText(o.Model, 200)) {
		return o, &reviewStartupInput{Field: "model", Message: "Enter the model to use for PR reviews."}, nil
	}
	return o, nil, nil
}

type reviewStartupCheckout struct {
	Path, Name, Identity string
	Links                []reviewStartupChoice
}

func findReviewStartupCheckout(ctx context.Context, path string, repositories []reviewStartupRepository) *reviewStartupCheckout {
	if !reviewStartupText(path, 4096) {
		return nil
	}
	path, err := filepath.Abs(path)
	if err != nil || !reviewStartupText(path, 4096) {
		return nil
	}
	remote, err := reviewGit(ctx, path, "remote", "get-url", "origin")
	if err != nil {
		return nil
	}
	host, name, err := reviewRemoteIdentity(strings.TrimSpace(string(remote)))
	if err != nil {
		return nil
	}
	checkout := &reviewStartupCheckout{Path: path, Name: host + "/" + name}
	for _, repo := range repositories {
		if strings.ToLower(repo.Host) == host && strings.ToLower(repo.Name) == name {
			checkout.Links = append(checkout.Links, reviewStartupChoice{Label: fmt.Sprintf("%s/%s (%s, link %d)", repo.Host, repo.Name, repo.Provider, repo.ID), Value: fmt.Sprintf("%s:%d", repo.Provider, repo.ID)})
		}
	}
	if len(checkout.Links) == 0 {
		return nil
	}
	common, err := reviewGit(ctx, path, "rev-parse", "--git-common-dir")
	if err != nil {
		return nil
	}
	commonPath := strings.TrimSpace(string(common))
	if !filepath.IsAbs(commonPath) {
		canonicalPath, err := filepath.EvalSymlinks(path)
		if err != nil {
			return nil
		}
		commonPath = filepath.Join(canonicalPath, commonPath)
	}
	canonical, err := filepath.EvalSymlinks(commonPath)
	if err != nil {
		return nil
	}
	checkout.Identity = canonical + "\x00" + checkout.Name
	return checkout
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
