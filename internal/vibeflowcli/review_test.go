package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func reviewTestGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	// Detached maintenance can recreate files while TempDir cleanup removes them.
	cmd := exec.Command("git", append([]string{"-c", "maintenance.auto=false", "-c", "user.name=Review Test", "-c", "user.email=review@example.invalid", "-C", dir}, args...)...)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %v: %v %s", args, err, data)
	}
	return strings.TrimSpace(string(data))
}

func reviewTestRepo(t *testing.T) (string, *reviewExecution) {
	t.Helper()
	dir := t.TempDir()
	reviewTestGit(t, dir, "init")
	if err := os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc value() int { return 1 }\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewTestGit(t, dir, "add", ".")
	reviewTestGit(t, dir, "commit", "-m", "base")
	base := reviewTestGit(t, dir, "rev-parse", "HEAD")
	os.WriteFile(filepath.Join(dir, "main.go"), []byte("package main\nfunc value() int { return 2 }\n"), 0600)
	os.WriteFile(filepath.Join(dir, "hidden.txt"), []byte("export-ignore must not hide this evidence\n"), 0600)
	os.WriteFile(filepath.Join(dir, ".gitattributes"), []byte("hidden.txt export-ignore\n"), 0600)
	if runtime.GOOS != "windows" {
		if err := os.Symlink("../../outside-canary", filepath.Join(dir, "escape")); err != nil {
			t.Fatal(err)
		}
	}
	reviewTestGit(t, dir, "add", ".")
	reviewTestGit(t, dir, "commit", "-m", "head")
	head := reviewTestGit(t, dir, "rev-parse", "HEAD")
	reviewTestGit(t, dir, "remote", "add", "origin", "https://github.com/acme/repo.git")
	e := &reviewExecution{Version: 2, Prompt: "Review the exact change and stop."}
	e.Review = reviewJob{ID: reviewUUID(), Provider: "github", ProviderHost: "github.com", RepositoryLinkID: 7, HeadSHA: head, BaseSHA: base}
	e.Attempt.ID = reviewUUID()
	e.Attempt.RunnerID = reviewUUID()
	e.Attempt.Round.ID = reviewUUID()
	e.Attempt.Round.JobID = e.Review.ID
	e.Attempt.Round.HeadSHA = head
	e.Attempt.Round.BaseSHA = base
	e.Attempt.Round.DeadlineAt = time.Now().Add(time.Minute).UnixMilli()
	e.Attempt.LeaseExpiresAt = time.Now().Add(time.Minute).UnixMilli()
	e.Attempt.Round.Details = reviewRepository{BaseRepositoryName: "acme/repo", HeadRepositoryName: "acme/repo", BaseCloneURL: "https://github.com/acme/repo.git", HeadCloneURL: "https://github.com/acme/repo.git"}
	return dir, e
}

func TestReviewTestRepoDoesNotLaunchBackgroundMaintenance(t *testing.T) {
	tracePath := filepath.Join(t.TempDir(), "git-trace.jsonl")
	t.Setenv("GIT_TRACE2_EVENT", tracePath)
	reviewTestRepo(t)
	data, err := os.ReadFile(tracePath)
	if err != nil {
		t.Fatal(err)
	}
	decoder := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var event struct {
			Event string   `json:"event"`
			Argv  []string `json:"argv"`
		}
		if err := decoder.Decode(&event); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if event.Event == "child_start" && strings.Contains(strings.Join(event.Argv, " "), "maintenance run") {
			t.Fatalf("fixture launched background maintenance that can race TempDir cleanup: %v", event.Argv)
		}
	}
}

func TestReviewCheckoutExportsExactObjectsWithoutTouchingSource(t *testing.T) {
	source, e := reviewTestRepo(t)
	root := t.TempDir()
	os.WriteFile(filepath.Join(source, "local-only.txt"), []byte("developer's unsaved work"), 0600)
	status := reviewTestGit(t, source, "status", "--porcelain")
	if err := prepareReviewCheckout(context.Background(), source, root, e); err != nil {
		t.Fatal(err)
	}
	for name, want := range map[string]string{"base/main.go": "return 1", "head/main.go": "return 2", "head/hidden.txt": "export-ignore must not hide", "review.diff": "return 2"} {
		data, err := os.ReadFile(filepath.Join(root, "input", name))
		if err != nil || !strings.Contains(string(data), want) {
			t.Fatalf("%s: %q %v", name, data, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "input", "head", "local-only.txt")); !os.IsNotExist(err) {
		t.Fatal("uncommitted data entered the review")
	}
	// head/ is a real git worktree detached at the exact head SHA, owned by the
	// private object store rather than the developer's repository.
	head := filepath.Join(root, "input", "head")
	if got := reviewTestGit(t, head, "rev-parse", "HEAD"); got != e.Review.HeadSHA {
		t.Fatalf("worktree HEAD %s", got)
	}
	if branch := reviewTestGit(t, head, "branch", "--show-current"); branch != "" {
		t.Fatalf("worktree is on branch %q, want detached", branch)
	}
	if common := reviewTestGit(t, head, "rev-parse", "--path-format=absolute", "--git-common-dir"); !strings.HasSuffix(common, filepath.Join(filepath.Base(root), "objects.git")) {
		t.Fatalf("worktree metadata lives in %s", common)
	}
	if reviewTestGit(t, head, "status", "--porcelain") != "" {
		t.Fatal("fresh worktree is dirty")
	}
	if reviewTestGit(t, source, "status", "--porcelain") != status || reviewTestGit(t, source, "rev-parse", "HEAD") != e.Review.HeadSHA {
		t.Fatal("developer checkout changed")
	}
	if list := reviewTestGit(t, source, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("developer repository gained a worktree: %s", list)
	}
	if branches := reviewTestGit(t, source, "branch", "--list"); strings.Count(branches, "\n") != 0 {
		t.Fatalf("developer repository gained a branch: %s", branches)
	}
	e.Attempt.Round.Details.BaseRepositoryName = "another/repository"
	if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), e); err == nil {
		t.Fatal("foreign repository was accepted")
	}
}

func TestReviewClientBoundsRedirectsAndErrors(t *testing.T) {
	var leaked atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { leaked.Add(1) }))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer private-supervisor-key" {
			t.Error("missing scoped API auth")
		}
		switch r.URL.Path {
		case "/rest/v1/vibeflow/redirect":
			http.Redirect(w, r, foreign.URL, 302)
		case "/rest/v1/vibeflow/error":
			http.Error(w, "private-supervisor-key", 500)
		case "/rest/v1/vibeflow/huge":
			io.WriteString(w, strings.Repeat("x", (2<<20)+1))
		default:
			io.WriteString(w, `{"ok":true}`)
		}
	}))
	defer server.Close()
	client := NewClient(server.URL, "private-supervisor-key")
	for _, path := range []string{"/redirect", "/error", "/huge"} {
		var result any
		err := client.reviewRequest(context.Background(), "GET", path, nil, &result)
		if err == nil || strings.Contains(err.Error(), "private-supervisor-key") {
			t.Fatalf("unsafe error for %s: %v", path, err)
		}
	}
	if leaked.Load() != 0 {
		t.Fatal("redirect forwarded credentials")
	}
	var result map[string]bool
	if err := client.reviewRequest(context.Background(), "GET", "/ok", nil, &result); err != nil || !result["ok"] {
		t.Fatalf("request failed: %v", err)
	}
}

func TestReviewModelRelayOnlyForwardsBoundedInference(t *testing.T) {
	var calls atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.URL.Path != "/rest/v1/llm-gateway/v1/messages" || r.Header.Get("x-axiom-api-key") != "full-user-key" || r.Header.Get("Authorization") != "" {
			t.Error("incorrect upstream boundary")
		}
		var body map[string]any
		json.NewDecoder(r.Body).Decode(&body)
		if body["model"] != "selected/model" {
			t.Error("child changed selected model")
		}
		w.Header().Set("Content-Type", "text/event-stream")
		io.WriteString(w, "data: {\"ok\":true}\n\n")
	}))
	defer server.Close()
	base, key, closeRelay, err := startReviewRelay(context.Background(), NewClient(server.URL, "full-user-key"), "claude", "selected/model")
	if err != nil {
		t.Fatal(err)
	}
	defer closeRelay()
	if key == "full-user-key" || len(key) != 64 {
		t.Fatal("invalid delegated model token")
	}
	for _, path := range []string{"/v1/messages", "/rest/v1/vibeflow/mcp", "/v1/messages?destination=evil", "/v1/messages/count_tokens/../messages"} {
		req, _ := http.NewRequest("POST", base+path, strings.NewReader(`{"model":"child-model","messages":[]}`))
		req.Header.Set("X-Api-Key", key)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if path == "/v1/messages" {
			if resp.StatusCode != 200 || !strings.Contains(string(data), "ok") {
				t.Fatal("model request failed")
			}
		} else if resp.StatusCode == 200 {
			t.Fatalf("unapproved endpoint accepted: %s", path)
		}
		if strings.Contains(string(data), "full-user-key") {
			t.Fatal("supervisor credential leaked")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("unexpected upstream calls %d", calls.Load())
	}
	req, _ := http.NewRequest("POST", base+"/v1/messages", strings.NewReader(`{}`))
	req.Header.Set("X-Api-Key", "wrong")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != 403 {
		t.Fatal("unauthorized model request accepted")
	}
}

// This optional acceptance runs a real installed harness with the user's own
// login. It is explicit because it spends model usage, unlike unit tests.
func TestReviewInstalledProviderAcceptance(t *testing.T) {
	provider := os.Getenv("VIBEFLOW_REVIEW_PROVIDER_ACCEPTANCE")
	if provider == "" {
		t.Skip("set VIBEFLOW_REVIEW_PROVIDER_ACCEPTANCE=<harness key> to spend a bounded model call")
	}
	source, execution := reviewTestRepo(t)
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution); err != nil {
		t.Fatal(err)
	}
	digest := strings.Repeat("a", 64)
	execution.Prompt = fmt.Sprintf("Review this tiny change briefly. Use schema_version 1, head_sha %s, base_sha %s, brief_digest %s, empty reconciliations.", execution.Review.HeadSHA, execution.Review.BaseSHA, digest)
	cfg, err := LoadConfig(ConfigPath())
	if err != nil {
		cfg = DefaultConfig()
	}
	model := os.Getenv("VIBEFLOW_REVIEW_PROVIDER_MODEL")
	if err := preflightReviewProvider(context.Background(), cfg, provider, model); err != nil {
		t.Fatal(err)
	}
	spec, err := prepareReviewProvider(context.Background(), cfg, provider, model, root, execution, &reviewBrief{Digest: digest}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()
	cmd := exec.Command(spec.Binary, spec.Args...)
	cmd.Dir, cmd.Env = spec.Dir, spec.Env
	in, err := os.Open(spec.InputFile)
	if err != nil {
		t.Fatal(err)
	}
	defer in.Close()
	cmd.Stdin = in
	var output limitedReviewBuffer
	output.limit = 8 << 20
	cmd.Stdout, cmd.Stderr = &output, &output
	if err = runReviewProcess(ctx, cmd); err != nil {
		t.Fatalf("installed harness: %v\n%s", err, output.String())
	}
	data, err := os.ReadFile(filepath.Join(root, "result.json"))
	if err != nil {
		t.Fatalf("no result.json: %v\n%s", err, output.String())
	}
	var result struct {
		Result  json.RawMessage `json:"result"`
		Failure *string         `json:"failure_reason"`
	}
	if err := json.Unmarshal(data, &result); err != nil || result.Failure != nil || len(result.Result) == 0 || string(result.Result) == "null" {
		t.Fatalf("harness did not complete acceptance: %v %s", err, data)
	}
	if err := removeReviewDir(root); err != nil {
		t.Fatal(err)
	}
	t.Logf("Installed %s result: %s", provider, data)
}

func TestReviewModelRelayRejectsProviderSideToolsAndConversationReuse(t *testing.T) {
	for _, provider := range []string{"claude", "codex"} {
		t.Run(provider, func(t *testing.T) {
			calls := 0
			upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { calls++; io.WriteString(w, `{"ok":true}`) }))
			defer upstream.Close()
			base, key, closeRelay, err := startReviewRelay(context.Background(), NewClient(upstream.URL, "supervisor"), provider, "selected")
			if err != nil {
				t.Fatal(err)
			}
			defer closeRelay()
			endpoint := "/v1/messages"
			local := `{"model":"selected","tools":[{"name":"Read","input_schema":{"type":"object"}},{"type":"custom","name":"Grep","input_schema":{"type":"object"}}]}`
			if provider == "codex" {
				endpoint = "/v1/responses"
				local = `{"model":"selected","tools":[{"type":"function","name":"read","parameters":{"type":"object"}},{"type":"custom","name":"apply_patch"}]}`
			}
			bodies := []string{local, `{"tools":[{"type":"mcp","server_url":"https://untrusted.invalid"}]}`, `{"tools":[{"type":"web_search"}]}`, `{"tools":[{"type":"code_execution_20250825","name":"code_execution"}]}`, `{"mcp_servers":[{"url":"https://untrusted.invalid"}]}`, `{"previous_response_id":"foreign-conversation"}`, `{"conversation":"foreign"}`, `{"container":"foreign-container"}`}
			for i, body := range bodies {
				req, _ := http.NewRequest("POST", base+endpoint, strings.NewReader(body))
				req.Header.Set("Authorization", "Bearer "+key)
				response, err := http.DefaultClient.Do(req)
				if err != nil {
					t.Fatal(err)
				}
				response.Body.Close()
				if i == 0 && response.StatusCode != 200 {
					t.Fatalf("ordinary local tools rejected: %d", response.StatusCode)
				}
				if i > 0 && response.StatusCode < 400 {
					t.Fatalf("provider-side execution accepted: %s", body)
				}
			}
			if calls != 1 {
				t.Fatalf("unapproved requests reached provider: %d", calls)
			}
		})
	}
}

func TestReviewCheckoutDiffExcludesTargetOnlyCommits(t *testing.T) {
	source, execution := reviewTestRepo(t)
	ancestor := execution.Review.BaseSHA
	reviewTestGit(t, source, "checkout", "--detach", ancestor)
	if err := os.WriteFile(filepath.Join(source, "target-only.txt"), []byte("merged independently on target branch\n"), 0600); err != nil {
		t.Fatal(err)
	}
	reviewTestGit(t, source, "add", "target-only.txt")
	reviewTestGit(t, source, "commit", "-m", "advance target only")
	target := reviewTestGit(t, source, "rev-parse", "HEAD")
	execution.Review.BaseSHA = target
	execution.Attempt.Round.BaseSHA = target
	root := t.TempDir()
	if err := prepareReviewCheckout(context.Background(), source, root, execution); err != nil {
		t.Fatal(err)
	}
	diff, err := os.ReadFile(filepath.Join(root, "input", "review.diff"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(diff), "target-only.txt") {
		t.Fatal("PR delta incorrectly reports independent target commit as removed")
	}
	if _, err := os.Stat(filepath.Join(root, "input", "base", "target-only.txt")); err != nil {
		t.Fatal("target integration context lost", err)
	}
	var revisions struct {
		Target   string `json:"target_base_sha"`
		Head     string `json:"head_sha"`
		Ancestor string `json:"merge_base_sha"`
	}
	data, err := os.ReadFile(filepath.Join(root, "input", "revisions.json"))
	if err != nil {
		t.Fatal(err)
	}
	if json.Unmarshal(data, &revisions) != nil || revisions.Target != target || revisions.Head != execution.Review.HeadSHA || revisions.Ancestor != ancestor {
		t.Fatalf("incorrect revision evidence: %s", data)
	}
	if _, err = os.Stat(filepath.Join(root, "input", "merge-base", "target-only.txt")); !os.IsNotExist(err) {
		t.Fatal("target-only content entered review baseline")
	}
	if reviewTestGit(t, source, "rev-parse", "HEAD") != target {
		t.Fatal("developer checkout was changed")
	}
}

// The review child guard re-executes os.Executable. In this test artifact,
// route only that exact private CLI invocation through the real command.
func TestMain(m *testing.M) {
	if len(os.Args) == 3 && os.Args[1] == "review-child" {
		if err := Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	// LoadConfig lets these override the saved server and token, and spawned
	// CLI binaries inherit them, so a developer shell's real credentials would
	// replace fixture values. Tests that need them use t.Setenv.
	for _, key := range []string{"VIBEFLOW_URL", "VIBEFLOW_TOKEN"} {
		os.Unsetenv(key)
	}
	code := m.Run()
	if builtCLI.dir != "" {
		os.RemoveAll(builtCLI.dir)
	}
	os.Exit(code)
}

var builtCLI struct {
	once      sync.Once
	dir, path string
	err       error
}

// builtVibeflow builds the real CLI once per test run, for tests that run it
// as a separate process (inside tmux or a PTY).
func builtVibeflow(t *testing.T) string {
	t.Helper()
	builtCLI.once.Do(func() {
		if builtCLI.dir, builtCLI.err = os.MkdirTemp("", "vibeflow-cli-test-"); builtCLI.err != nil {
			return
		}
		builtCLI.path = filepath.Join(builtCLI.dir, "vibeflow")
		if out, err := exec.Command("go", "build", "-o", builtCLI.path, "../../cmd/vibeflow").CombinedOutput(); err != nil {
			builtCLI.err = fmt.Errorf("build: %v %s", err, out)
		}
	})
	if builtCLI.err != nil {
		t.Fatal(builtCLI.err)
	}
	return builtCLI.path
}
