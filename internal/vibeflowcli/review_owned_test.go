//go:build darwin || linux

package vibeflowcli

import (
	"bufio"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"
)

// An owned launch re-executes os.Executable. In this test artifact, route only
// that exact private CLI invocation through the real command implementation.
func TestMain(m *testing.M) {
	if (len(os.Args) == 5 && os.Args[1] == "--root" && os.Args[3] == "review-watch" && os.Args[4] == "--owned-runner") || (len(os.Args) == 3 && os.Args[1] == "review-child") {
		if err := Execute(); err != nil {
			os.Exit(1)
		}
		os.Exit(0)
	}
	os.Exit(m.Run())
}

func TestReviewOwnedOwnerProcess(t *testing.T) {
	if os.Getenv("REVIEW_OWNED_TEST_OWNER") != "1" {
		return
	}
	reader := bufio.NewReader(os.Stdin)
	line, err := reader.ReadBytes('\n')
	if err != nil {
		t.Fatal(err)
	}
	var fixture struct {
		Root     string
		Config   *Config
		Options  reviewWatchOptions
		Capacity *reviewCapacity
	}
	if json.Unmarshal(line, &fixture) != nil {
		t.Fatal("invalid fixture")
	}
	SetRootDir(fixture.Root)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	handle, err := startReviewOwnedWithCapacity(ctx, fixture.Config, "does-not-exist.yaml", fixture.Options, fixture.Capacity)
	if err != nil {
		t.Fatal(err)
	}
	fmt.Fprintln(os.Stdout, `{"ready":true}`)
	action, _ := reader.ReadString('\n')
	if strings.TrimSpace(action) == "cancel" {
		cancel()
		select {
		case <-handle.Done():
		case <-time.After(8 * time.Second):
			t.Fatal("context cancellation lost owned handle")
		}
		if err := handle.Err(); err != nil {
			t.Fatal(err)
		}
	} else if err := handle.Close(); err != nil {
		t.Fatal(err)
	}
	if err := handle.Close(); err != nil {
		t.Fatal("close was not idempotent", err)
	}
}

// The private process must wait for its owner, use pipe-delivered credentials,
// and leave another root's runner alive when its own owner disconnects.
func TestReviewOwnedBinaryLifetime(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\n[ \"$ANTHROPIC_API_KEY\" = owned-model-canary ] || exit 7\nprintf '%s\\n' '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var registrations, heartbeats, stopped atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer owned-api-canary" {
			t.Error("selected API credential lost")
			http.Error(w, "wrong credential", 401)
			return
		}
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			body["user_id"] = 42
			registrations.Add(1)
			json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			w.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(w, `{"reviews":[]}`)
		case r.Method == "DELETE":
			stopped.Add(1)
			w.WriteHeader(204)
		default:
			t.Errorf("unexpected request %s", r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "owned-api-canary"
	cfg.Providers["claude"] = Provider{Binary: provider, Env: map[string]string{"ANTHROPIC_API_KEY": "owned-model-canary"}}
	opts := reviewWatchOptions{Project: "1", ProjectID: 1, Repository: t.TempDir(), RepositoryLinkID: 7, GitProvider: "github", Provider: "claude", Kind: "local", Name: "owned-test", PollInterval: time.Second, Timeout: time.Minute}
	payload, _ := json.Marshal(map[string]any{"config": cfg, "options": opts})
	type child struct {
		input   io.WriteCloser
		done    chan error
		ready   chan bool
		failure chan string
	}
	start := func(root string, send bool) child {
		t.Helper()
		cmd := exec.Command(binary, "--root", root, "review-watch", "--owned-runner")
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "VIBEFLOW_URL=http://127.0.0.1:1", "VIBEFLOW_TOKEN=wrong-ambient-canary"}
		input, err := cmd.StdinPipe()
		if err != nil {
			t.Fatal(err)
		}
		stdout, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		c := child{input: input, done: make(chan error, 1), ready: make(chan bool, 1), failure: make(chan string, 1)}
		go func() {
			ready := false
			scanner := bufio.NewScanner(stdout)
			for scanner.Scan() {
				var event struct {
					Ready bool   `json:"ready"`
					Error string `json:"error"`
				}
				if json.Unmarshal(scanner.Bytes(), &event) == nil && event.Ready && !ready {
					ready = true
					c.ready <- true
				}
				if event.Error != "" {
					select {
					case c.failure <- event.Error:
					default:
					}
				}
			}
			if !ready {
				c.ready <- false
			}
			c.done <- cmd.Wait()
		}()
		t.Cleanup(func() {
			input.Close()
			select {
			case <-c.done:
			case <-time.After(8 * time.Second):
				cmd.Process.Kill()
			}
		})
		if send {
			if _, err := input.Write(append(payload, '\n')); err != nil {
				t.Fatal(err)
			}
		}
		return c
	}
	awaitReady := func(c child) {
		t.Helper()
		select {
		case ready := <-c.ready:
			if !ready {
				t.Fatal("owned runner exited before readiness")
			}
		case <-time.After(8 * time.Second):
			t.Fatal("owned runner did not become ready")
		}
	}
	awaitExit := func(c child) {
		t.Helper()
		select {
		case err := <-c.done:
			if err != nil {
				t.Fatalf("owned runner stop: %v", err)
			}
			c.done <- nil
		case <-time.After(8 * time.Second):
			t.Fatal("owner disconnect did not stop runner")
		}
	}
	root1, root2 := t.TempDir(), t.TempDir()
	first := start(root1, true)
	awaitReady(first)
	second := start(root2, true)
	awaitReady(second)
	if registrations.Load() != 2 {
		t.Fatal("independent roots did not register independently")
	}
	first.input.Close()
	awaitExit(first)
	before := heartbeats.Load()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && heartbeats.Load() == before {
		time.Sleep(20 * time.Millisecond)
	}
	if heartbeats.Load() == before || stopped.Load() != 1 {
		t.Fatal("closing one owner affected the other root")
	}
	second.input.Close()
	awaitExit(second)
	noHandshake := start(t.TempDir(), false)
	noHandshake.input.Close()
	select {
	case <-noHandshake.done:
		noHandshake.done <- nil
	case <-time.After(3 * time.Second):
		t.Fatal("owner death before setup left child blocked")
	}
	if registrations.Load() != 2 {
		t.Fatal("child registered without an owner payload")
	}
	for _, root := range []string{root1, root2} {
		filepath.WalkDir(root, func(path string, entry os.DirEntry, err error) error {
			if err != nil || entry.IsDir() {
				return err
			}
			data, err := os.ReadFile(path)
			if err != nil {
				t.Error(err)
				return nil
			}
			for _, secret := range []string{"owned-api-canary", "owned-model-canary", "wrong-ambient-canary"} {
				if strings.Contains(string(data), secret) {
					t.Errorf("credential persisted in %s", filepath.Base(path))
				}
			}
			return nil
		})
		if paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "background.json")); len(paths) != 0 {
			t.Fatal("TUI consent was persisted as detached state")
		}
	}
	// A pre-existing detached process never becomes owned by a new TUI.
	configPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveConfig(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	runDetached := func(args ...string) (string, error) {
		cmd := exec.Command(binary, append([]string{"--root", root1, "--config", configPath, "review-watch"}, args...)...)
		cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir()}
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	if out, err := runDetached("--background", "--project", "1", "--repository-link", "7", "--repo", opts.Repository, "--name", opts.Name, "--provider", "claude", "--interval", "1s"); err != nil {
		t.Fatalf("detached fixture: %v %s", err, out)
	}
	bindings, _ := filepath.Glob(filepath.Join(root1, "review-runners", "*", "background.json"))
	if len(bindings) != 1 {
		t.Fatal("missing detached fixture")
	}
	t.Cleanup(func() { runDetached("--stop", filepath.Base(filepath.Dir(bindings[0]))) })
	conflict := start(root1, true)
	select {
	case ok := <-conflict.ready:
		if ok {
			t.Fatal("owned launch adopted detached runner")
		}
	case <-time.After(5 * time.Second):
		t.Fatal("detached conflict did not settle")
	}
	select {
	case reason := <-conflict.failure:
		if reason != "busy" {
			t.Fatal("detached conflict lost safe reason")
		}
	default:
		t.Fatal("detached conflict did not report ownership failure")
	}
	conflict.input.Close()
	before = heartbeats.Load()
	deadline = time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) && heartbeats.Load() == before {
		time.Sleep(20 * time.Millisecond)
	}
	if heartbeats.Load() == before || registrations.Load() != 3 || stopped.Load() != 2 {
		t.Fatal("failed owned launch stopped or duplicated detached runner")
	}
}

func TestReviewOwnedParentDeathStopsActiveDescendants(t *testing.T) {
	for _, action := range []string{"close", "cancel", "kill", "relative-paths", "guard-kill"} {
		t.Run(action, func(t *testing.T) {
			repo, execution := reviewTestRepo(t)
			root := t.TempDir()
			capacity, err := newReviewCapacity(root, 1)
			if err != nil {
				t.Fatal(err)
			}
			defer capacity.Close()
			pidPath := filepath.Join(root, "provider.pids")
			provider := filepath.Join(t.TempDir(), "claude")
			script := "#!/bin/sh\n[ \"$ANTHROPIC_API_KEY\" = 'owned-model-$literal' ] || exit 7\nfor arg in \"$@\"; do if [ \"$arg\" = --help ]; then echo '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'; exit 0; fi; done\ntrap '' TERM INT\nsleep 60 &\nprintf '%s %s %s\\n' \"$$\" \"$!\" \"$PPID\" > " + shellQuote(pidPath) + "\nwait\n"
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var failures, unregistered atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer selected-api-canary" {
					t.Error("selected API credential replaced by ambient config")
					w.WriteHeader(401)
					return
				}
				switch {
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
					var body map[string]any
					json.NewDecoder(r.Body).Decode(&body)
					body["user_id"] = 42
					execution.Attempt.RunnerID = body["id"].(string)
					json.NewEncoder(w).Encode(body)
				case strings.HasSuffix(r.URL.Path, "/heartbeat"):
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/work"):
					json.NewEncoder(w).Encode(map[string]any{"reviews": []reviewJob{execution.Review}})
				case strings.HasSuffix(r.URL.Path, "/claim"), strings.HasSuffix(r.URL.Path, "/renew"):
					json.NewEncoder(w).Encode(execution)
				case strings.HasSuffix(r.URL.Path, "/brief"):
					digest := sha256.Sum256([]byte("{}"))
					json.NewEncoder(w).Encode(reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
				case strings.HasSuffix(r.URL.Path, "/fail"):
					failures.Add(1)
					w.WriteHeader(204)
				case r.Method == "DELETE":
					unregistered.Add(1)
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected API %s", r.URL.Path)
					w.WriteHeader(404)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken = server.URL, "selected-api-canary"
			cfg.Providers["claude"] = Provider{Binary: provider, Env: map[string]string{"ANTHROPIC_API_KEY": "${REVIEW_OWNED_MODEL_KEY}"}}
			opts := reviewWatchOptions{Project: "1", ProjectID: 1, Repository: repo, RepositoryLinkID: 7, GitProvider: "github", Provider: "claude", Kind: "local", Name: "owned-test", PollInterval: time.Second, Timeout: time.Minute}
			owner := exec.Command(os.Args[0], "-test.run=^TestReviewOwnedOwnerProcess$")
			owner.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + t.TempDir(), "REVIEW_OWNED_TEST_OWNER=1", "REVIEW_OWNED_MODEL_KEY=owned-model-$literal", "VIBEFLOW_TOKEN=wrong-ambient-canary", "VIBEFLOW_URL=http://127.0.0.1:1"}
			if action == "relative-paths" {
				launchDir := t.TempDir()
				git, err := exec.LookPath("git")
				if err != nil {
					t.Fatal(err)
				}
				wrapper := "#!/bin/sh\n"
				owner.Env = []string{"PATH=" + launchDir + ":" + os.Getenv("PATH"), "REVIEW_OWNED_TEST_OWNER=1", "REVIEW_OWNED_MODEL_KEY=owned-model-$literal"}
				for _, key := range []string{"HOME", "TMPDIR", "CODEX_HOME", "SSH_AUTH_SOCK"} {
					relative := strings.ToLower(key)
					absolute := filepath.Join(launchDir, relative)
					if err := os.MkdirAll(absolute, 0700); err != nil {
						t.Fatal(err)
					}
					owner.Env = append(owner.Env, key+"="+relative)
					// macOS may spell the launch directory /var or /private/var.
					// Require an absolute path to the same location, not one spelling.
					wrapper += "[ \"${" + key + "#/}\" != \"$" + key + "\" ] && [ \"$" + key + "\" -ef " + shellQuote(absolute) + " ] || { echo " + key + " > " + shellQuote(filepath.Join(root, "environment-check-failed")) + "; exit 77; }\n"
				}
				wrapper += "exec " + shellQuote(git) + " \"$@\"\n"
				if err := os.WriteFile(filepath.Join(launchDir, "git"), []byte(wrapper), 0700); err != nil {
					t.Fatal(err)
				}
				owner.Dir = launchDir
			}
			input, err := owner.StdinPipe()
			if err != nil {
				t.Fatal(err)
			}
			stdout, err := owner.StdoutPipe()
			if err != nil {
				t.Fatal(err)
			}
			if err := owner.Start(); err != nil {
				t.Fatal(err)
			}
			var pids []int
			t.Cleanup(func() {
				input.Close()
				owner.Process.Kill()
				owner.Wait()
				for _, pid := range pids {
					syscall.Kill(pid, syscall.SIGKILL)
				}
			})
			if err := json.NewEncoder(input).Encode(map[string]any{"Root": root, "Config": cfg, "Options": opts, "Capacity": capacity}); err != nil {
				t.Fatal(err)
			}
			ready := make(chan bool, 1)
			go func() {
				scanner := bufio.NewScanner(stdout)
				for scanner.Scan() {
					if scanner.Text() == `{"ready":true}` {
						ready <- true
						_, _ = io.Copy(io.Discard, stdout)
						return
					}
				}
				ready <- false
			}()
			select {
			case ok := <-ready:
				if !ok {
					t.Fatal("owner failed to start runner with selected current config")
				}
			case <-time.After(8 * time.Second):
				t.Fatal("owner startup stalled")
			}
			deadline := time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) {
				data, _ := os.ReadFile(pidPath)
				fields := strings.Fields(string(data))
				if len(fields) == 3 {
					for _, field := range fields {
						pid, _ := strconv.Atoi(field)
						pids = append(pids, pid)
					}
					break
				}
				time.Sleep(20 * time.Millisecond)
			}
			if len(pids) != 3 || pids[0] <= 0 || pids[1] <= 0 || pids[2] <= 0 {
				boundary, _ := os.ReadFile(filepath.Join(root, "environment-check-failed"))
				t.Fatalf("provider descendants did not start; environment boundary %s", boundary)
			}
			if slot, err := capacity.tryAcquire(); err != nil || slot != nil {
				if slot != nil {
					slot.Close()
				}
				t.Fatalf("admitted work before provider cleanup: %v", err)
			}
			runnerParent, _ := exec.Command("ps", "-o", "ppid=", "-p", strconv.Itoa(pids[2])).Output()
			runnerPID, _ := strconv.Atoi(strings.TrimSpace(string(runnerParent)))
			if action == "guard-kill" {
				if err := syscall.Kill(pids[2], syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
				fmt.Fprintln(input, "close")
				if err := owner.Wait(); err != nil {
					t.Fatal("owner could not stop quarantined runner", err)
				}
				if failures.Load() != 0 || unregistered.Load() != 0 {
					t.Fatal("unverified provider cleanup was finalized")
				}
				markers, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "work", "*", "provider-cleanup-pending.json"))
				if len(markers) != 1 {
					t.Fatal("guard death lost durable cleanup uncertainty")
				}
				if err := capacity.Close(); err == nil {
					t.Fatal("removed quarantined capacity group")
				}
				restarted, err := newReviewCapacity(root, 1)
				if err != nil {
					t.Fatal(err)
				}
				defer restarted.Close()
				if slot, err := restarted.tryAcquire(); err != nil || slot != nil {
					if slot != nil {
						slot.Close()
					}
					t.Fatal("guard death capacity silently restored", err)
				}
				return
			}
			if action == "kill" {
				if err := owner.Process.Kill(); err != nil {
					t.Fatal(err)
				}
			} else {
				fmt.Fprintln(input, action)
			}
			deadline = time.Now().Add(8 * time.Second)
			for time.Now().Before(deadline) && unregistered.Load() == 0 {
				time.Sleep(20 * time.Millisecond)
			}
			if unregistered.Load() != 1 || failures.Load() != 1 {
				for _, pid := range append(append([]int{}, pids...), owner.Process.Pid, runnerPID) {
					if pid <= 0 {
						continue
					}
					state, err := exec.Command("ps", "-o", "pid=,ppid=,pgid=,stat=,etime=,comm=", "-p", strconv.Itoa(pid)).CombinedOutput()
					t.Logf("owned fixture pid=%d signal0=%v process=%s psErr=%v", pid, syscall.Kill(pid, 0), state, err)
				}
				for _, pattern := range []string{"state.json", "runner.lock", "last-provider-diagnostic.json", "work/*/provider-cleanup-pending.json", "work/*/child-diagnostic.json", "work/*/child.lock"} {
					paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", pattern))
					for _, path := range paths {
						if strings.HasSuffix(path, ".lock") {
							lock, err := lockReviewFile(path)
							if lock != nil {
								lock.Close()
							}
							t.Logf("owned fixture lock %s: %v", path, err)
							continue
						}
						data, err := os.ReadFile(path)
						t.Logf("owned fixture metadata %s: %s readErr=%v", path, data, err)
					}
				}
				t.Fatalf("owner death did not cancel and preserve receipt: failures=%d unregister=%d", failures.Load(), unregistered.Load())
			}
			var survivors []int
			for _, pid := range pids {
				if syscall.Kill(pid, 0) == nil {
					t.Errorf("owned model descendant %d survived", pid)
					survivors = append(survivors, pid)
				}
			}
			pids = survivors
			work, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "work", "*"))
			if len(work) != 0 {
				t.Fatal("private model inputs were not cleaned")
			}
		})
	}
}

func TestReviewOwnedSparseEnvironmentDoesNotOverrideSelectedCredentials(t *testing.T) {
	provider := filepath.Join(t.TempDir(), "codex")
	script := "#!/bin/sh\n/usr/bin/grep -q selected-model-canary \"$CODEX_HOME/auth.json\" || exit 7\n[ \"$1\" = sandbox ] && exit 0\necho '--ephemeral --ignore-rules --strict-config --output-schema'\n"
	if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			body["user_id"] = 42
			json.NewEncoder(w).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(w, `{"reviews":[]}`)
		default:
			w.WriteHeader(204)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "owned-api-canary"
	cfg.Providers["codex"] = Provider{Binary: provider, Env: map[string]string{"OPENAI_API_KEY": "selected-model-canary"}}
	opts := reviewWatchOptions{Project: "1", ProjectID: 1, Repository: t.TempDir(), RepositoryLinkID: 7, GitProvider: "github", Provider: "codex", Kind: "local", Name: "sparse-env", PollInterval: time.Second, Timeout: time.Minute}
	payload, _ := json.Marshal(map[string]any{"Root": t.TempDir(), "Config": cfg, "Options": opts})
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	owner := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestReviewOwnedOwnerProcess$")
	owner.Env = []string{"REVIEW_OWNED_TEST_OWNER=1", "OPENAI_API_KEY=unselected-ambient-canary"}
	owner.Stdin = strings.NewReader(string(payload) + "\nclose\n")
	output, err := owner.CombinedOutput()
	if err != nil || !strings.Contains(string(output), `{"ready":true}`) {
		t.Fatalf("sparse launch inherited unselected model credentials: %v %s", err, output)
	}
}

func TestReviewOwnedCancellationWhileStartupHandleIsUnavailable(t *testing.T) {
	previousRoot := rootDir
	root := t.TempDir()
	SetRootDir(root)
	t.Cleanup(func() { rootDir = previousRoot })
	registered, cancelled := make(chan struct{}, 1), make(chan struct{}, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/pr-review-runners") {
			t.Errorf("unexpected request before registration: %s", r.URL.Path)
			w.WriteHeader(400)
			return
		}
		_, _ = io.Copy(io.Discard, r.Body)
		registered <- struct{}{}
		select {
		case <-r.Context().Done():
		case <-time.After(8 * time.Second):
			t.Error("startup API cancellation did not arrive")
		}
		cancelled <- struct{}{}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "owned-api-canary"
	cfg.Providers["claude"] = Provider{Binary: "/bin/sh"}
	opts := reviewWatchOptions{Project: "1", ProjectID: 1, Repository: t.TempDir(), RepositoryLinkID: 7, GitProvider: "github", Provider: "claude", Kind: "local", Name: "startup-cancel", PollInterval: time.Second, Timeout: time.Minute}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	result := make(chan error, 1)
	go func() {
		handle, err := startReviewOwned(ctx, cfg, "unused.yaml", opts)
		if handle != nil {
			_ = handle.Close()
			result <- fmt.Errorf("startup unexpectedly returned a live handle")
			return
		}
		result <- err
	}()
	select {
	case <-registered:
	case err := <-result:
		t.Fatalf("startup failed before registration: %v", err)
	case <-time.After(5 * time.Second):
		t.Fatal("owned registration did not start")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("cancelled startup: %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("startup cancellation leaked an unavailable handle")
	}
	select {
	case <-cancelled:
	case <-time.After(3 * time.Second):
		t.Fatal("owned API request survived startup cancellation")
	}
	locks, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "runner.lock"))
	if len(locks) != 1 {
		t.Fatal("missing runner lock fixture")
	}
	lock, err := lockReviewFile(locks[0])
	if err != nil {
		t.Fatal("cancelled startup retained runner ownership", err)
	}
	lock.Close()
}

func TestReviewOwnedCleanupNoticeClearsAfterSurvivingGuard(t *testing.T) {
	previousRoot := rootDir
	SetRootDir(t.TempDir())
	t.Cleanup(func() { rootDir = previousRoot })
	_, execution := reviewTestRepo(t)
	var beats, failures atomic.Int64
	var refuseHeartbeat atomic.Bool
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			body["user_id"] = 42
			json.NewEncoder(out).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			beats.Add(1)
			if refuseHeartbeat.Load() {
				out.WriteHeader(401)
				return
			}
			out.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/fail"):
			failures.Add(1)
			out.WriteHeader(204)
		case strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(out, `{"reviews":[]}`)
		case r.Method == "DELETE":
			out.WriteHeader(204)
		default:
			t.Errorf("unexpected recovery request %s", r.URL.Path)
			out.WriteHeader(400)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "token"
	// Capability check succeeds after recovery without launching inference.
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\necho '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["claude"] = Provider{Binary: provider}
	opts := reviewWatchOptions{ProjectID: 1, RepositoryLinkID: 7, GitProvider: "github", Kind: "local", Name: "notice", Repository: t.TempDir(), Provider: "claude", PollInterval: time.Second, Timeout: time.Minute}
	identity := fmt.Sprintf("%s\n1\n7\ngithub\nlocal\nnotice", server.URL)
	digest := sha256.Sum256([]byte(identity))
	dir := filepath.Join(RootDir(), "review-runners", hex.EncodeToString(digest[:16]))
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	capacity, err := newReviewCapacity(RootDir(), 1)
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	p := &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution}
	w := &reviewWatch{root: dir, capacity: capacity, state: reviewRunnerState{ID: execution.Attempt.RunnerID, OwnerID: 42, Pending: p}}
	if ok, err := w.acquireCapacity(); err != nil || !ok {
		t.Fatal(err)
	}
	work := w.workDir(p)
	if err := os.MkdirAll(work, 0700); err != nil {
		t.Fatal(err)
	}
	input := filepath.Join(work, "input")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", "sleep 60"}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: work, InputFile: input, DeadlineAt: time.Now().Add(time.Minute).UnixMilli(), CapacityFD: 3, Cleanup: &reviewProviderCleanup{Reservation: *p.Capacity, JobID: p.JobID, RequestID: p.RequestID, AttemptID: execution.Attempt.ID}}
	path := filepath.Join(work, "child.json")
	if err := saveReviewJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	guard := exec.Command(os.Args[0], "review-child", path)
	guard.ExtraFiles = []*os.File{w.slot}
	pipe, err := guard.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	defer pipe.Close()
	if err := guard.Start(); err != nil {
		t.Fatal(err)
	}
	defer func() { guard.Process.Kill(); guard.Wait() }()
	pipe.Write([]byte{'R'})
	w.releaseCapacity()
	marker := filepath.Join(work, "provider-cleanup-pending.json")
	until := time.Now().Add(5 * time.Second)
	for time.Now().Before(until) {
		if _, err := os.Stat(marker); err == nil {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	handle, err := startReviewOwnedWithCapacity(context.Background(), cfg, "unused", opts, capacity)
	if err != nil {
		t.Fatal(err)
	}
	defer handle.Close()
	until = time.Now().Add(5 * time.Second)
	for time.Now().Before(until) && handle.Status() == "" {
		time.Sleep(10 * time.Millisecond)
	}
	if !strings.Contains(handle.Status(), "Provider cleanup unverified: "+marker) {
		t.Fatal("missing private cleanup notice", handle.Status())
	}
	before := beats.Load()
	refuseHeartbeat.Store(true)
	until = time.Now().Add(4 * time.Second)
	for time.Now().Before(until) && beats.Load() < before+2 {
		time.Sleep(10 * time.Millisecond)
	}
	if beats.Load() < before+2 || !strings.Contains(handle.Status(), "Provider cleanup unverified: "+marker) || failures.Load() != 0 {
		t.Fatal("quarantine stopped heartbeats or cleared notice early")
	}
	refuseHeartbeat.Store(false)
	pipe.Close()
	guard.Wait()
	until = time.Now().Add(8 * time.Second)
	for time.Now().Before(until) && (handle.Status() != "" || failures.Load() != 1) {
		time.Sleep(10 * time.Millisecond)
	}
	if handle.Status() != "" || failures.Load() != 1 {
		t.Fatal("confirmed guard cleanup did not resume recovery", handle.Status())
	}
}

func TestReviewOwnedMarkerOnlyQuarantinesBinding(t *testing.T) {
	for _, status := range []int{401, 403} {
		t.Run(strconv.Itoa(status), func(t *testing.T) { testReviewOwnedMarkerOnlyQuarantinesBinding(t, status) })
	}
}

func testReviewOwnedMarkerOnlyQuarantinesBinding(t *testing.T, status int) {
	previousRoot := rootDir
	SetRootDir(t.TempDir())
	t.Cleanup(func() { rootDir = previousRoot })
	var names sync.Map
	var authStatus atomic.Int64
	heartbeats := make(chan int, 16)
	claimed := make(chan string, 4)
	server := httptest.NewServer(http.HandlerFunc(func(out http.ResponseWriter, r *http.Request) {
		switch {
		case strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			json.NewDecoder(r.Body).Decode(&body)
			names.Store(body["id"].(string), body["name"].(string))
			body["user_id"] = 42
			json.NewEncoder(out).Encode(body)
		case strings.HasSuffix(r.URL.Path, "/heartbeat"):
			name := ""
			names.Range(func(key, value any) bool {
				if strings.Contains(r.URL.Path, "/"+key.(string)+"/") {
					name = value.(string)
				}
				return true
			})
			code := 204
			if name == "unauthorized" {
				code = status
			}
			if denial := authStatus.Load(); name == "quarantined" && denial != 0 {
				code = int(denial)
			}
			out.WriteHeader(code)
			if name == "quarantined" {
				heartbeats <- code
			}
		case strings.HasSuffix(r.URL.Path, "/work"):
			repository := 7
			names.Range(func(key, value any) bool {
				if strings.Contains(r.URL.Path, "/"+key.(string)+"/") && value == "healthy" {
					repository = 8
				}
				return true
			})
			fmt.Fprintf(out, `{"reviews":[{"id":"job","repository_link_id":%d,"provider":"github"}]}`, repository)
		case strings.HasSuffix(r.URL.Path, "/claim"):
			names.Range(func(key, value any) bool {
				if strings.Contains(r.URL.Path, "/"+key.(string)+"/") {
					claimed <- value.(string)
				}
				return true
			})
			out.WriteHeader(409)
		default:
			out.WriteHeader(204)
		}
	}))
	defer server.Close()
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken = server.URL, "token"
	provider := filepath.Join(t.TempDir(), "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\necho '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["claude"] = Provider{Binary: provider}
	opts := reviewWatchOptions{ProjectID: 1, RepositoryLinkID: 7, GitProvider: "github", Kind: "local", Name: "quarantined", Repository: t.TempDir(), Provider: "claude", PollInterval: time.Second, Timeout: time.Minute}
	old, err := newReviewCapacity(RootDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer old.Close()
	request := reviewUUID()
	dir := filepath.Join(RootDir(), "review-runners", reviewBackgroundID(cfg.ServerURL, opts), "work", request)
	if err := os.MkdirAll(dir, 0700); err != nil {
		t.Fatal(err)
	}
	marker := filepath.Join(dir, "provider-cleanup-pending.json")
	if err := saveReviewJSON(marker, reviewProviderCleanup{Reservation: reviewReservation{Directory: old.Directory, Slot: "slot-0"}, RequestID: request, JobID: "old-job", AttemptID: "old-attempt"}); err != nil {
		t.Fatal(err)
	}
	old.owner.Close()
	capacity, err := newReviewCapacity(RootDir(), 2)
	if err != nil {
		t.Fatal(err)
	}
	defer capacity.Close()
	affected, err := startReviewOwnedWithCapacity(context.Background(), cfg, "unused", opts, capacity)
	if err != nil {
		t.Fatal(err)
	}
	defer affected.Close()
	deadline := time.NewTimer(4 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(10 * time.Millisecond)
	defer tick.Stop()
	for affected.Status() == "" {
		select {
		case name := <-claimed:
			t.Fatalf("marker-only binding claimed fresh work: %s", name)
		case <-deadline.C:
			t.Fatal("marker-only binding did not surface cleanup notice")
		case <-tick.C:
		}
	}
	if !strings.Contains(affected.Status(), "Provider cleanup unverified: "+marker) {
		t.Fatal(affected.Status())
	}
	opts.Name = "healthy"
	opts.RepositoryLinkID = 8
	healthy, err := startReviewOwnedWithCapacity(context.Background(), cfg, "unused", opts, capacity)
	if err != nil {
		t.Fatal(err)
	}
	defer healthy.Close()
	select {
	case name := <-claimed:
		if name != "healthy" {
			t.Fatal("quarantined binding claimed work", name)
		}
	case <-time.After(4 * time.Second):
		t.Fatal("quarantined binding blocked healthy remaining capacity")
	}
	if err := healthy.Close(); err != nil {
		t.Fatal(err)
	}
	awaitHeartbeat := func(want int) {
		t.Helper()
		// Race-instrumented subprocess shutdown can allow another idle poll;
		// the next legitimate exponential-backoff delay can therefore be 8s.
		timer := time.NewTimer(20 * time.Second)
		defer timer.Stop()
		for {
			select {
			case got := <-heartbeats:
				if got == want {
					return
				}
			case <-affected.Done():
				t.Fatalf("marker-only runner exited on heartbeat HTTP %d: %v", status, affected.Err())
			case name := <-claimed:
				if name == "quarantined" {
					t.Fatal("quarantined binding claimed fresh work")
				}
			case <-timer.C:
				t.Fatalf("runner did not continue heartbeat recovery after HTTP %d", status)
			}
		}
	}
	authStatus.Store(int64(status))
	awaitHeartbeat(status)
	awaitHeartbeat(status) // A second paced request proves the first denial was nonterminal.
	if !strings.Contains(affected.Status(), "Provider cleanup unverified: "+marker) {
		t.Fatal("authorization failure hid cleanup uncertainty", affected.Status())
	}
	authStatus.Store(0)
	awaitHeartbeat(204)
	select {
	case <-affected.Done():
		t.Fatal("restored authorization did not retain runner")
	default:
	}
	if !strings.Contains(affected.Status(), "Provider cleanup unverified: "+marker) {
		t.Fatal("authorization restoration falsely cleared cleanup uncertainty")
	}
	opts.Name = "unauthorized"
	denied, err := startReviewOwnedWithCapacity(context.Background(), cfg, "unused", opts, capacity)
	if denied != nil {
		denied.Close()
		t.Fatal("fresh unauthorized binding unexpectedly became ready")
	}
	if err == nil {
		t.Fatal("fresh unauthorized binding did not retain terminal behavior")
	}
	if _, err := os.Stat(marker); err != nil {
		t.Fatal("cleanup evidence lost", err)
	}
}
