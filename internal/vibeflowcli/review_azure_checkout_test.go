package vibeflowcli

import (
	"context"
	"net/http"
	"net/http/cgi"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReviewAzureRemoteIdentity(t *testing.T) {
	for raw, want := range map[string]string{
		"https://dev.azure.com/org/Project/_git/Repo":                          "dev.azure.com project/repo",
		"https://org@dev.azure.com/org/My%20Project/_git/Repo":                 "dev.azure.com my project/repo",
		"https://dev.azure.com/org/_git/Repo":                                  "dev.azure.com repo/repo",
		"https://tfs.example.com/DefaultCollection/Project/_git/Repo/":         "tfs.example.com project/repo",
		"git@ssh.dev.azure.com:v3/org/Project/Repo":                            "dev.azure.com project/repo",
		"git@ssh.dev.azure.com:v3/org/My%20Project/Repo":                       "dev.azure.com my project/repo",
		"https://org.visualstudio.com/Project/_git/Repo":                       "org.visualstudio.com project/repo",
		"https://org.visualstudio.com/DefaultCollection/Project/_git/Repo.git": "org.visualstudio.com project/repo",
	} {
		host, name, err := reviewAzureRemoteIdentity(raw)
		if err != nil || host+" "+name != want {
			t.Errorf("%s = %q %q %v, want %q", raw, host, name, err, want)
		}
	}
	for _, raw := range []string{
		"https://user:secret@dev.azure.com/org/Project/_git/Repo",
		"http://dev.azure.com/org/Project/_git/Repo",
		"https://dev.azure.com/org/Project/Repo",
		"https://dev.azure.com/org/Project/_git/Repo?x=1",
		"https://dev.azure.com:8443/org/Project/_git/Repo",
		"https://dev.azure.com/org/Project/_git/..",
		"git@ssh.dev.azure.com:v3/org/Project",
	} {
		if _, _, err := reviewAzureRemoteIdentity(raw); err == nil {
			t.Errorf("accepted invalid Azure remote %s", raw)
		}
	}
}

func azureReviewTestRepo(t *testing.T) (string, *reviewExecution) {
	t.Helper()
	source, e := reviewTestRepo(t)
	reviewTestGit(t, source, "remote", "set-url", "origin", "https://org@dev.azure.com/org/My%20Project/_git/Repo")
	e.Review.Provider, e.Review.ProviderHost = "azure_devops", "dev.azure.com"
	clone := "https://dev.azure.com/org/My%20Project/_git/Repo"
	e.Attempt.Round.Details = reviewRepository{BaseRepositoryName: "My Project/Repo", HeadRepositoryName: "My Project/Repo", BaseCloneURL: clone, HeadCloneURL: clone}
	return source, e
}

func TestReviewAzureCheckoutUsesLocalCommits(t *testing.T) {
	source, e := azureReviewTestRepo(t)
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, e); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "input", "head", "main.go")); err != nil {
		t.Fatal(err)
	}
	e.Attempt.Round.Details.BaseRepositoryName = "Other/Repo"
	if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), e); err == nil {
		t.Fatal("checkout for another Azure repository was accepted")
	}
}

func TestReviewAzureCheckoutFetchesMissingCommitsThroughProxy(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git unavailable")
	}
	upstream, e := azureReviewTestRepo(t)
	bare := filepath.Join(t.TempDir(), "repo.git")
	reviewTestGit(t, filepath.Dir(bare), "clone", "--bare", upstream, bare)
	reviewTestGit(t, bare, "config", "uploadpack.allowAnySHA1InWant", "true")
	reviewTestGit(t, bare, "config", "http.receivepack", "false")
	gitPath, _ := exec.LookPath("git")
	backend := &cgi.Handler{Path: gitPath, Args: []string{"http-backend"}, Env: []string{"GIT_PROJECT_ROOT=" + filepath.Dir(bare), "GIT_HTTP_EXPORT_ALL=1"}}
	const token = "runner-token"
	const prefix = "/rest/v1/vibeflow/projects/1/pr-review-runners/r/jobs/j/attempts/a/git"
	var mu sync.Mutex
	var auth []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		auth = append(auth, r.Header.Get("Authorization"))
		mu.Unlock()
		if strings.Contains(r.URL.RawQuery, "receive-pack") || strings.HasSuffix(r.URL.Path, "receive-pack") {
			http.Error(w, "push denied", http.StatusForbidden)
			return
		}
		rest, ok := strings.CutPrefix(r.URL.Path, prefix)
		// Enforce the server proxy contract exactly.
		contract := (r.Method == http.MethodGet && rest == "/info/refs" && r.URL.RawQuery == "service=git-upload-pack") ||
			(r.Method == http.MethodPost && rest == "/git-upload-pack" && r.Header.Get("Content-Type") == "application/x-git-upload-pack-request")
		if !contract {
			t.Errorf("request outside the proxy contract: %s %s?%s", r.Method, r.URL.Path, r.URL.RawQuery)
		}
		if len(r.Header.Values("Authorization")) != 1 {
			t.Errorf("Authorization headers = %q", r.Header.Values("Authorization"))
		}
		if !ok || !contract || r.Header.Get("Authorization") != "Bearer "+token {
			http.Error(w, "denied", http.StatusForbidden)
			return
		}
		r.URL.Path = "/repo.git" + rest
		backend.ServeHTTP(w, r)
	}))
	defer server.Close()

	// The selected checkout matches the repository but lacks the review commits.
	source := t.TempDir()
	reviewTestGit(t, source, "init")
	reviewTestGit(t, source, "remote", "add", "origin", "https://dev.azure.com/org/My%20Project/_git/Repo")
	if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), e); err == nil {
		t.Fatal("missing Azure commits were fetched without the VibeFlow proxy")
	}
	// A user's global extra header must not be sent alongside the proxy token.
	globalConfig := filepath.Join(t.TempDir(), "gitconfig")
	if err := os.WriteFile(globalConfig, []byte("[http]\n\textraHeader = Authorization: Bearer user-global\n[credential]\n\thelper = !false\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_CONFIG_GLOBAL", globalConfig)
	root := t.TempDir()
	proxy := reviewGitProxy{URL: server.URL + prefix, Token: token}
	if err := prepareReviewCheckout(context.Background(), source, root, e, proxy); err != nil {
		t.Fatalf("fetch through proxy: %v", err)
	}
	if data, err := os.ReadFile(filepath.Join(root, "input", "head", "main.go")); err != nil || !strings.Contains(string(data), "return 2") {
		t.Fatalf("head worktree content: %q %v", data, err)
	}
	mu.Lock()
	if len(auth) == 0 {
		t.Fatal("proxy was not used")
	}
	for _, a := range auth {
		if a != "Bearer "+token {
			t.Fatalf("proxy request not authorized with only the VibeFlow token: %q", auth)
		}
	}
	mu.Unlock()
	if config, err := os.ReadFile(filepath.Join(root, "objects.git", "config")); err != nil || strings.Contains(string(config), token) {
		t.Fatalf("runner token persisted in Git config: %v", err)
	}
	if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), e, reviewGitProxy{URL: server.URL + prefix, Token: "wrong"}); err == nil {
		t.Fatal("rejected proxy authorization still produced a checkout")
	}
	if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), e, reviewGitProxy{URL: "http://example.com" + prefix, Token: token}); err == nil {
		t.Fatal("non-loopback HTTP proxy URL was accepted")
	}
}

func TestReviewStartupAcceptsAzureRepositories(t *testing.T) {
	if !validReviewStartupRepository(reviewStartupRepository{ID: 3, Provider: "azure_devops", Host: "dev.azure.com", Name: "My Project/Repo"}) {
		t.Fatal("Azure repository with a spaced project name was rejected")
	}
	for _, repo := range []reviewStartupRepository{
		{ID: 3, Provider: "azure_devops", Host: "dev.azure.com", Name: "Project"},
		{ID: 3, Provider: "azure_devops", Host: "dev.azure.com/x", Name: "Project/Repo"},
		{ID: 3, Provider: "azure_devops", Host: "dev.azure.com", Name: "Project/a/b"},
	} {
		if validReviewStartupRepository(repo) {
			t.Errorf("accepted invalid Azure repository %+v", repo)
		}
	}
}
