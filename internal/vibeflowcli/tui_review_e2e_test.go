//go:build darwin || linux

package vibeflowcli

import (
	"bufio"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"slices"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"testing"
	"time"

	"github.com/charmbracelet/x/ansi"
)

// Exercise the installed command shape through a real PTY. A saved config must
// skip authentication setup without treating an earlier launch as consent.
func TestReviewTUIBinaryConsent(t *testing.T) {
	if _, err := exec.LookPath("script"); err != nil {
		t.Skip("native script command unavailable; real PTY is required")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build: %v %s", err, out)
	}
	repo, _ := reviewTestRepo(t)
	repo2, _ := reviewTestRepo(t)
	repo3, _ := reviewTestRepo(t)
	reviewTestGit(t, repo2, "remote", "set-url", "origin", "https://github.com/acme/two.git")
	reviewTestGit(t, repo3, "remote", "set-url", "origin", "https://github.com/acme/three.git")
	launchRepo, _ := reviewTestRepo(t)
	reviewTestGit(t, launchRepo, "remote", "set-url", "origin", "https://github.com/acme/cli.git")
	root, binDir := t.TempDir(), t.TempDir()
	// No sessions are needed for this scenario, and no user tmux server is used.
	if err := os.WriteFile(filepath.Join(binDir, "tmux"), []byte("#!/bin/sh\nexit 0\n"), 0700); err != nil {
		t.Fatal(err)
	}
	provider := filepath.Join(binDir, "claude")
	if err := os.WriteFile(provider, []byte("#!/bin/sh\n[ \"$ANTHROPIC_API_KEY\" = tui-model-canary ] || exit 9\nprintf '%s\\n' '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'\n"), 0700); err != nil {
		t.Fatal(err)
	}
	var registrations, heartbeats, stopped, projects, repositories, craReads atomic.Int64
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == "HEAD" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if strings.Contains(r.URL.Path, "pr-review") {
			craReads.Add(1)
		}
		if r.Header.Get("Authorization") != "Bearer tui-api-canary" {
			t.Error("TUI lost its configured API credential")
			http.Error(w, "invalid credential", http.StatusUnauthorized)
			return
		}
		switch {
		case r.Method == "GET" && r.URL.Path == "/rest/v1/vibeflow/projects":
			projects.Add(1)
			// A numeric project name must not shadow the configured project ID.
			fmt.Fprint(w, `[{"id":67,"name":"66"},{"id":66,"name":"Axiom"}]`)
		case r.Method == "GET" && r.URL.Path == "/rest/v1/vibeflow/projects/66/pr-review-repositories":
			repositories.Add(1)
			fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":7,"repository_name":"acme/repo"},{"provider":"github","provider_host":"github.com","repository_link_id":8,"repository_name":"acme/two"}]}`)
		case r.Method == "GET" && r.URL.Path == "/rest/v1/vibeflow/projects/67/pr-review-repositories":
			repositories.Add(1)
			fmt.Fprint(w, `{"repositories":[{"provider":"github","provider_host":"github.com","repository_link_id":9,"repository_name":"acme/three"},{"provider":"github","provider_host":"github.com","repository_link_id":10,"repository_name":"acme/missing"}]}`)
		case r.Method == "GET" && r.URL.Path == "/rest/v1/vibeflow/projects/66/sessions":
			fmt.Fprint(w, `[]`)
		case r.Method == "GET" && r.URL.Path == "/rest/v1/vibeflow/projects/66/pr-review-sessions":
			fmt.Fprint(w, `{"sessions":[]}`)
		case r.Method == "GET" && (r.URL.Path == "/rest/v1/vibeflow/projects/66/pr-review-summaries" || r.URL.Path == "/rest/v1/vibeflow/projects/67/pr-review-summaries"):
			fmt.Fprint(w, `{"summaries":[]}`)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
			var body map[string]any
			if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
				t.Error(err)
			}
			id, _ := body["repository_link_id"].(float64)
			if body["provider"] != "github" || (id != 7 && id != 8 && id != 9) || (id == 9 && r.URL.Path != "/rest/v1/vibeflow/projects/67/pr-review-runners") || (id != 9 && r.URL.Path != "/rest/v1/vibeflow/projects/66/pr-review-runners") {
				t.Error("runner did not match the local Git origin to its project repository")
			}
			body["user_id"] = 42
			registrations.Add(1)
			_ = json.NewEncoder(w).Encode(body)
		case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
			heartbeats.Add(1)
			w.WriteHeader(http.StatusNoContent)
		case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/work"):
			fmt.Fprint(w, `{"reviews":[]}`)
		case r.Method == "DELETE" && strings.Contains(r.URL.Path, "/pr-review-runners/"):
			stopped.Add(1)
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected request %s %s", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	cfg := DefaultConfig()
	cfg.ServerURL, cfg.APIToken, cfg.DefaultProject = server.URL, "tui-api-canary", "66"
	// Launching the CLI from its own checkout must still discover the project's
	// linked checkout from directory history when no default directory is saved.
	cfg.DefaultWorkDir, cfg.TmuxSocket = "", "review-tui-test"
	cfg.DirectoryHistory = []string{repo, repo2, repo3}
	cfg.Providers["claude"] = Provider{Binary: provider, Env: map[string]string{"ANTHROPIC_API_KEY": "tui-model-canary"}}
	configPath := filepath.Join(root, "config.yaml")
	if err := SaveConfig(cfg, configPath); err != nil {
		t.Fatal(err)
	}
	t.Run("preview_default_off", func(t *testing.T) {
		before := craReads.Load()
		terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir, "TEST_CRA_DISABLED=1")
		terminal.await(t, "q: quit")
		terminal.send(t, "Rr[]")
		terminal.send(t, "q")
		terminal.wait(t, false)
		if strings.Contains(terminal.output.String(), "Run PR reviews") || strings.Contains(terminal.output.String(), "PR review runners") || craReads.Load() != before || registrations.Load() != 0 {
			t.Fatal("default TUI accessed the CRA preview")
		}
		out, err := exec.Command(binary, "--root", root, "review-watch", "--status").CombinedOutput()
		if err == nil || !strings.Contains(string(out), "--cra") {
			t.Fatalf("public CRA command bypassed gate: %v %s", err, out)
		}
	})
	t.Run("animated_prompt_does_not_resolve_or_register", func(t *testing.T) {
		before, links, lookups := registrations.Load(), repositories.Load(), projects.Load()
		terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir, "CLICOLOR_FORCE=1")
		terminal.await(t, "Run PR reviews while this CLI is open?")
		terminal.await(t, "Run reviews")
		terminal.await(t, "Not now")
		initial := terminal.output.RawString()
		// Stay on the real consent screen through a complete blink interval.
		select {
		case <-terminal.done:
			t.Fatalf("TUI exited while awaiting consent: %v\n%s", terminal.err, terminal.output.String())
		case <-time.After(5500 * time.Millisecond):
		}
		output := terminal.output.RawString()
		if !regexp.MustCompile(`\x1b\[[0-9;]*48;(2|5);[0-9;]+m`).MatchString(output) {
			t.Fatal("color-enabled owl did not render colored terminal cells")
		}
		if len(output) <= len(initial) {
			t.Fatal("owl never animated while waiting for consent")
		}
		if registrations.Load() != before || repositories.Load() != links || projects.Load() != lookups {
			t.Fatal("idling on the animated prompt resolved or registered a runner before consent")
		}
		terminal.send(t, "\r")
		terminal.await(t, "q: quit")
		terminal.send(t, "q")
		terminal.wait(t, false)
		if registrations.Load() != before {
			t.Fatal("the animated prompt changed the default Not now choice")
		}
	})
	for _, choice := range []string{"\r", "n"} {
		t.Run(fmt.Sprintf("decline_%q", choice), func(t *testing.T) {
			terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir)
			terminal.await(t, "Run PR reviews while this CLI is open?")
			if strings.Contains(terminal.output.String(), "VibeFlow Server URL:") {
				t.Fatal("saved YAML did not bypass authentication setup")
			}
			terminal.send(t, choice)
			terminal.await(t, "q: quit")
			terminal.send(t, "q")
			terminal.wait(t, false)
			if registrations.Load() != 0 {
				t.Fatal("declining session consent registered a runner")
			}
		})
	}
	// A pre-existing runner has its own liveness pipe and must survive TUI exit.
	externalCmd := exec.Command(binary, "--root", root, "review-watch", "--owned-runner")
	externalInput, err := externalCmd.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	externalOutput, err := externalCmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err := externalCmd.Start(); err != nil {
		t.Fatal(err)
	}
	externalOptions := reviewStartupOptions(cfg, configPath, launchRepo)
	externalOptions.ProjectID, externalOptions.RepositoryLinkID, externalOptions.GitProvider, externalOptions.Repository, externalOptions.Name = 66, 7, "github", repo, "pre-existing"
	if err := json.NewEncoder(externalInput).Encode(reviewOwnedSpec{Config: cfg, Options: externalOptions}); err != nil {
		t.Fatal(err)
	}
	externalReader := bufio.NewReader(externalOutput)
	ready, err := externalReader.ReadString('\n')
	if err != nil || !strings.Contains(ready, `"ready":true`) {
		t.Fatalf("external startup: %s %v", ready, err)
	}
	externalDone := make(chan error, 1)
	go func() { _, _ = io.Copy(io.Discard, externalReader); externalDone <- externalCmd.Wait() }()
	t.Cleanup(func() {
		_ = externalInput.Close()
		select {
		case err := <-externalDone:
			if err != nil {
				t.Error(err)
			}
		case <-time.After(10 * time.Second):
			_ = externalCmd.Process.Kill()
			t.Error("external fixture failed to close")
		}
	})
	for _, killOwner := range []bool{false, true} {
		t.Run(fmt.Sprintf("accept_kill_%t", killOwner), func(t *testing.T) {
			before, beats, links, lookups, priorStops := registrations.Load(), heartbeats.Load(), repositories.Load(), projects.Load(), stopped.Load()
			terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir)
			terminal.await(t, "Run PR reviews while this CLI is open?")
			if registrations.Load() != before {
				t.Fatal("a previous Yes was reused before this session's consent")
			}
			terminal.send(t, "y")
			terminal.await(t, "3 online, 1")
			terminal.await(t, "q: quit")
			if registrations.Load() != before+3 || heartbeats.Load() <= beats || repositories.Load() <= links || projects.Load() <= lookups {
				t.Fatal("Yes did not resolve the local repository, register, and heartbeat")
			}
			terminal.send(t, "R")
			terminal.await(t, "acme/missing [needs_checkout]")
			terminal.send(t, "rr")
			refreshDeadline := time.Now().Add(5 * time.Second)
			for repositories.Load() < links+6 && time.Now().Before(refreshDeadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if repositories.Load() < links+6 {
				t.Fatal("explicit refresh did not rediscover both projects")
			}
			terminal.send(t, "R")
			if registrations.Load() != before+3 {
				t.Fatal("refresh duplicated registrations")
			}
			if killOwner {
				data, err := os.ReadFile(filepath.Join(root, "vibeflow.pid"))
				pid, parseErr := strconv.Atoi(strings.TrimSpace(string(data)))
				if err != nil || parseErr != nil || pid <= 0 {
					t.Fatal("missing isolated TUI process identity")
				}
				if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
					t.Fatal(err)
				}
			} else {
				terminal.send(t, "q")
			}
			terminal.wait(t, killOwner)
			deadline := time.Now().Add(8 * time.Second)
			for stopped.Load() != priorStops+3 && time.Now().Before(deadline) {
				time.Sleep(20 * time.Millisecond)
			}
			if stopped.Load() != priorStops+3 {
				t.Fatal("closing the TUI did not deregister its owned runner")
			}
			select {
			case err := <-externalDone:
				t.Fatalf("TUI stopped pre-existing runner: %v", err)
			default:
			}
		})
	}
	// A remembered repository/provider is reusable, but consent must still be No.
	t.Run("decline_saved_preferences", func(t *testing.T) {
		before := registrations.Load()
		terminal := startReviewTUITerminal(t, binary, launchRepo, root, binDir)
		terminal.await(t, "Run PR reviews while this CLI is open?")
		terminal.send(t, "\r")
		terminal.await(t, "q: quit")
		terminal.send(t, "q")
		terminal.wait(t, false)
		if registrations.Load() != before {
			t.Fatal("saved review preferences authorized another runner")
		}
	})
	t.Run("choose_second_known_checkout", func(t *testing.T) {
		secondRepo, _ := reviewTestRepo(t)
		pickerRoot := t.TempDir()
		pickerConfig := *cfg
		pickerConfig.DirectoryHistory = []string{repo, secondRepo}
		if err := SaveConfig(&pickerConfig, filepath.Join(pickerRoot, "config.yaml")); err != nil {
			t.Fatal(err)
		}
		before, beats, priorStops := registrations.Load(), heartbeats.Load(), stopped.Load()
		terminal := startReviewTUITerminal(t, binary, launchRepo, pickerRoot, binDir)
		terminal.await(t, "Run PR reviews while this CLI is open?")
		terminal.send(t, "y")
		terminal.await(t, "q: quit")
		terminal.send(t, "R")
		terminal.await(t, "acme/repo [needs_checkout]")
		terminal.send(t, "jj\r")
		terminal.await(t, "local checkout for")
		terminal.await(t, "Enter another path")
		if registrations.Load() != before {
			t.Fatal("multiple known checkouts registered a runner before selection")
		}
		terminal.send(t, "\x1b[B\r")
		terminal.await(t, "online]")
		if registrations.Load() != before+1 || heartbeats.Load() <= beats {
			t.Fatal("choosing the second checkout did not register and heartbeat")
		}
		data, err := os.ReadFile(filepath.Join(pickerRoot, "review-runner-preferences.json"))
		if err != nil {
			t.Fatal(err)
		}
		var preferences reviewStartupPreferences
		if err := json.Unmarshal(data, &preferences); err != nil {
			t.Fatal(err)
		}
		var selectedPath string
		for _, path := range preferences.Checkouts {
			selectedPath = path
		}
		selected, err := filepath.EvalSymlinks(selectedPath)
		if err != nil {
			t.Fatal(err)
		}
		want, err := filepath.EvalSymlinks(secondRepo)
		if err != nil || selected != want {
			t.Fatalf("checkout picker saved %q; want %q: %v", selected, want, err)
		}
		terminal.send(t, "Rq")
		terminal.wait(t, false)
		deadline := time.Now().Add(8 * time.Second)
		for stopped.Load() != priorStops+1 && time.Now().Before(deadline) {
			time.Sleep(20 * time.Millisecond)
		}
		if stopped.Load() != priorStops+1 {
			t.Fatal("closing the TUI did not stop the selected checkout's runner")
		}
	})
}

type reviewTUIOutput struct {
	sync.Mutex
	text strings.Builder
}

func (b *reviewTUIOutput) Write(p []byte) (int, error) {
	b.Lock()
	defer b.Unlock()
	return b.text.Write(p)
}

func (b *reviewTUIOutput) String() string {
	return ansi.Strip(b.RawString())
}

func (b *reviewTUIOutput) RawString() string {
	b.Lock()
	defer b.Unlock()
	return b.text.String()
}

type reviewTUITerminal struct {
	input  io.WriteCloser
	output reviewTUIOutput
	done   chan struct{}
	err    error
}

func startReviewTUITerminal(t *testing.T, binary, repo, root, binDir string, env ...string) *reviewTUITerminal {
	t.Helper()
	command := "stty rows 30 cols 100; exec " + shellQuote(binary) + " --root " + shellQuote(root)
	if !slices.Contains(env, "TEST_CRA_DISABLED=1") {
		command += " --cra"
	}
	args := []string{"-q", "/dev/null", "/bin/sh", "-c", command}
	if runtime.GOOS == "linux" {
		args = []string{"-q", "-e", "-c", command, "/dev/null"}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	cmd := exec.CommandContext(ctx, "script", args...)
	cmd.Dir = repo
	cmd.Env = []string{"PATH=" + binDir + ":/usr/bin:/bin", "HOME=" + t.TempDir(), "TERM=xterm-256color", "LANG=en_US.UTF-8"}
	if len(env) == 0 {
		env = []string{"NO_COLOR=1"}
	}
	cmd.Env = append(cmd.Env, env...)
	terminal := &reviewTUITerminal{done: make(chan struct{})}
	var err error
	terminal.input, err = cmd.StdinPipe()
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	cmd.Stdout, cmd.Stderr = &terminal.output, &terminal.output
	if err := cmd.Start(); err != nil {
		cancel()
		t.Fatal(err)
	}
	go func() {
		terminal.err = cmd.Wait()
		close(terminal.done)
	}()
	t.Cleanup(func() {
		defer cancel()
		defer terminal.input.Close()
		select {
		case <-terminal.done:
			return
		default:
		}
		_, _ = io.WriteString(terminal.input, "\x03")
		select {
		case <-terminal.done:
		case <-time.After(5 * time.Second):
			cancel()
			<-terminal.done
		}
	})
	return terminal
}

func (terminal *reviewTUITerminal) send(t *testing.T, input string) {
	t.Helper()
	if _, err := io.WriteString(terminal.input, input); err != nil {
		t.Fatal(err)
	}
}

func (terminal *reviewTUITerminal) await(t *testing.T, want string) {
	t.Helper()
	deadline := time.Now().Add(12 * time.Second)
	for time.Now().Before(deadline) {
		if strings.Contains(terminal.output.String(), want) {
			return
		}
		select {
		case <-terminal.done:
			t.Fatalf("TUI exited before %q: %v\n%s", want, terminal.err, terminal.output.String())
		case <-time.After(20 * time.Millisecond):
		}
	}
	t.Fatalf("TUI never displayed %q:\n%s", want, terminal.output.String())
}

func (terminal *reviewTUITerminal) wait(t *testing.T, killed bool) {
	t.Helper()
	select {
	case <-terminal.done:
		if terminal.err != nil && !killed {
			t.Fatalf("TUI quit: %v\n%s", terminal.err, terminal.output.String())
		}
	case <-time.After(8 * time.Second):
		t.Fatalf("TUI did not quit:\n%s", terminal.output.String())
	}
}
