package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
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

type reviewStartupRepositoriesResponse struct {
	Repositories                []reviewStartupRepository `json:"repositories"`
	SupportedRunnerCapabilities []string                  `json:"supported_runner_capabilities"`
}

type reviewDiscovery struct {
	Projects []Project
	Complete bool // False for failed or legacy capped project enumeration.
	Warning  string
}

func validReviewStartupRepository(repo reviewStartupRepository) bool {
	host, name, err := reviewRemoteIdentity("https://" + repo.Host + "/" + repo.Name)
	owner, repository, _ := strings.Cut(repo.Name, "/")
	return repo.ID > 0 && reviewStartupText(repo.Host, 253) && reviewStartupText(repo.Name, 512) && err == nil && owner != "." && repository != "." && host == strings.ToLower(repo.Host) && name == strings.ToLower(repo.Name) && (repo.Provider == "github" || repo.Provider == "bitbucket") && (repo.Provider != "bitbucket" || host == "bitbucket.org") && (repo.Provider != "github" || host != "bitbucket.org")
}

// Shared read-only enumeration for browsing managed reviews.
func listReviewProjects(ctx context.Context, client *Client) (reviewDiscovery, error) {
	var d reviewDiscovery
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
	if !reviewHarnessSupported(o.Provider) {
		return o, &reviewStartupInput{Field: "provider", Message: "Choose Vera's coding harness.", Choices: reviewHarnessChoices(cfg)}, nil
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
