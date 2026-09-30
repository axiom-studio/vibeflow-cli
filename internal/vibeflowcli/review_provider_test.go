package vibeflowcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestReviewHarnessArgsCoverEveryProvider(t *testing.T) {
	const prompt = "PROMPT"
	for _, tc := range []struct {
		key, model string
		want       []string
		stdin      bool
	}{
		{"claude", "", []string{"-p", "--dangerously-skip-permissions"}, true},
		{"claude", "M", []string{"-p", "--dangerously-skip-permissions", "--model", "M"}, true},
		{"codex", "", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-"}, true},
		{"codex", "M", []string{"exec", "--dangerously-bypass-approvals-and-sandbox", "--skip-git-repo-check", "-m", "M", "-"}, true},
		{"gemini", "M", []string{"--yolo", "-m", "M", "-p", prompt}, false},
		{"qwen", "M", []string{"--yolo", "-m", "M", prompt}, false},
		{"copilot", "M", []string{"--yolo", "--model", "M", "-p", prompt}, false},
		{"cursor", "M", []string{"-p", "--force", "--trust", "--model", "M", prompt}, false},
		{"kiro", "", []string{"chat", "--no-interactive", "--trust-all-tools", prompt}, false},
		{"kiro", "M", []string{"chat", "--no-interactive", "--trust-all-tools", "--model", "M", prompt}, false},
	} {
		args, stdin, err := reviewHarnessArgs(tc.key, tc.model, prompt)
		if err != nil || stdin != tc.stdin || !slices.Equal(args, tc.want) {
			t.Errorf("%s %q: got %q stdin=%v err=%v", tc.key, tc.model, args, stdin, err)
		}
	}
	if _, _, err := reviewHarnessArgs("openshell", "", prompt); err == nil || !strings.Contains(err.Error(), "claude, codex, copilot, cursor, gemini, kiro, qwen") {
		t.Fatalf("unknown harness error must name supported keys: %v", err)
	}
}

// fakeReviewHarnesses installs an executable for every configured harness.
func fakeReviewHarnesses(t *testing.T, cfg *Config, script string) {
	t.Helper()
	bin := t.TempDir()
	for _, key := range reviewHarnessKeys {
		path := filepath.Join(bin, key+"-harness")
		if err := os.WriteFile(path, []byte(script), 0700); err != nil {
			t.Fatal(err)
		}
		p := cfg.Providers[key]
		p.Binary = path
		cfg.Providers[key] = p
	}
}

func TestReviewProviderUsesNormalModeForEveryHarness(t *testing.T) {
	_, execution := reviewTestRepo(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("CODEX_HOME", filepath.Join(home, ".codex"))
	t.Setenv("AMBIENT_USER_SETTING", "kept")
	t.Setenv("OPENAI_API_KEY", "unselected-ambient-key")
	cfg := DefaultConfig()
	cfg.APIToken = "vibeflow-api-token-canary"
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	for _, key := range reviewHarnessKeys {
		t.Run(key, func(t *testing.T) {
			p := cfg.Providers[key]
			p.Env = map[string]string{"HARNESS_SETTING": "$AMBIENT_USER_SETTING-configured", "OPENAI_API_KEY": "selected-model-key"}
			cfg.Providers[key] = p
			root := t.TempDir()
			spec, err := prepareReviewProvider(context.Background(), cfg, key, "some-model", root, execution, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			if spec.Binary != p.Binary || spec.Dir != filepath.Join(root, "input", "head") {
				t.Fatalf("binary %q dir %q", spec.Binary, spec.Dir)
			}
			task := filepath.Join(root, "task.txt")
			want, stdin, _ := reviewHarnessArgs(key, "some-model", "Your complete PR review task is in the file "+task+". Read that whole file first, then follow it exactly.")
			if !slices.Equal(spec.Args, want) {
				t.Fatalf("args %q, want %q", spec.Args, want)
			}
			if (stdin && spec.InputFile != task) || (!stdin && spec.InputFile != os.DevNull) {
				t.Fatalf("input file %q", spec.InputFile)
			}
			for _, kv := range []string{"HOME=" + home, "CODEX_HOME=" + filepath.Join(home, ".codex"), "AMBIENT_USER_SETTING=kept", "HARNESS_SETTING=kept-configured", "OPENAI_API_KEY=selected-model-key"} {
				if !slices.Contains(spec.Env, kv) {
					t.Fatalf("normal-mode env lacks %s", kv)
				}
			}
			if trusted := slices.Contains(spec.Env, "GEMINI_CLI_TRUST_WORKSPACE=true"); trusted != (key == "gemini") {
				t.Fatalf("gemini workspace trust set=%v for %s", trusted, key)
			}
			if strings.Contains(strings.Join(spec.Args, " "), cfg.APIToken) {
				t.Fatal("VibeFlow token reached argv")
			}
			data, err := os.ReadFile(task)
			if err != nil {
				t.Fatal(err)
			}
			for _, want := range []string{execution.Prompt, filepath.Join(root, "result.json"), filepath.Join(root, "schema.json"), "disposable git worktree", execution.Attempt.Round.HeadSHA, `"failure_reason"`} {
				if !strings.Contains(string(data), want) {
					t.Fatalf("task lacks %q", want)
				}
			}
			for _, stale := range []string{"read-only", "do not run project code"} {
				if strings.Contains(string(data), stale) {
					t.Fatalf("task still says %q", stale)
				}
			}
		})
	}
}

func TestReviewPreflightRequiresOnlyAnInstalledHarness(t *testing.T) {
	cfg := DefaultConfig()
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 1\n") // Never executed by preflight.
	for _, key := range reviewHarnessKeys {
		if err := preflightReviewProvider(context.Background(), cfg, key, ""); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	p := cfg.Providers["gemini"]
	p.Binary = filepath.Join(t.TempDir(), "missing")
	cfg.Providers["gemini"] = p
	if err := preflightReviewProvider(context.Background(), cfg, "gemini", ""); err == nil || !strings.Contains(err.Error(), "not installed") {
		t.Fatalf("missing binary: %v", err)
	}
	if err := preflightReviewProvider(context.Background(), cfg, "unknown", ""); err == nil {
		t.Fatal("unknown harness accepted")
	}
	cfg.LLMGatewayEnabled = true
	if err := preflightReviewProvider(context.Background(), cfg, "claude", ""); err == nil {
		t.Fatal("gateway accepted missing model")
	}
}

// reviewResultEnvelope is the result.json a harness writes on success.
func reviewResultEnvelope(t *testing.T, result any) string {
	t.Helper()
	data, err := json.Marshal(map[string]any{"result": result, "failure_reason": nil})
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

// reviewWriteResult is the fake-harness shell line that writes result.json.
// The harness runs in <root>/input/head, so the file is two levels up.
func reviewWriteResult(envelope string) string {
	return "printf '%s\\n' " + shellQuote(envelope) + " > ../../result.json\n"
}

// Messages observed from real harnesses on 2026-09-29.
func TestReviewHarnessOutputNamesUserActions(t *testing.T) {
	for output, want := range map[string]string{
		"Gemini CLI is not running in a trusted directory. To proceed, use --skip-trust":        "untrusted_workspace",
		"Error: Authentication required. Please run 'agent login' first, or set CURSOR_API_KEY": "authentication_required",
		"[API Error: 401 invalid access token or token expired]":                                "authentication_required",
		"Not logged in": "authentication_required",
		"IneligibleTierError: This client is no longer supported for Gemini Code Assist": "access_denied",
		"panic: index out of range": "",
	} {
		if got := classifyReviewHarnessOutput(output); got != want {
			t.Errorf("%q: got %q want %q", output, got, want)
		}
	}
}

// Where a harness has a non-interactive status command (verified 2026-09-30
// against claude 2.1.285, codex, cursor agent and kiro-cli), a logged-out
// harness is refused before Vera claims a review. Copilot, Gemini and Qwen
// have none, so they are never run.
func TestReviewHarnessLoginCheck(t *testing.T) {
	cfg := DefaultConfig()
	args := filepath.Join(t.TempDir(), "args")
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho \"$@\" > "+shellQuote(args)+"\nexit 1\n")
	for key, want := range map[string]string{"claude": "auth status", "codex": "login status", "cursor": "status", "kiro": "whoami"} {
		err := checkReviewHarnessLogin(context.Background(), cfg, key)
		if err == nil || !strings.Contains(err.Error(), "is not logged in") || !strings.Contains(err.Error(), reviewHarnessLoginHint(key)) {
			t.Fatalf("%s: %v", key, err)
		}
		if data, _ := os.ReadFile(args); strings.TrimSpace(string(data)) != want {
			t.Fatalf("%s ran %q", key, data)
		}
	}
	for _, key := range []string{"copilot", "gemini", "qwen"} {
		if err := checkReviewHarnessLogin(context.Background(), cfg, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// cursor's agent reports a missing login with exit status 0.
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho 'Not logged in'\n")
	if err := checkReviewHarnessLogin(context.Background(), cfg, "cursor"); err == nil {
		t.Fatal("cursor logged out accepted")
	}
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\necho 'Logged in using ChatGPT'\n")
	for _, key := range reviewHarnessKeys {
		if err := checkReviewHarnessLogin(context.Background(), cfg, key); err != nil {
			t.Fatalf("%s: %v", key, err)
		}
	}
	// Gateway reviews reach Claude through the VibeFlow relay, not a login.
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 1\n")
	cfg.LLMGatewayEnabled = true
	if err := checkReviewHarnessLogin(context.Background(), cfg, "claude"); err != nil {
		t.Fatal(err)
	}
}

// Claude asks whether to trust every new folder, so a review worktree is
// pre-trusted in Claude's own config and forgotten again at cleanup, keeping
// everything else in that file.
func TestClaudeReviewTrust(t *testing.T) {
	withTempRoot(t)
	dir := t.TempDir()
	t.Setenv("CLAUDE_CONFIG_DIR", dir)
	path := filepath.Join(dir, ".claude.json")
	original := `{"numStartups": 7, "oauthAccount": {"emailAddress": "x"}, "projects": {"/keep": {"hasTrustDialogAccepted": true, "allowedTools": ["a"]}}}`
	if err := os.WriteFile(path, []byte(original), 0644); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work", "req")
	head := filepath.Join(work, "input", "head")
	if err := os.MkdirAll(head, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CLAUDE_CONFIG_DIR": dir}
	if err := trustReviewWorktree("claude", env, head); err != nil {
		t.Fatal(err)
	}
	real, _ := filepath.EvalSymlinks(head)
	var config map[string]any
	data, _ := os.ReadFile(path)
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatal(err)
	}
	projects := config["projects"].(map[string]any)
	if entry, _ := projects[real].(map[string]any); entry == nil || entry["hasTrustDialogAccepted"] != true {
		t.Fatalf("worktree %s not trusted: %s", real, data)
	}
	if info, _ := os.Stat(path); info.Mode().Perm() != 0644 {
		t.Fatalf("mode changed to %v", info.Mode().Perm())
	}
	// Claude adds its own entry for the worktree's repository while it runs.
	projects[filepath.Join(filepath.Dir(filepath.Dir(real)), "objects.git")] = map[string]any{"hasTrustDialogAccepted": true}
	data, _ = json.Marshal(config)
	os.WriteFile(path, data, 0644)
	if err := forgetReviewTrust("claude", env, work); err != nil {
		t.Fatal(err)
	}
	var want, got any
	json.Unmarshal([]byte(original), &want)
	data, _ = os.ReadFile(path)
	json.Unmarshal(data, &got)
	if !equalJSON(want, got) {
		t.Fatalf("cleanup left %s", data)
	}
	// Nothing to forget: the file is not rewritten.
	before, _ := os.Stat(path)
	if err := forgetReviewTrust("claude", env, work); err != nil {
		t.Fatal(err)
	}
	if after, _ := os.Stat(path); !after.ModTime().Equal(before.ModTime()) {
		t.Fatal("file rewritten without a change")
	}
}

// Codex keeps folder trust as [projects."<path>"] tables in config.toml.
func TestCodexReviewTrust(t *testing.T) {
	withTempRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "config.toml")
	original := "model = \"gpt\"\n\n[projects.\"/keep\"]\ntrust_level = \"trusted\"\n\n[mcp_servers.x]\ncommand = \"y\"\n"
	if err := os.WriteFile(path, []byte(original), 0600); err != nil {
		t.Fatal(err)
	}
	work := filepath.Join(t.TempDir(), "work", "req")
	head := filepath.Join(work, "input", "head")
	if err := os.MkdirAll(head, 0700); err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"CODEX_HOME": dir}
	for range 2 { // The second call finds the entry and changes nothing.
		if err := trustReviewWorktree("codex", env, head); err != nil {
			t.Fatal(err)
		}
	}
	real, _ := filepath.EvalSymlinks(head)
	data, _ := os.ReadFile(path)
	if want := original + "\n[projects.\"" + real + "\"]\ntrust_level = \"trusted\"\n"; string(data) != want {
		t.Fatalf("config:\n%s\nwant:\n%s", data, want)
	}
	if err := forgetReviewTrust("codex", env, work); err != nil {
		t.Fatal(err)
	}
	data, _ = os.ReadFile(path)
	if strings.TrimRight(string(data), "\n") != strings.TrimRight(original, "\n") {
		t.Fatalf("cleanup left:\n%s", data)
	}
}
