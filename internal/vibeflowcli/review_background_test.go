//go:build darwin || linux

package vibeflowcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// This catches a background process tied to its launcher, lost configured
// credentials, duplicate registrations, stale PID status, and ambient rerouting.
func TestReviewBackgroundBinaryLifecycle(t *testing.T) {
	if runtime.GOOS != "darwin" && runtime.GOOS != "linux" {
		t.Skip("review isolation requires macOS or Linux")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	root := t.TempDir()
	configPath := filepath.Join(t.TempDir(), "runner.yaml")
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\n[ \"$ANTHROPIC_API_KEY\" = 'configured-model-canary' ] || exit 9\nprintf '%s\\n' '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var registrations, heartbeats, wrongOrigin atomic.Int64
	foreign := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "HEAD" {
			wrongOrigin.Add(1)
		}
		http.Error(w, "wrong origin", http.StatusForbidden)
	}))
	defer foreign.Close()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer configured-api-canary" {
			t.Error("runner did not use its pinned config credential")
			http.Error(w, "bad auth", http.StatusUnauthorized)
			return
		}
		switch {
		case r.URL.Path == "/rest/v1/vibeflow/projects/23/pr-review-runners":
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			registrations.Add(1)
			json.NewEncoder(w).Encode(map[string]any{"id": body["id"], "user_id": 42, "provider": "github", "repository_link_id": 7})
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			fmt.Fprint(w, `{}`)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(w, `{"reviews":[],"next_after_id":""}`)
		case r.Method == "DELETE":
			fmt.Fprint(w, `{}`)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL = foreign.URL // The explicit UAT-style override must be pinned.
	cfg.APIToken = "configured-api-canary"
	cfg.Providers["claude"] = Provider{Binary: provider, Env: map[string]string{"ANTHROPIC_API_KEY": "configured-model-canary"}}
	if err := SaveConfig(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	tmuxDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmuxDir, "tmux"), []byte("#!/bin/sh\nexit 1\n"), 0700); err != nil {
		t.Fatal(err)
	}
	launchTUI := func() {
		t.Helper()
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, "--root", ".", "--config", configPath)
		cmd.Dir = root
		cmd.Env = []string{"PATH=" + tmuxDir + ":" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "VIBEFLOW_URL=" + foreign.URL, "VIBEFLOW_TOKEN=ambient-token-canary", "ANTHROPIC_API_KEY=ambient-model-canary", "TERM=dumb"}
		// TUI startup attempts autostart before the expected non-terminal error.
		_, _ = cmd.CombinedOutput()
	}
	run := func(extraEnv []string, args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--root", ".", "--config", configPath, "review-watch"}, args...)...)
		cmd.Dir = root
		for _, entry := range os.Environ() {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.HasPrefix(key, "VIBEFLOW_") && !strings.HasPrefix(key, "ANTHROPIC_") && key != "CLAUDE_CODE_OAUTH_TOKEN" && key != "SSH_AUTH_SOCK" {
				cmd.Env = append(cmd.Env, entry)
			}
		}
		cmd.Env = append(cmd.Env, extraEnv...)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	start := []string{"--background", "--project", "23", "--repository-link", "7", "--repo", root, "--provider", "claude", "--name", "test-runner", "--interval", "1s"}
	if out, err := run(nil, "--background", "--project", "23", "--repository-link", "7"); err == nil || !strings.Contains(out, "explicit") {
		t.Fatalf("implicit repository was accepted: %v %s", err, out)
	}
	if out, err := run([]string{"VIBEFLOW_TOKEN=ambient-token-canary", "VIBEFLOW_URL=" + server.URL}, start...); err == nil || strings.Contains(out, "ambient-token-canary") {
		t.Fatalf("ambient account override was accepted or leaked: %v", err)
	}
	if dirs, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json")); len(dirs) != 0 {
		t.Fatal("rejected opt-in persisted a runner")
	}
	output, err := run([]string{"VIBEFLOW_URL=" + server.URL}, start...)
	if err != nil {
		t.Fatalf("background start failed: %v\n%s", err, output)
	}
	dirs, err := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json"))
	if err != nil || len(dirs) != 1 {
		t.Fatalf("expected one opt-in descriptor: %v %v", dirs, err)
	}
	id := filepath.Base(filepath.Dir(dirs[0]))
	t.Cleanup(func() { run(nil, "--stop", id) })
	managedPID := func() int {
		t.Helper()
		data, err := os.ReadFile(filepath.Join(filepath.Dir(dirs[0]), "background-status.json"))
		if err != nil {
			t.Fatal(err)
		}
		var process struct {
			PID int `json:"pid"`
		}
		if err := json.Unmarshal(data, &process); err != nil || process.PID <= 0 {
			t.Fatalf("missing managed process identity: %v", err)
		}
		return process.PID
	}
	wait := func(check func() bool, message string) {
		t.Helper()
		deadline := time.Now().Add(8 * time.Second)
		for time.Now().Before(deadline) {
			if check() {
				return
			}
			time.Sleep(50 * time.Millisecond)
		}
		t.Fatal(message)
	}
	wait(func() bool { return heartbeats.Load() >= 2 }, "runner did not survive launcher exit with configured model credentials")
	if args, err := exec.Command("ps", "-p", fmt.Sprint(managedPID()), "-o", "command=").Output(); err != nil {
		t.Fatal("could not inspect the test runner's command")
	} else {
		for _, secret := range []string{"configured-api-canary", "configured-model-canary", "ambient-token-canary"} {
			if strings.Contains(string(args), secret) {
				t.Fatal("credential entered managed process argv")
			}
		}
	}
	var wg sync.WaitGroup
	for i := 0; i < 3; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			run([]string{"VIBEFLOW_URL=" + server.URL}, start...)
		}()
	}
	wg.Wait()
	if registrations.Load() != 1 {
		t.Fatalf("duplicate runner registered: %d", registrations.Load())
	}
	changed := append(append([]string{}, start...), "--model", "other-model")
	if out, err := run([]string{"VIBEFLOW_URL=" + server.URL}, changed...); err == nil || !strings.Contains(out, "different binding") {
		t.Fatalf("active binding silently changed: %v %s", err, out)
	}
	status, err := run([]string{"VIBEFLOW_URL=" + foreign.URL, "VIBEFLOW_TOKEN=ambient-token-canary", "ANTHROPIC_API_KEY=ambient-model-canary"}, "--status")
	if err != nil || !strings.Contains(status, "running") || !strings.Contains(status, id) || !strings.Contains(status, server.URL) || !strings.Contains(status, "test-runner") {
		t.Fatalf("live status: %v %s", err, status)
	}
	// Graceful process termination keeps the prior opt-in; a subsequent TUI
	// launch reloads that binding despite unrelated shell credentials/origin.
	if err := syscall.Kill(managedPID(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { out, _ := run(nil, "--status"); return strings.Contains(out, "stopped") }, "graceful process exit did not stop")
	launchTUI()
	wait(func() bool { return registrations.Load() == 2 }, "TUI did not start the previously opted-in runner")
	wait(func() bool { out, _ := run(nil, "--status"); return strings.Contains(out, "running") }, "TUI launch used ambient model credentials")
	for i := 0; i < 2; i++ {
		if out, err := run(nil, "--stop", id); err != nil {
			t.Fatalf("idempotent stop: %v %s", err, out)
		}
	}
	status, err = run(nil, "--status")
	if err != nil || strings.Contains(status, "running") || !strings.Contains(status, "disabled") {
		t.Fatalf("stopped status: %v %s", err, status)
	}
	launchTUI()
	if registrations.Load() != 2 {
		t.Fatal("TUI restarted a disabled runner")
	}
	if out, err := run([]string{"VIBEFLOW_URL=" + server.URL}, start...); err != nil {
		t.Fatalf("restart: %v %s", err, out)
	}
	wait(func() bool { return registrations.Load() == 3 }, "restart did not register")
	// Kill only the process started by this test, then leave stale metadata.
	if err := syscall.Kill(managedPID(), syscall.SIGKILL); err != nil {
		t.Fatal(err)
	}
	wait(func() bool {
		out, err := run(nil, "--status")
		return err == nil && strings.Contains(out, "stale") && !strings.Contains(out, "running")
	}, "stale process was reported running")
	launchTUI()
	if registrations.Load() != 3 {
		t.Fatal("TUI respawned a stale runner")
	}
	// Changed file credentials cannot rebind the existing opt-in on TUI launch.
	if out, err := run([]string{"VIBEFLOW_URL=" + server.URL}, start...); err != nil {
		t.Fatalf("explicit stale restart: %v %s", err, out)
	}
	if err := syscall.Kill(managedPID(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	wait(func() bool { out, _ := run(nil, "--status"); return strings.Contains(out, "stopped") }, "second graceful exit did not stop")
	cfg.APIToken = "rotated-api-canary"
	if err := SaveConfig(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	launchTUI()
	status, _ = run(nil, "--status")
	if !strings.Contains(status, "failed") || strings.Contains(status, "running") || registrations.Load() != 4 {
		t.Fatalf("changed account was silently used: %s", status)
	}
	launchTUI()
	if registrations.Load() != 4 {
		t.Fatal("TUI respawned a failed runner")
	}
	statusPath := filepath.Join(filepath.Dir(dirs[0]), "background-status.json")
	for _, invalid := range []string{"", "not-json", `{}`, `{"phase":"unknown"}`} {
		if invalid == "" {
			if err := os.Remove(statusPath); err != nil {
				t.Fatal(err)
			}
		} else if err := os.WriteFile(statusPath, []byte(invalid), 0600); err != nil {
			t.Fatal(err)
		}
		launchTUI()
		data, err := os.ReadFile(statusPath)
		if (invalid == "" && !os.IsNotExist(err)) || (invalid != "" && string(data) != invalid) {
			t.Fatal("TUI respawned a runner with missing or invalid process status")
		}
		status, _ = run(nil, "--status")
		if !strings.Contains(status, "stale") {
			t.Fatalf("unknown process status was not stale: %s", status)
		}
	}
	// Explicit stop changes only management state, even with a pending receipt.
	statePath := filepath.Join(filepath.Dir(dirs[0]), "state.json")
	pending := []byte(`{"id":"test-pending","pending":{"job_id":"keep-this-receipt","request_id":"keep-this-request","completed":false}}`)
	if err := os.WriteFile(statePath, pending, 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := run(nil, "--stop", id); err != nil {
		t.Fatalf("stop failed runner: %v %s", err, out)
	}
	if data, _ := os.ReadFile(statePath); string(data) != string(pending) {
		t.Fatal("stop changed the pending receipt")
	}
	// An existing foreground runner's exact URL spelling owns its lock/receipts.
	cfg.ServerURL, cfg.APIToken = server.URL+"/", "configured-api-canary"
	if err := SaveConfig(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	foreground := exec.Command(binary, append([]string{"--root", root, "--config", configPath, "review-watch"}, append(start[1:], "--name", "trailing-runner")...)...)
	foreground.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
	if err := foreground.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = foreground.Process.Signal(syscall.SIGTERM)
		_ = foreground.Wait()
		paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json"))
		for _, path := range paths {
			run(nil, "--stop", filepath.Base(filepath.Dir(path)))
		}
	})
	wait(func() bool { return registrations.Load() == 5 }, "foreground trailing-slash fixture did not register")
	if out, err := run(nil, append(start, "--name", "trailing-runner")...); err == nil || !strings.Contains(out, "foreground") {
		t.Fatalf("background bypassed foreground trailing-slash lock: %v %s", err, out)
	}
	raceStart := append(append([]string{}, start...), "--name", "race-runner")
	if out, err := run(nil, raceStart...); err != nil {
		t.Fatalf("concurrency fixture: %v %s", err, out)
	}
	var raceID string
	paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json"))
	for _, path := range paths {
		var saved struct{ Options struct{ Name string } }
		data, _ := os.ReadFile(path)
		if json.Unmarshal(data, &saved) == nil && saved.Options.Name == "race-runner" {
			raceID = filepath.Base(filepath.Dir(path))
		}
	}
	if raceID == "" {
		t.Fatal("missing concurrent control fixture")
	}
	beforeRace := registrations.Load()
	errors := make(chan error, 2)
	go func() { _, err := run(nil, "--stop", raceID); errors <- err }()
	go func() { _, err := run(nil, raceStart...); errors <- err }()
	for i := 0; i < 2; i++ {
		if err := <-errors; err != nil {
			t.Fatalf("concurrent start/stop: %v", err)
		}
	}
	if registrations.Load() > beforeRace+1 {
		t.Fatal("concurrent start/stop created duplicate registrations")
	}
	if out, err := run(nil, "--stop", raceID); err != nil {
		t.Fatalf("stop concurrent fixture: %v %s", err, out)
	}
	status, _ = run(nil, "--status")
	if strings.Contains(status, "running") || strings.Contains(status, "stopping") {
		t.Fatalf("concurrent control left a managed runner alive: %s", status)
	}
	if wrongOrigin.Load() != 0 {
		t.Fatalf("runner silently changed origin: %d requests", wrongOrigin.Load())
	}
	filepath.WalkDir(filepath.Join(root, "review-runners"), func(path string, entry os.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Error(err)
			return nil
		}
		for _, secret := range []string{"configured-api-canary", "configured-model-canary", "ambient-token-canary", "ambient-model-canary", "rotated-api-canary"} {
			if strings.Contains(string(data), secret) {
				t.Errorf("credential leaked into %s", filepath.Base(path))
			}
		}
		return nil
	})
}

// Missing PR commits must still be fetched through the opted-in SSH agent,
// while the isolated model process must never inherit that agent socket.
func TestReviewBackgroundFetchUsesPinnedSSHAgent(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	upstream, execution := reviewTestRepo(t)
	source := t.TempDir()
	reviewTestGit(t, source, "init")
	reviewTestGit(t, source, "fetch", upstream, execution.Review.BaseSHA)
	reviewTestGit(t, source, "remote", "add", "origin", "git@github.com:acme/repo.git")
	if err := exec.Command("git", "-C", source, "cat-file", "-e", execution.Review.HeadSHA+"^{commit}").Run(); err == nil {
		t.Fatal("fixture already has the missing head")
	}
	socketDir, err := os.MkdirTemp("/tmp", "review-agent-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(socketDir) })
	socketPath := filepath.Join(socketDir, "agent.sock")
	agent, err := net.Listen("unix", socketPath)
	if err != nil {
		t.Fatal(err)
	}
	defer agent.Close()
	binDir := t.TempDir()
	ssh := "#!/bin/sh\n[ \"$SSH_AUTH_SOCK\" = " + shellQuote(socketPath) + " ] && [ -S \"$SSH_AUTH_SOCK\" ] || exit 91\ncase \"$*\" in\n *-G*) exit 0 ;;\n *\"git@github.com git-upload-pack '/acme/repo.git'\") exec git-upload-pack " + shellQuote(upstream) + " ;;\n *) exit 92 ;;\nesac\n"
	if err := os.WriteFile(filepath.Join(binDir, "ssh"), []byte(ssh), 0700); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(binDir, "claude")
	script := "#!/bin/sh\n[ -z \"$SSH_AUTH_SOCK\" ] || exit 93\nfor arg in \"$@\"; do if [ \"$arg\" = --help ]; then echo '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'; exit 0; fi; done\nexit 17\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var submitted atomic.Bool
	var reason atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			execution.Attempt.RunnerID = body["id"].(string)
			body["user_id"] = 42
			json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/work"):
			if submitted.Load() {
				fmt.Fprint(w, `{"reviews":[]}`)
			} else {
				json.NewEncoder(w).Encode(map[string]any{"reviews": []reviewJob{execution.Review}})
			}
		case strings.HasSuffix(r.URL.Path, "/claim"):
			json.NewEncoder(w).Encode(execution)
		case strings.HasSuffix(r.URL.Path, "/brief"):
			digest := sha256.Sum256([]byte("{}"))
			json.NewEncoder(w).Encode(reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
		case strings.HasSuffix(r.URL.Path, "/fail"):
			var body map[string]string
			json.NewDecoder(r.Body).Decode(&body)
			reason.Store(body["reason"])
			submitted.Store(true)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"), r.Method == "DELETE":
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected API path %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	root, modelHome := t.TempDir(), t.TempDir()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "configured-api-canary"
	cfg.Providers["claude"] = Provider{Binary: provider}
	if err := SaveConfig(cfg, filepath.Join(root, "config.yaml")); err != nil {
		t.Fatal(err)
	}
	run := func(args ...string) (string, error) {
		ctx, cancel := context.WithTimeout(context.Background(), 25*time.Second)
		defer cancel()
		cmd := exec.CommandContext(ctx, binary, append([]string{"--root", root, "review-watch"}, args...)...)
		cmd.Env = []string{"HOME=" + modelHome, "PATH=" + binDir + ":" + os.Getenv("PATH"), "SSH_AUTH_SOCK=" + socketPath}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() {
		paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json"))
		for _, path := range paths {
			run("--stop", filepath.Base(filepath.Dir(path)))
		}
	})
	if out, err := run("--background", "--project", "1", "--repository-link", "7", "--repo", source, "--provider", "claude", "--name", "ssh-runner", "--interval", "1s"); err != nil {
		t.Fatalf("background SSH runner start: %v %s", err, out)
	}
	deadline := time.Now().Add(8 * time.Second)
	for !submitted.Load() && time.Now().Before(deadline) {
		time.Sleep(50 * time.Millisecond)
	}
	if !submitted.Load() || !strings.Contains(fmt.Sprint(reason.Load()), "provider_exit") || !strings.Contains(fmt.Sprint(reason.Load()), "exit 17") {
		t.Fatalf("SSH fetch or model isolation failed: %v", reason.Load())
	}
	if err := exec.Command("git", "-C", source, "cat-file", "-e", execution.Review.HeadSHA+"^{commit}").Run(); err == nil {
		t.Fatal("background fetch changed the developer checkout")
	}
	status, err := run("--status")
	if err != nil || !strings.Contains(status, "last failed attempt") || !strings.Contains(status, "provider_exit") {
		t.Fatalf("safe last attempt diagnostic missing from status: %v %s", err, status)
	}
	diagnostics, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "last-provider-diagnostic.json"))
	if len(diagnostics) != 1 {
		t.Fatal("missing execution diagnostic fixture")
	}
	data, err := os.ReadFile(diagnostics[0])
	if err != nil {
		t.Fatal(err)
	}
	var poisoned map[string]any
	if json.Unmarshal(data, &poisoned) != nil {
		t.Fatal("invalid execution diagnostic fixture")
	}
	poisoned["category"] = "provider-output-credential-canary"
	data, _ = json.Marshal(poisoned)
	if err := os.WriteFile(diagnostics[0], data, 0600); err != nil {
		t.Fatal(err)
	}
	status, err = run("--status")
	if err != nil || strings.Contains(status, "provider-output-credential-canary") || strings.Contains(status, "last failed attempt") {
		t.Fatalf("poisoned diagnostic appeared in status: %v", err)
	}
}
