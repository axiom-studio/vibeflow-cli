/*
 * Copyright (c) 2026. AXIOM STUDIO AI Inc.
 *
 * Licensed under the Apache License, Version 2.0 (the "License");
 * you may not use this file except in compliance with the License.
 * You may obtain a copy of the License at
 *
 *     http://www.apache.org/licenses/LICENSE-2.0
 *
 * Unless required by applicable law or agreed to in writing, software
 * distributed under the License is distributed on an "AS IS" BASIS,
 * WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
 * See the License for the specific language governing permissions and
 * limitations under the License.
 */

package vibeflowcli

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

func TestWithSessionIdentity(t *testing.T) {
	const base = `Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.`

	t.Run("all values are listed in a fixed order", func(t *testing.T) {
		got := WithSessionIdentity(base, SessionIdentity{
			SessionID: "session-1", AgentType: "qwen", AgentModel: "m1",
			GitBranch: "main", GitRemoteURL: "git@github.com:org/repo.git", WorkingDir: "/w",
		})
		want := base + ` Register with exactly these values (session_init and session_register); do not infer or change them: ` +
			`session_id="session-1", agent_type="qwen", agent_model="m1", git_branch="main", git_remote_url="git@github.com:org/repo.git", working_directory="/w".`
		if got != want {
			t.Errorf("got:\n%s\nwant:\n%s", got, want)
		}
	})
	t.Run("unknown values are omitted", func(t *testing.T) {
		got := WithSessionIdentity(base, SessionIdentity{SessionID: "session-1", AgentType: "claude"})
		if strings.Contains(got, "agent_model") || strings.Contains(got, "git_remote_url") {
			t.Errorf("empty fields must be omitted: %s", got)
		}
	})
	t.Run("no values leaves the prompt unchanged", func(t *testing.T) {
		if got := WithSessionIdentity(base, SessionIdentity{}); got != base {
			t.Errorf("got %q, want the prompt unchanged", got)
		}
	})
	t.Run("the original instruction is kept verbatim", func(t *testing.T) {
		if got := WithSessionIdentity(base, SessionIdentity{SessionID: "s"}); !strings.HasPrefix(got, base) {
			t.Errorf("prompt must start with the original instruction: %s", got)
		}
	})
}

func TestGetGitRemoteURL(t *testing.T) {
	repo := newTestRepo(t, "")
	if got := GetGitRemoteURL(repo); got != "" {
		t.Errorf("repo without origin: got %q, want empty", got)
	}
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "git@github.com:org/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, out)
	}
	if got := GetGitRemoteURL(repo); got != "git@github.com:org/repo.git" {
		t.Errorf("got %q, want the origin URL", got)
	}
	if got := GetGitRemoteURL(""); got != "" {
		t.Errorf("empty dir must return empty, got %q", got)
	}
	if got := GetGitRemoteURL(t.TempDir()); got != "" {
		t.Errorf("non-repo dir: got %q, want empty", got)
	}
}

func TestGetGitRemoteURL_StripsCredentials(t *testing.T) {
	repo := newTestRepo(t, "")
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "https://x-access-token:ghp_FAKE123@github.com/org/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, out)
	}
	if got := GetGitRemoteURL(repo); got != "https://github.com/org/repo.git" {
		t.Errorf("got %q, want the URL without credentials", got)
	}
	for in, want := range map[string]string{
		"git@github.com:org/repo.git":                    "git@github.com:org/repo.git", // scp-style: no secret
		"ssh://git@host.example/org/repo.git":            "ssh://git@host.example/org/repo.git",
		"ssh://git:S3cret@host.example/org/repo.git":     "ssh://git@host.example/org/repo.git",
		"https://ghp_FAKE123@github.com/org/repo.git":    "https://github.com/org/repo.git", // token as the user
		"http://u:S3cret@git.local:8080/r.git":           "http://git.local:8080/r.git",
		"https://github.com/org/repo.git?token=S3cret#x": "https://github.com/org/repo.git",
		"https://github.com/org/repo.git":                "https://github.com/org/repo.git",
		"https://u:S3cret@bad host/r.git":                "", // unparseable → dropped, never raw
	} {
		if got := sanitizeRemoteURL(in); got != want {
			t.Errorf("sanitizeRemoteURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestRedactCommandSecrets_MasksURLCredentials(t *testing.T) {
	got := redactCommandSecrets(`claude 'git_remote_url="https://u:S3cret@github.com/o/r.git"'`)
	if strings.Contains(got, "S3cret") || !strings.Contains(got, "https://<redacted>@github.com/o/r.git") {
		t.Errorf("redacted = %q", got)
	}
	if plain := `claude 'git@github.com:o/r.git https://github.com/o/r.git'`; redactCommandSecrets(plain) != plain {
		t.Errorf("URLs without credentials must be unchanged: %q", redactCommandSecrets(plain))
	}
}

// TestVibeflowInitPrompt_CarriesSessionIdentity launches and then restarts a
// VibeFlow-mode session on a real tmux server with a fake agent, and checks
// the prompt the agent receives names the session ID vibeflow-cli launched
// it with, the harness, branch, remote and working directory.
func TestVibeflowInitPrompt_CarriesSessionIdentity(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	if out, err := exec.Command("git", "-C", repo, "remote", "add", "origin", "git@github.com:org/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("add remote: %v: %s", err, out)
	}
	t.Chdir(repo)
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("MCP_TOKEN", "")

	socket := fmt.Sprintf("vftest-identity-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	record := filepath.Join(state, "agent-args")
	binary := filepath.Join(state, "fake-agent")
	script := "#!/bin/sh\nprintf '%s\\n' \"$@\" > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	cfg.Providers["claude"] = Provider{Binary: binary, LaunchTemplate: "{{.Binary}}"}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	readArgs := func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(record); err == nil && len(data) > 0 {
				return string(data)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fake agent did not record its arguments")
		return ""
	}

	// launchCmd reads the root's persistent --mcp flag, so run it under a root.
	root := &cobra.Command{Use: "vibeflow"}
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().String("mcp", "", "")
	root.AddCommand(launchCmd())
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetArgs([]string{"launch", "--provider", "claude", "--session-type", "vibeflow", "--persona", "qa_lead", "--project", "demo", "--model", "m1"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}
	metas, err := NewStore().List()
	if err != nil || len(metas) != 1 {
		t.Fatalf("stored sessions = %v, %v", metas, err)
	}
	meta := metas[0]
	check := func(stage, args string) {
		t.Helper()
		for _, want := range []string{
			`Initialize a vibeflow session for project demo with persona "qa_lead" and follow the agent prompt.`,
			fmt.Sprintf(`session_id=%q`, meta.Name),
			`agent_type="claude"`,
			`agent_model="m1"`,
			`git_branch="main"`,
			`git_remote_url="git@github.com:org/repo.git"`,
			fmt.Sprintf(`working_directory=%q`, meta.WorkingDir),
		} {
			if !strings.Contains(args, want) {
				t.Errorf("%s prompt missing %s\n---\n%s", stage, want, args)
			}
		}
	}
	check("launch", readArgs())

	// Restart must register with the same identity.
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg)); err != nil {
		t.Fatal(err)
	}
	check("restart", readArgs())

	// A token-bearing HTTPS origin must never reach the prompt, the spawn log
	// or the session store.
	if out, err := exec.Command("git", "-C", repo, "remote", "set-url", "origin", "https://x-access-token:ghp_FAKE123@github.com/org/repo.git").CombinedOutput(); err != nil {
		t.Fatalf("set remote: %v: %s", err, out)
	}
	if err := os.Remove(record); err != nil {
		t.Fatal(err)
	}
	if _, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg)); err != nil {
		t.Fatal(err)
	}
	args := readArgs()
	if !strings.Contains(args, `git_remote_url="https://github.com/org/repo.git"`) {
		t.Errorf("prompt missing the sanitized remote:\n%s", args)
	}
	for _, f := range []string{"", filepath.Join(state, "vibeflow-cli.log"), DefaultStorePath()} {
		text := args
		if f != "" {
			data, err := os.ReadFile(f)
			if err != nil || len(data) == 0 {
				t.Fatalf("read %s: %v", f, err)
			}
			text = string(data)
		}
		if strings.Contains(text, "ghp_FAKE123") {
			t.Errorf("remote token leaked into %s", map[bool]string{true: "the prompt", false: f}[f == ""])
		}
	}
}
