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
		{"cursor", "M", []string{"-p", "--force", "--model", "M", prompt}, false},
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
