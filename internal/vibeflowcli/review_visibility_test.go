package vibeflowcli

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewListCommandWithoutOrdinarySessions(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	var reads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer fixture-token" {
			t.Error("missing authorized read")
		}
		if r.URL.Path == "/rest/v1/vibeflow/projects/13/pr-review-sessions" {
			reads.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"sessions": []map[string]any{{"session_id": "review-visible", "project_id": 13, "job_id": "job-visible", "pr_number": 57, "repository_name": "acme/repo", "provider": "github", "head_sha": strings.Repeat("a", 40), "state": "reviewing", "round_number": 2, "attempt_number": 1}}, "next_after_id": "review-visible"})
			return
		}
		t.Errorf("unexpected %s", r.URL)
		w.WriteHeader(404)
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = server.URL
	cfg.APIToken = "fixture-token"
	path := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list", "--project", "13")
	out, err := cmd.CombinedOutput()
	if err != nil || reads.Load() != 1 || !strings.Contains(string(out), reviewSessionLabel) || !strings.Contains(string(out), "acme/repo#57") || !strings.Contains(string(out), "--reviews-after review-visible") {
		t.Fatalf("managed review invisible: %v reads=%d\n%s", err, reads.Load(), out)
	}
	cfg.DefaultProject = "13"
	if err := SaveConfig(cfg, path); err != nil {
		t.Fatal(err)
	}
	cmd = exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list")
	out, err = cmd.CombinedOutput()
	if err != nil || reads.Load() != 2 || !strings.Contains(string(out), reviewSessionLabel) {
		t.Fatalf("configured project ignored: %v reads=%d\n%s", err, reads.Load(), out)
	}
	cmd = exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list", "--project", "13")
	out, err = cmd.CombinedOutput()
	if err != nil || reads.Load() != 3 || !strings.Contains(string(out), reviewSessionLabel) {
		t.Fatalf("review history page not listed: %v reads=%d %s", err, reads.Load(), out)
	}

}

func TestReviewRegistrationRequiresExactScopeEcho(t *testing.T) {
	previous := rootDir
	t.Cleanup(func() { rootDir = previous })
	for _, tc := range []struct {
		name, provider string
		link           int64
	}{{"missing", "", 0}, {"wrong_provider", "bitbucket", 7}, {"wrong_repository", "github", 8}} {
		t.Run(tc.name, func(t *testing.T) {
			SetRootDir(t.TempDir())
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if strings.HasSuffix(r.URL.Path, "/pr-review-repositories") {
					w.Write([]byte(`{"repositories":[]}`)) // Capability probe.
					return
				}
				requests.Add(1)
				var in map[string]any
				json.NewDecoder(r.Body).Decode(&in)
				if in["provider"] != "github" || in["repository_link_id"] != float64(7) {
					t.Error("wrong outbound scope")
				}
				json.NewEncoder(w).Encode(map[string]any{"id": in["id"], "user_id": 1, "provider": tc.provider, "repository_link_id": tc.link})
			}))
			defer server.Close()
			watch := &reviewWatch{client: NewClient(server.URL, "fixture-token"), cfg: DefaultConfig(), options: reviewWatchOptions{ProjectID: 13, RepositoryLinkID: 7, GitProvider: "github", Kind: "local", Name: "scope-test", Once: true}, output: io.Discard}
			err := watch.run(context.Background())
			if err == nil || !strings.Contains(err.Error(), "repository scope") || requests.Load() != 1 {
				t.Fatalf("unconfirmed scope continued: %v requests=%d", err, requests.Load())
			}
		})
	}
}

func TestReviewSessionsRejectsForeignAndUnboundedProjection(t *testing.T) {
	for _, tc := range []struct {
		name string
		page reviewSessionsPage
	}{
		{"foreign_project", reviewSessionsPage{Sessions: []reviewSession{{SessionID: "foreign", ProjectID: 14}}}},
		{"duplicate", reviewSessionsPage{Sessions: []reviewSession{{SessionID: "same", ProjectID: 13}, {SessionID: "same", ProjectID: 13}}}},
		{"cursor_cycle", reviewSessionsPage{NextAfterID: "cursor"}},
		{"oversized", reviewSessionsPage{Sessions: make([]reviewSession, 26)}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { json.NewEncoder(w).Encode(tc.page) }))
			defer server.Close()
			if _, err := NewClient(server.URL, "fixture-token").listReviewSessions(context.Background(), 13, "cursor"); err == nil {
				t.Fatal("invalid projection accepted")
			}
		})
	}
}

func TestReviewWatchIntervalBounds(t *testing.T) {
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir = previousRoot; flagConfigPath = previousConfig })
	SetRootDir(t.TempDir())
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	for _, tc := range []struct {
		interval string
		ok       bool
	}{{"1s", true}, {"60s", true}, {"61s", false}, {"5m", false}} {
		t.Run(tc.interval, func(t *testing.T) {
			cmd := reviewWatchCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--project", "1", "--repository-link", "7", "--interval", tc.interval})
			err := cmd.ExecuteContext(context.Background())
			// Accepted intervals proceed to the later "connect VibeFlow" check.
			rejected := err != nil && strings.Contains(err.Error(), "interval must be between 1s and 60s")
			if rejected == tc.ok {
				t.Fatalf("interval %s ok=%v: %v", tc.interval, tc.ok, err)
			}
		})
	}
}

func TestReviewListCommandSurvivesReviewAPIFailure(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux unavailable")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "private provider error", http.StatusServiceUnavailable)
	}))
	defer server.Close()
	for _, tc := range []struct{ name, url, want string }{
		{"http_503", server.URL, "managed reviews unavailable: review API returned HTTP 503"},
		{"unreachable", "http://127.0.0.1:1", "managed reviews unavailable: review API connection failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			cfg := DefaultConfig()
			cfg.ServerURL = tc.url
			cfg.APIToken = "fixture-token"
			cfg.DefaultProject = "13"
			path := filepath.Join(t.TempDir(), "config.yaml")
			if err := SaveConfig(cfg, path); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--root", t.TempDir(), "--config", path, "--tmux-socket", fmt.Sprintf("review-list-%d", time.Now().UnixNano()), "list")
			var stdout, stderr strings.Builder
			cmd.Stdout, cmd.Stderr = &stdout, &stderr
			err := cmd.Run()
			if err != nil || !strings.Contains(stdout.String(), "sessions") || !strings.Contains(stderr.String(), tc.want) || strings.Contains(stderr.String(), "private") {
				t.Fatalf("review outage broke local listing: %v\nstdout: %s\nstderr: %s", err, stdout.String(), stderr.String())
			}
		})
	}
}
