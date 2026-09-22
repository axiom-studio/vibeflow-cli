package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"sort"
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

type reviewBinding struct {
	Options     reviewWatchOptions
	ProjectName string
	Repository  reviewStartupRepository
	Checkouts   []reviewStartupCheckout
	Problem     string
}

type reviewDiscovery struct {
	Projects []Project
	Bindings []reviewBinding
	Problems map[int64]string
	Revoked  map[int64]bool
	Complete bool // False for failed or legacy capped project enumeration.
	Warning  string
}

func knownReviewCheckoutPaths(cfg *Config, cwd string, sessions []SessionMeta) []string {
	paths := append([]string{cfg.DefaultWorkDir, cwd}, cfg.DirectoryHistory...)
	for _, session := range sessions {
		paths = append(paths, session.WorkingDir, session.WorktreePath)
	}
	seen := map[string]bool{}
	var result []string
	for _, path := range paths {
		if path == "" {
			continue
		}
		path, err := filepath.Abs(path)
		if err != nil {
			continue
		}
		if canonical, err := filepath.EvalSymlinks(path); err == nil {
			path = canonical
		}
		if !seen[path] {
			seen[path] = true
			result = append(result, path)
		}
	}
	sort.Strings(result)
	return result
}

func validReviewStartupRepository(repo reviewStartupRepository) bool {
	host, name, err := reviewRemoteIdentity("https://" + repo.Host + "/" + repo.Name)
	owner, repository, _ := strings.Cut(repo.Name, "/")
	return repo.ID > 0 && reviewStartupText(repo.Host, 253) && reviewStartupText(repo.Name, 512) && err == nil && owner != "." && repository != "." && host == strings.ToLower(repo.Host) && name == strings.ToLower(repo.Name) && (repo.Provider == "github" || repo.Provider == "bitbucket") && (repo.Provider != "bitbucket" || host == "bitbucket.org") && (repo.Provider != "github" || host != "bitbucket.org")
}

func discoverReviewBindings(ctx context.Context, cfg *Config, paths []string, preferred map[string]string) (reviewDiscovery, error) {
	if cfg == nil || cfg.ServerURL == "" || strings.TrimSpace(cfg.APIToken) == "" {
		return reviewDiscovery{Problems: map[int64]string{}, Revoked: map[int64]bool{}}, fmt.Errorf("connect VibeFlow before starting review runners")
	}
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	d, err := listReviewProjects(ctx, client)
	if err != nil {
		return d, err
	}
	return discoverReviewProjectBindings(ctx, cfg, client, d, paths, preferred)
}

// Shared read-only enumeration also serves browsing without runner consent.
func listReviewProjects(ctx context.Context, client *Client) (reviewDiscovery, error) {
	d := reviewDiscovery{Problems: map[int64]string{}, Revoked: map[int64]bool{}}
	seenProjects, seenCursors := map[int64]bool{}, map[string]bool{}
	cursor := ""
	for {
		path := "/projects?paginated=true&limit=100"
		if cursor != "" {
			path += "&after_id=" + cursor
		}
		var raw json.RawMessage
		if err := client.reviewRequest(ctx, "GET", path, nil, &raw); err != nil {
			var response *reviewHTTPError
			if errors.As(err, &response) && (response.Status == 401 || response.Status == 403) {
				d.Complete = true
			}
			return d, err
		}
		var page struct {
			Projects []Project `json:"projects"`
			Next     string    `json:"next_after_id"`
		}
		legacy := strings.HasPrefix(strings.TrimSpace(string(raw)), "[")
		if legacy {
			if cursor != "" || json.Unmarshal(raw, &page.Projects) != nil {
				return d, fmt.Errorf("invalid review project page")
			}
		} else if json.Unmarshal(raw, &page) != nil || page.Projects == nil {
			return d, fmt.Errorf("invalid review project page")
		}
		for _, project := range page.Projects {
			if project.ID <= 0 || !reviewStartupText(project.Name, 300) || seenProjects[project.ID] {
				return d, fmt.Errorf("invalid or duplicate review project")
			}
			seenProjects[project.ID] = true
			d.Projects = append(d.Projects, project)
		}
		if legacy || page.Next == "" {
			d.Complete = true
			if legacy && len(page.Projects) >= 200 {
				d.Complete = false
				d.Warning = "Server returned the legacy 200-project limit; review discovery may be incomplete. Upgrade the server for full coverage."
			}
			break
		}
		next, err := strconv.ParseInt(page.Next, 10, 64)
		if err != nil || next <= 0 || strconv.FormatInt(next, 10) != page.Next || seenCursors[page.Next] {
			return d, fmt.Errorf("invalid or repeated review project cursor")
		}
		seenCursors[page.Next] = true
		cursor = page.Next
	}
	return d, nil
}

func discoverReviewProjectBindings(ctx context.Context, cfg *Config, client *Client, d reviewDiscovery, paths []string, preferred map[string]string) (reviewDiscovery, error) {
	base := reviewWatchOptions{Kind: "local", PollInterval: 5 * time.Second, Timeout: 15 * time.Minute}
	base.Name, _ = os.Hostname()
	if !reviewStartupText(base.Name, 100) {
		base.Name = "Review runner"
	}
	// Preferences are supplied explicitly; discovery never reads session consent.
	base.Provider, base.Project, base.Repository = cfg.DefaultProvider, "", ""
	for _, project := range d.Projects {
		var linked struct {
			Repositories []reviewStartupRepository `json:"repositories"`
		}
		err := client.reviewRequest(ctx, "GET", fmt.Sprintf("/projects/%d/pr-review-repositories", project.ID), nil, &linked)
		if err != nil {
			d.Problems[project.ID] = err.Error()
			var response *reviewHTTPError
			if errors.As(err, &response) && (response.Status == 401 || response.Status == 403 || response.Status == 404) {
				d.Revoked[project.ID] = true
			}
			continue
		}
		valid := linked.Repositories != nil
		seenLinks := map[string]bool{}
		for _, repo := range linked.Repositories {
			key := fmt.Sprintf("%s:%d", repo.Provider, repo.ID)
			if !validReviewStartupRepository(repo) || seenLinks[key] {
				valid = false
				break
			}
			seenLinks[key] = true
		}
		if !valid {
			d.Problems[project.ID] = "invalid linked review repository response"
			continue
		}
		for _, repo := range linked.Repositories {
			o := base
			o.ProjectID, o.Project, o.GitProvider, o.RepositoryLinkID = project.ID, strconv.FormatInt(project.ID, 10), repo.Provider, repo.ID
			binding := reviewBinding{Options: o, ProjectName: project.Name, Repository: repo}
			id := reviewBackgroundID(cfg.ServerURL, o)
			if selected := findReviewStartupCheckout(ctx, preferred[id], []reviewStartupRepository{repo}); selected != nil {
				binding.Options.Repository = selected.Path
			}
			seen := map[string]bool{}
			for _, path := range paths {
				if ctx.Err() != nil {
					return d, ctx.Err()
				}
				candidate := findReviewStartupCheckout(ctx, path, []reviewStartupRepository{repo})
				if candidate != nil && !seen[candidate.Identity] {
					seen[candidate.Identity] = true
					binding.Checkouts = append(binding.Checkouts, *candidate)
				}
			}
			if binding.Options.Repository == "" {
				if len(binding.Checkouts) == 1 {
					binding.Options.Repository = binding.Checkouts[0].Path
				} else if len(binding.Checkouts) == 0 {
					binding.Problem = "No known checkout; press Enter to enter a path."
				} else {
					binding.Problem = "Choose a local checkout; press Enter."
				}
			}
			d.Bindings = append(d.Bindings, binding)
		}
	}
	return d, nil
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
		if !validReviewStartupRepository(repo) {
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
	Checkouts        map[string]string              `json:"checkouts,omitempty"`
	Context          reviewStartupPreferenceContext `json:"context"`
	Project          string                         `json:"project"`
	Provider         string                         `json:"provider"`
	Repository       string                         `json:"repository"`
	GitProvider      string                         `json:"git_provider"`
	RepositoryLinkID int64                          `json:"repository_link_id"`
	Model            string                         `json:"model,omitempty"`
}

func loadReviewGroupPreferences(cfg *Config, configPath, cwd string) (reviewWatchOptions, map[string]string) {
	o := reviewStartupOptions(cfg, configPath, cwd)
	choices := map[string]string{}
	key, err := reviewStartupContext(cfg, configPath, cwd)
	if err != nil {
		return o, choices
	}
	data, err := os.ReadFile(filepath.Join(RootDir(), "review-runner-preferences.json"))
	var prefs reviewStartupPreferences
	if err != nil || len(data) > 4<<20 || json.Unmarshal(data, &prefs) != nil {
		return o, choices
	}
	// default_project only scopes ordinary sessions, never the runner group.
	prefs.Context.Project, key.Project = "", ""
	if prefs.Context != key {
		return o, choices
	}
	if prefs.Provider == "claude" || prefs.Provider == "codex" {
		o.Provider, o.Model = prefs.Provider, prefs.Model
	}
	for id, path := range prefs.Checkouts {
		choices[id] = path
	}
	if prefs.Repository != "" {
		legacy := o
		legacy.ProjectID, _ = strconv.ParseInt(prefs.Project, 10, 64)
		legacy.RepositoryLinkID, legacy.GitProvider = prefs.RepositoryLinkID, prefs.GitProvider
		if legacy.ProjectID > 0 && legacy.RepositoryLinkID > 0 {
			choices[reviewBackgroundID(cfg.ServerURL, legacy)] = prefs.Repository
		}
	}
	return o, choices
}

func saveReviewGroupPreferences(cfg *Config, configPath string, o reviewWatchOptions, choices map[string]string) error {
	cwd, err := os.Getwd()
	if err != nil {
		return err
	}
	key, err := reviewStartupContext(cfg, configPath, cwd)
	if err != nil {
		return err
	}
	key.Project = ""
	server, err := url.Parse(key.ServerURL)
	if err != nil || server.Host == "" || server.User != nil || server.RawQuery != "" || server.Fragment != "" {
		return fmt.Errorf("invalid review server URL")
	}
	if (o.Provider != "claude" && o.Provider != "codex") || (o.Model != "" && !reviewStartupText(o.Model, 200)) {
		return fmt.Errorf("invalid review provider or model")
	}
	if err := os.MkdirAll(RootDir(), 0700); err != nil {
		return err
	}
	return saveReviewJSON(filepath.Join(RootDir(), "review-runner-preferences.json"), reviewStartupPreferences{Context: key, Provider: o.Provider, Model: o.Model, Checkouts: choices})
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
