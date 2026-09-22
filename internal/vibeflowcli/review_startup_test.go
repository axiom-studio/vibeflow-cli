package vibeflowcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// A public GitHub repository must never select the same name or link ID from
// another provider or enterprise host. Resolution must not register a runner.
func TestReviewStartupResolvesConfiguredProjectAndLocalRepository(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	for _, tc := range []struct {
		remote, provider, host, name string
		link                         int64
	}{
		{"https://github.com/acme/repo.git", "github", "github.com", "acme/repo", 7},
		{"git@company.ghe.com:ACME/Repo.git", "github", "company.ghe.com", "acme/repo", 8},
		{"git@bitbucket.org:workspace/repo.git", "bitbucket", "bitbucket.org", "workspace/repo", 7},
	} {
		t.Run(tc.host, func(t *testing.T) {
			reviewTestGit(t, repo, "remote", "set-url", "origin", tc.remote)
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer startup-api-canary" {
					t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
					http.Error(w, "invalid", 400)
					return
				}
				switch r.URL.Path {
				case "/rest/v1/vibeflow/projects":
					fmt.Fprint(w, `[{"id":66,"name":"Axiom","description":"","status":"active","created_at":"2026-09-15T00:00:00Z"}]`)
				case "/rest/v1/vibeflow/projects/66/pr-review-repositories":
					fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"company.ghe.com","repository_link_id":8,"repository_name":"acme/repo"},{"provider":"bitbucket","provider_host":"bitbucket.org","repository_link_id":7,"repository_name":"workspace/repo"}]}`)
				default:
					t.Errorf("unexpected path %s", r.URL.Path)
					http.NotFound(w, r)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken, cfg.DefaultProject, cfg.DefaultWorkDir, cfg.DefaultProvider = server.URL, "startup-api-canary", "Axiom", repo, "codex"
			got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err != nil || input != nil {
				t.Fatalf("resolution: input=%+v err=%v", input, err)
			}
			if got.ProjectID != 66 || got.Project != "66" || got.Repository != repo || got.Provider != "codex" || got.Model != "" || got.GitProvider != tc.provider || got.RepositoryLinkID != tc.link {
				t.Fatalf("wrong project, checkout, or provider identity: %+v", got)
			}
		})
	}
}

func TestReviewStartupMissingProviderConfigDoesNotRepeatPicker(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":66,"name":"Axiom"}]`)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "test-token", "66"
	cfg.Providers["claude"] = Provider{}
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{Provider: "claude"}, false)
	if err == nil || input != nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("unconfigured selection must fail visibly, not loop: input=%+v err=%v", input, err)
	}
}

func TestReviewStartupAsksOnlyForMissingOrAmbiguousInputs(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	for _, tc := range []struct {
		name, projects, repositories, project, provider, model, path, field string
		gateway                                                             bool
		link                                                                int64
		gitProvider                                                         string
		choices                                                             []string
	}{
		{name: "duplicate project name", projects: `[{"id":66,"name":"Axiom"},{"id":67,"name":"Axiom"}]`, project: "Axiom", field: "project", choices: []string{"66", "67"}},
		{name: "missing project", project: "missing", field: "project", choices: []string{"66"}},
		{name: "invalid numeric project", project: "0", field: "project", choices: []string{"66"}},
		{name: "numeric project ID takes precedence", projects: `[{"id":66,"name":"Axiom"},{"id":67,"name":"66"}]`, project: "66"},
		{name: "unsupported model provider", provider: "gemini", field: "provider", choices: []string{"claude", "codex"}},
		{name: "missing checkout", path: "missing", field: "repository"},
		{name: "unlinked checkout", repositories: `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"other/repo"}]}`, field: "repository"},
		{name: "ambiguous repository links", repositories: `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/repo"}]}`, field: "repository_link", choices: []string{"github:7", "github:8"}},
		{name: "selected link resolves ambiguity", repositories: `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/repo"}]}`, gitProvider: "github", link: 8},
		{name: "gateway needs model", gateway: true, field: "model"},
		{name: "gateway model selected", gateway: true, model: "review-model"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			projects, repositories := tc.projects, tc.repositories
			if projects == "" {
				projects = `[{"id":66,"name":"Axiom"}]`
			}
			if repositories == "" {
				repositories = `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Method != "GET" {
					t.Errorf("resolver mutated server: %s", r.Method)
				}
				if r.URL.Path == "/rest/v1/vibeflow/projects" {
					fmt.Fprint(w, projects)
					return
				}
				if r.URL.Path == "/rest/v1/vibeflow/projects/66/pr-review-repositories" {
					fmt.Fprint(w, repositories)
					return
				}
				t.Errorf("unexpected path %s", r.URL.Path)
				http.NotFound(w, r)
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken, cfg.DefaultProject, cfg.DefaultWorkDir = server.URL, "startup-api-canary", "66", repo
			cfg.LLMGatewayEnabled = tc.gateway
			o := reviewWatchOptions{Project: tc.project, Provider: tc.provider, Model: tc.model, RepositoryLinkID: tc.link, GitProvider: tc.gitProvider}
			if tc.path != "" {
				o.Repository = filepath.Join(t.TempDir(), tc.path)
			}
			got, input, err := resolveReviewStartup(context.Background(), cfg, o, tc.path != "")
			if err != nil {
				t.Fatal(err)
			}
			if tc.field == "" {
				if input != nil {
					t.Fatalf("unnecessary input %+v", input)
				}
				if tc.link > 0 && got.RepositoryLinkID != tc.link {
					t.Fatalf("selected repository lost: %+v", got)
				}
				return
			}
			if input == nil || input.Field != tc.field {
				t.Fatalf("input %+v, want %s", input, tc.field)
			}
			if len(input.Choices) != len(tc.choices) {
				t.Fatalf("choices %+v, want %v", input.Choices, tc.choices)
			}
			for i, value := range tc.choices {
				if input.Choices[i].Value != value {
					t.Fatalf("choice %d = %+v, want %s", i, input.Choices[i], value)
				}
			}
		})
	}
}

func TestReviewStartupRejectsInvalidRepositoryDataAndHidesAPIErrors(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	for _, body := range []string{
		`{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":0,"repository_name":"acme/repo"}]}`,
		`{"repositories":[{"provider":"github","provider_host":"github.com/token-canary","repository_link_id":7,"repository_name":"acme/repo"}]}`,
		`{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"../repo"}]}`,
		`{"repositories":[{"provider":"bitbucket","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`,
		`{"repositories":[{"provider":"github","provider_host":"bitbucket.org","repository_link_id":7,"repository_name":"acme/repo"}]}`,
		`{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/\u001b[31mrepo"}]}`,
		`{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/."}]}`,
		`{}`,
		`token-canary`,
	} {
		t.Run(body, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/rest/v1/vibeflow/projects" {
					fmt.Fprint(w, `[{"id":66,"name":"Axiom"}]`)
					return
				}
				if body == "token-canary" {
					http.Error(w, body, 401)
					return
				}
				fmt.Fprint(w, body)
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken, cfg.DefaultProject, cfg.DefaultWorkDir = server.URL, "startup-api-canary", "66", repo
			_, _, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err == nil || strings.Contains(err.Error(), "token-canary") || strings.Contains(err.Error(), server.URL) {
				t.Fatalf("invalid or unsafe error %v", err)
			}
			if body == "token-canary" {
				var status *reviewHTTPError
				if !errors.As(err, &status) || status.Status != 401 {
					t.Fatalf("HTTP status lost: %v", err)
				}
			}
		})
	}
}

func TestReviewStartupPreferencesKeepSecretsAndConsentOutOfConfig(t *testing.T) {
	previousRoot := rootDir
	SetRootDir(t.TempDir())
	t.Cleanup(func() { SetRootDir(previousRoot) })
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject, cfg.DefaultProvider = "https://cloud.example", "startup-api-canary", "Axiom", "gemini"
	cfg.SavedEnvVars = map[string]string{"TOKEN": "saved-env-canary"}
	cwd := t.TempDir()
	t.Chdir(cwd)
	configPath := filepath.Join(RootDir(), "config.yaml")
	if err := os.WriteFile(configPath, []byte("original-config-canary"), 0600); err != nil {
		t.Fatal(err)
	}
	initial := reviewStartupOptions(cfg, configPath, cwd)
	if initial.Repository != cwd || initial.Provider != "gemini" || initial.Project != "Axiom" {
		t.Fatalf("defaults: %+v", initial)
	}
	o := initial
	o.Project, o.ProjectID, o.Provider, o.Model, o.RepositoryLinkID, o.GitProvider = "66", 66, "codex", "review-model", 7, "github"
	if err := saveReviewStartupOptions(cfg, configPath, o); err != nil {
		t.Fatal(err)
	}
	got := reviewStartupOptions(cfg, configPath, cwd)
	if got.Project != "66" || got.Provider != "codex" || got.Model != "review-model" || got.RepositoryLinkID != 7 || got.GitProvider != "github" || got.Repository != cwd {
		t.Fatalf("saved corrections lost: %+v", got)
	}
	data, err := os.ReadFile(filepath.Join(RootDir(), "review-runner-preferences.json"))
	if err != nil {
		t.Fatal(err)
	}
	for _, secret := range []string{"startup-api-canary", "saved-env-canary", "enabled", "consent", "APIToken"} {
		if strings.Contains(string(data), secret) {
			t.Fatalf("preferences persisted %s", secret)
		}
	}
	var value map[string]any
	if json.Unmarshal(data, &value) != nil {
		t.Fatal("invalid preferences")
	}
	info, err := os.Stat(filepath.Join(RootDir(), "review-runner-preferences.json"))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatalf("preferences mode: %v %v", info, err)
	}
	config, _ := os.ReadFile(configPath)
	if string(config) != "original-config-canary" {
		t.Fatal("auth config was rewritten")
	}
	for _, change := range []string{"server", "config", "project", "provider", "workdir", "gateway"} {
		t.Run(change, func(t *testing.T) {
			other := *cfg
			path := configPath
			switch change {
			case "server":
				other.ServerURL += "/"
			case "config":
				path += ".other"
			case "project":
				other.DefaultProject = "Other"
			case "provider":
				other.DefaultProvider = "claude"
			case "workdir":
				other.DefaultWorkDir = t.TempDir()
			case "gateway":
				other.LLMGatewayEnabled = true
			}
			got := reviewStartupOptions(&other, path, cwd)
			if got.RepositoryLinkID != 0 || got.Model != "" || got.Project == "66" {
				t.Fatalf("stale context loaded preferences: %+v", got)
			}
		})
	}
	if got := reviewStartupOptions(cfg, configPath, t.TempDir()); got.RepositoryLinkID != 0 || got.Project != "Axiom" {
		t.Fatalf("another launch directory reused saved inputs: %+v", got)
	}
	cfg.DefaultWorkDir = "checkout"
	if err := saveReviewStartupOptions(cfg, configPath, o); err != nil {
		t.Fatal(err)
	}
	if got := reviewStartupOptions(cfg, configPath, t.TempDir()); got.RepositoryLinkID != 0 || got.Repository != "checkout" {
		t.Fatalf("relative configured checkout reused another launch directory: %+v", got)
	}
}

func TestReviewStartupRejectsProjectTerminalControlsAndCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":66,"name":"Axiom\u001b[31m"}]`)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "startup-api-canary"
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if err == nil || input != nil || strings.Contains(err.Error(), "\x1b") {
		t.Fatalf("unsafe project response: %+v %v", input, err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, _, err = resolveReviewStartup(ctx, cfg, reviewWatchOptions{}, false)
	if err != context.Canceled {
		t.Fatalf("cancellation lost: %v", err)
	}
}

func TestReviewStartupUsesActualWorkingDirectoryWithNoConfiguredCheckout(t *testing.T) {
	withTempRoot(t)
	repo, _ := reviewTestRepo(t)
	t.Chdir(repo)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/rest/v1/vibeflow/projects" {
			fmt.Fprint(w, `[{"id":66,"name":"Axiom"}]`)
			return
		}
		fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "startup-api-canary"
	got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if err != nil || input != nil || got.Repository != repo || got.ProjectID != 66 {
		t.Fatalf("working checkout unresolved: %+v %+v %v", got, input, err)
	}
}

func TestReviewStartupEmptyProjectListOffersActionInsteadOfUnusablePrompt(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, `[]`) }))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "startup-api-canary"
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if input != nil || err == nil || !strings.Contains(strings.ToLower(err.Error()), "project") {
		t.Fatalf("unusable empty project prompt: %+v %v", input, err)
	}
}

func TestReviewStartupProjectAPIErrorsRetainSafeStatus(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "private-body-canary", 403) }))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "startup-api-canary"
	_, _, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	var status *reviewHTTPError
	if !errors.As(err, &status) || status.Status != 403 || strings.Contains(err.Error(), "private-body-canary") {
		t.Fatalf("API status lost or leaked: %v", err)
	}
}

func reviewStartupDiscoveryConfig(t *testing.T, repositories string) *Config {
	t.Helper()
	previousRoot := rootDir
	SetRootDir(t.TempDir())
	t.Cleanup(func() { SetRootDir(previousRoot) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" {
			t.Errorf("startup changed server state: %s %s", r.Method, r.URL.Path)
		}
		switch r.URL.Path {
		case "/rest/v1/vibeflow/projects":
			fmt.Fprint(w, `[{"id":66,"name":"Selected project"},{"id":67,"name":"Other project"}]`)
		case "/rest/v1/vibeflow/projects/66/pr-review-repositories":
			fmt.Fprint(w, repositories)
		default:
			t.Errorf("unexpected startup request: %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "startup-api-canary", "66"
	cfg.DefaultWorkDir = filepath.Join(t.TempDir(), "not-a-checkout")
	return cfg
}

func TestReviewStartupDiscoversKnownCheckoutFromHistoryOrSessions(t *testing.T) {
	for _, source := range []string{"directory history", "session working directory", "session worktree"} {
		t.Run(source, func(t *testing.T) {
			cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
			repo, _ := reviewTestRepo(t)
			var sessions []SessionMeta
			switch source {
			case "directory history":
				cfg.DirectoryHistory = []string{filepath.Join(t.TempDir(), "deleted"), repo}
			case "session working directory":
				sessions = []SessionMeta{{Project: "Incorrect project label", ProjectID: 67, WorkingDir: repo}}
			case "session worktree":
				sessions = []SessionMeta{{Project: "Incorrect project label", ProjectID: 67, WorkingDir: cfg.DefaultWorkDir, WorktreePath: repo}}
			}
			before, err := json.Marshal(sessions)
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(DefaultStorePath(), before, 0600); err != nil {
				t.Fatal(err)
			}
			got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err != nil || input != nil || got.Repository != repo || got.ProjectID != 66 || got.RepositoryLinkID != 7 {
				t.Fatalf("known checkout was not resolved: options=%+v input=%+v err=%v", got, input, err)
			}
			after, err := os.ReadFile(DefaultStorePath())
			if err != nil || string(after) != string(before) {
				t.Fatalf("startup rewrote session metadata: %q %v", after, err)
			}
			if _, err := os.Stat(DefaultStorePath() + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("startup created a session lock: %v", err)
			}
		})
	}
}

func TestReviewStartupDistinctKnownClonesRequireAChoice(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
	first, _ := reviewTestRepo(t)
	second, _ := reviewTestRepo(t)
	cfg.DirectoryHistory = []string{first, second}
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if err != nil || input == nil || input.Field != "repository_choice" || len(input.Choices) != 3 {
		t.Fatalf("distinct clones did not produce a picker: %+v %v", input, err)
	}
	for i, path := range []string{first, second} {
		if input.Choices[i].Value != path || !strings.Contains(input.Choices[i].Label, "github.com/acme/repo") || !strings.Contains(input.Choices[i].Label, path) {
			t.Fatalf("choice does not identify the checkout: %+v", input.Choices[i])
		}
	}
	if input.Choices[2].Value != "manual" || !strings.Contains(input.Message, "Selected project") {
		t.Fatalf("picker is missing manual escape or project context: %+v", input)
	}
}

func TestReviewStartupWithoutLinkedRepositoriesExplainsRequiredSetup(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[]}`)
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if input != nil || err == nil || !strings.Contains(err.Error(), "Selected project") || !strings.Contains(err.Error(), "link") {
		t.Fatalf("unlinked project asked for an unusable path: input=%+v err=%v", input, err)
	}
}

func TestReviewStartupKnownCheckoutDeduplicatesWorktreesAndSymlinks(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
	repo, _ := reviewTestRepo(t)
	worktree := filepath.Join(t.TempDir(), "agent-worktree")
	reviewTestGit(t, repo, "worktree", "add", "--detach", worktree, "HEAD")
	cfg.DirectoryHistory = []string{repo, worktree, repo}
	if runtime.GOOS != "windows" {
		alias := filepath.Join(t.TempDir(), "checkout-alias")
		if err := os.Symlink(repo, alias); err != nil {
			t.Fatal(err)
		}
		cfg.DirectoryHistory = append(cfg.DirectoryHistory, alias)
	}
	got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if err != nil || input != nil || got.Repository != repo || got.RepositoryLinkID != 7 {
		t.Fatalf("one Git checkout family became ambiguous: options=%+v input=%+v err=%v", got, input, err)
	}
}

func TestReviewStartupKnownCheckoutMustMatchSelectedProjectRemoteIdentity(t *testing.T) {
	for _, remote := range []string{
		"https://other.ghe.com/acme/repo.git",
		"https://bitbucket.org/acme/repo.git",
		"https://github.com/other/repo.git",
		"https://private-token-canary@github.com/acme/repo.git",
	} {
		t.Run(remote, func(t *testing.T) {
			cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
			repo, _ := reviewTestRepo(t)
			reviewTestGit(t, repo, "remote", "set-url", "origin", remote)
			sessions, err := json.Marshal([]SessionMeta{{Project: "Selected project", ProjectID: 66, WorkingDir: repo}})
			if err != nil {
				t.Fatal(err)
			}
			if err = os.WriteFile(DefaultStorePath(), sessions, 0600); err != nil {
				t.Fatal(err)
			}
			_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err != nil || input == nil || input.Field != "repository" || !strings.Contains(input.Message, "Selected project") || !strings.Contains(input.Message, "github.com/acme/repo") || strings.Contains(input.Message, "private-token-canary") {
				t.Fatalf("unrelated or unsafe checkout was accepted, or missing setup context: input=%+v err=%v", input, err)
			}
		})
	}
}

func TestReviewStartupKnownRepositoriesRemainDistinctChoices(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"company.ghe.com","repository_link_id":8,"repository_name":"acme/repo"}]}`)
	public, _ := reviewTestRepo(t)
	enterprise, _ := reviewTestRepo(t)
	reviewTestGit(t, enterprise, "remote", "set-url", "origin", "git@company.ghe.com:acme/repo.git")
	cfg.DirectoryHistory = []string{public, enterprise}
	_, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
	if err != nil || input == nil || input.Field != "repository_choice" || len(input.Choices) != 3 {
		t.Fatalf("distinct provider hosts were collapsed: %+v %v", input, err)
	}
	if !strings.Contains(input.Choices[0].Label, "github.com/acme/repo") || !strings.Contains(input.Choices[1].Label, "company.ghe.com/acme/repo") {
		t.Fatalf("repository labels lost provider hosts: %+v", input.Choices)
	}
}

func TestReviewStartupCorruptOrAbsentSessionsAreNotRewritten(t *testing.T) {
	for _, contents := range []string{"", "invalid-session-canary"} {
		t.Run(contents, func(t *testing.T) {
			cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
			repo, _ := reviewTestRepo(t)
			cfg.DirectoryHistory = []string{repo}
			if contents != "" {
				if err := os.WriteFile(DefaultStorePath(), []byte(contents), 0600); err != nil {
					t.Fatal(err)
				}
			}
			got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err != nil || input != nil || got.Repository != repo {
				t.Fatalf("unavailable sessions prevented history detection: %+v %+v %v", got, input, err)
			}
			after, err := os.ReadFile(DefaultStorePath())
			if contents == "" {
				if !os.IsNotExist(err) {
					t.Fatalf("startup created session metadata: %q %v", after, err)
				}
			} else if err != nil || string(after) != contents {
				t.Fatalf("startup rewrote corrupt session metadata: %q %v", after, err)
			}
			if _, err := os.Stat(DefaultStorePath() + ".lock"); !os.IsNotExist(err) {
				t.Fatalf("startup created session lock: %v", err)
			}
		})
	}
}

func TestReviewStartupKeepsValidPreferredCheckoutAheadOfDiscovery(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
	preferred, _ := reviewTestRepo(t)
	other, _ := reviewTestRepo(t)
	cfg.DirectoryHistory = []string{other}
	got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{Repository: preferred}, false)
	if err != nil || input != nil || got.Repository != preferred {
		t.Fatalf("history replaced the preferred checkout: %+v %+v %v", got, input, err)
	}
}

func TestReviewStartupRevalidatesChosenCheckoutWithoutSubstitutingAnother(t *testing.T) {
	for _, change := range []string{"remote", "path"} {
		t.Run(change, func(t *testing.T) {
			cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"}]}`)
			first, _ := reviewTestRepo(t)
			second, _ := reviewTestRepo(t)
			cfg.DirectoryHistory = []string{first, second}
			options, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{}, false)
			if err != nil || input == nil || input.Field != "repository_choice" {
				t.Fatalf("initial picker: %+v %v", input, err)
			}
			options.Repository = input.Choices[0].Value
			switch change {
			case "remote":
				reviewTestGit(t, first, "remote", "set-url", "origin", "https://github.com/other/repo.git")
			case "path":
				if err := os.Rename(first, first+"-moved"); err != nil {
					t.Fatal(err)
				}
			}
			got, input, err := resolveReviewStartup(context.Background(), cfg, options, true)
			if err != nil || input == nil || input.Field != "repository" || got.Repository == second || got.RepositoryLinkID != 0 {
				t.Fatalf("stale choice was accepted or silently substituted: %+v %+v %v", got, input, err)
			}
		})
	}
}

func TestReviewStartupStaleRememberedLinkRequiresCorrection(t *testing.T) {
	cfg := reviewStartupDiscoveryConfig(t, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"other/repo"}]}`)
	other, _ := reviewTestRepo(t)
	reviewTestGit(t, other, "remote", "set-url", "origin", "https://github.com/other/repo.git")
	cfg.DirectoryHistory = []string{other}
	got, input, err := resolveReviewStartup(context.Background(), cfg, reviewWatchOptions{RepositoryLinkID: 7, GitProvider: "github"}, false)
	if err != nil || input == nil || input.Field != "repository_link" || len(input.Choices) != 1 || input.Choices[0].Value != "github:8" || got.RepositoryLinkID != 7 {
		t.Fatalf("remembered link silently selected a different repository: %+v %+v %v", got, input, err)
	}
}
