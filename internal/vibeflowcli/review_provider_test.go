package vibeflowcli

import (
	"context"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

func TestReviewCodexPreflightRejectsReadableTemporaryFiles(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native review platform")
	}
	if _, err := exec.LookPath("codex"); err != nil {
		t.Skip("Codex not installed")
	}
	root := t.TempDir()
	if err := os.Mkdir(filepath.Join(root, "input"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Providers["codex"] = Provider{Binary: "codex", Env: map[string]string{"OPENAI_API_KEY": "unused-model-only-key"}}
	spec, err := prepareReviewProvider(context.Background(), cfg, "codex", "", root, &reviewExecution{Prompt: "No model invocation."}, &reviewBrief{}, "", "")
	if err != nil {
		t.Fatal(err)
	}
	// macOS's usual per-user TempDir is denied correctly. The shared /tmp
	// directory reproduced a separate read allowance in actual Codex exec.
	outside, err := os.CreateTemp("/tmp", "vibeflow-review-outside-test-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(outside.Name()) })
	const canary = "harmless-outside-review-input\n"
	if _, err := outside.WriteString(canary); err != nil {
		outside.Close()
		t.Fatal(err)
	}
	if err := outside.Close(); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, spec.Binary, "sandbox", "-P", "review", "-C", spec.Dir, "--", "/bin/cat", outside.Name())
	cmd.Env = spec.Env
	cmd.Dir = spec.Dir
	data, err := cmd.CombinedOutput()
	if err != nil && !strings.Contains(string(data), canary) {
		t.Skip("installed Codex denies the shared temporary-file read")
	}
	if err != nil || string(data) != canary {
		t.Fatalf("unexpected installed sandbox response: %v %q", err, data)
	}

	for _, pending := range []*reviewReceipt{nil, {JobID: reviewUUID(), RequestID: reviewUUID()}} {
		name := "fresh_discovery"
		if pending != nil {
			name = "unclaimed_receipt"
		}
		t.Run(name, func(t *testing.T) {
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "review API must not be reached before capability rejection", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			watch := &reviewWatch{client: NewClient(server.URL, "supervisor-only-token"), cfg: cfg,
				root: t.TempDir(), state: reviewRunnerState{ID: reviewUUID(), Pending: pending},
				options: reviewWatchOptions{ProjectID: 1, Provider: "codex"}, output: io.Discard}
			err := watch.poll(ctx)
			if err == nil || !strings.Contains(err.Error(), "cannot enforce source-only review reads") {
				t.Fatalf("unsafe runtime was not rejected by capability preflight: %v", err)
			}
			if requests.Load() != 0 || watch.providerReady || watch.state.Pending != pending {
				t.Fatalf("unsafe runtime reached review discovery/claim or changed its receipt: calls=%d ready=%v", requests.Load(), watch.providerReady)
			}
		})
	}
}

func TestReviewCodexPreflightRefusesChatGPTLogin(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("native review platform")
	}
	dir := t.TempDir()
	fake, marker := filepath.Join(dir, "codex"), filepath.Join(dir, "invoked")
	if err := os.WriteFile(fake, []byte("#!/bin/sh\ntouch "+marker+"\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	codexHome := filepath.Join(dir, "codex-home")
	if err := os.Mkdir(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(codexHome, "auth.json"), []byte(`{"auth_mode":"chatgpt","tokens":{"access_token":"a","refresh_token":"r","id_token":"i"},"last_refresh":"2026-01-01T00:00:00Z"}`), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", codexHome)
	t.Setenv("OPENAI_API_KEY", "")
	for _, tc := range []struct {
		name string
		env  map[string]string
		want string
	}{
		{"chatgpt_login", nil, "Codex subscription (ChatGPT login) credentials are not supported for review-watch"},
		{"api_key", map[string]string{"OPENAI_API_KEY": "model-only-key"}, "capability check failed"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			os.Remove(marker)
			cfg := DefaultConfig()
			cfg.Providers["codex"] = Provider{Binary: fake, Env: tc.env}
			var requests atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				requests.Add(1)
				http.Error(w, "review API must not be reached before credential rejection", http.StatusServiceUnavailable)
			}))
			defer server.Close()
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			watch := &reviewWatch{client: NewClient(server.URL, "supervisor-only-token"), cfg: cfg, root: t.TempDir(), state: reviewRunnerState{ID: reviewUUID()}, options: reviewWatchOptions{ProjectID: 1, Provider: "codex"}, output: io.Discard}
			err := watch.poll(ctx)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("unexpected preflight result: %v", err)
			}
			_, statErr := os.Stat(marker)
			if launched := statErr == nil; launched != (tc.env != nil) {
				t.Fatalf("model CLI launched=%v before/after credential check", launched)
			}
			if requests.Load() != 0 || watch.providerReady {
				t.Fatalf("preflight failure reached discovery/claim: calls=%d ready=%v", requests.Load(), watch.providerReady)
			}
		})
	}
}
