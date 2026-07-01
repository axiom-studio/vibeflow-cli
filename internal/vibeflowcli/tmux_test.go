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
	"os/exec"
	"reflect"
	"strings"
	"testing"
)

func TestRenderLaunchCommand(t *testing.T) {
	t.Run("empty template", func(t *testing.T) {
		got, err := RenderLaunchCommand("", LaunchTemplateVars{Binary: "claude"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("basic template", func(t *testing.T) {
		tmpl := "{{.Binary}} --some-flag"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{Binary: "claude"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "claude --some-flag" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("skip permissions true", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "claude",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "claude --dangerously-skip-permissions" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("skip permissions false", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "claude",
			SkipPermissions: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "claude" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("codex yolo", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "codex",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "codex --yolo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("gemini yolo", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "gemini",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "gemini --yolo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("cursor agent autonomous flags", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo --approve-mcps{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "agent",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "agent --yolo --approve-mcps" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("model template quotes shell values", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .Model }} --model {{ shellQuote .Model }}{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary: "claude",
			Model:  "model with ' quote",
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "claude --model 'model with '\\'' quote'" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("qwen yolo on", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "qwen",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "qwen --yolo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("qwen yolo off", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "qwen",
			SkipPermissions: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "qwen" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("all vars", func(t *testing.T) {
		tmpl := "{{.Binary}} --project={{.Project}} --branch={{.Branch}} --server={{.ServerURL}}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:    "agent",
			Project:   "my-project",
			Branch:    "dev",
			ServerURL: "http://localhost:7080",
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "agent --project=my-project --branch=dev --server=http://localhost:7080"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("invalid template", func(t *testing.T) {
		_, err := RenderLaunchCommand("{{.Invalid}", LaunchTemplateVars{})
		if err == nil {
			t.Fatal("expected error for invalid template")
		}
	})
}

func TestFullSessionName(t *testing.T) {
	tm := &TmuxManager{socketName: "vibeflow"}

	t.Run("with provider", func(t *testing.T) {
		got := tm.FullSessionName("claude", "my-session")
		if got != "vibeflow_claude-my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("without provider", func(t *testing.T) {
		got := tm.FullSessionName("", "my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("codex provider", func(t *testing.T) {
		got := tm.FullSessionName("codex", "feature-branch")
		if got != "vibeflow_codex-feature-branch" {
			t.Errorf("got %q", got)
		}
	})
}

func TestParseSessionProvider(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"claude session", "vibeflow_claude-my-session", "claude"},
		{"codex session", "vibeflow_codex-feature-123", "codex"},
		{"gemini session", "vibeflow_gemini-dev", "gemini"},
		{"cursor session", "vibeflow_cursor-my-agent", "cursor"},
		{"qwen session", "vibeflow_qwen-feature-x", "qwen"},
		{"no provider", "vibeflow_my-session", "my"},
		{"no prefix", "some-session", "some"},
		{"no dash", "vibeflow_nodash", ""},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSessionProvider(tc.input)
			if got != tc.expected {
				t.Errorf("ParseSessionProvider(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestAtoi(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"0", 0},
		{"1", 1},
		{"42", 42},
		{"100", 100},
		{"abc", 0},
		{"12abc34", 1234},
		{"", 0},
		{"3.14", 314},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := atoi(tc.input)
			if got != tc.expected {
				t.Errorf("atoi(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestLaunchTemplateVars_Fields(t *testing.T) {
	vars := LaunchTemplateVars{
		WorkDir:         "/work",
		Project:         "proj",
		Branch:          "main",
		ServerURL:       "http://localhost",
		SessionID:       "session-abc",
		SkipPermissions: true,
		Binary:          "claude",
	}

	// Verify all fields are accessible in templates.
	tmpl := "{{.WorkDir}} {{.Project}} {{.Branch}} {{.ServerURL}} {{.SessionID}} {{.SkipPermissions}} {{.Binary}}"
	got, err := RenderLaunchCommand(tmpl, vars)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/work") {
		t.Error("missing WorkDir")
	}
	if !strings.Contains(got, "session-abc") {
		t.Error("missing SessionID")
	}
	if !strings.Contains(got, "true") {
		t.Error("missing SkipPermissions")
	}
}

func TestEnsurePrefix(t *testing.T) {
	tm := &TmuxManager{socketName: "vibeflow"}

	t.Run("already prefixed", func(t *testing.T) {
		got := tm.ensurePrefix("vibeflow_my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("not prefixed", func(t *testing.T) {
		got := tm.ensurePrefix("my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})
}

func TestNewTmuxManager_DefaultSocket(t *testing.T) {
	tm := NewTmuxManager("")
	if tm.socketName != "vibeflow" {
		t.Errorf("default socketName = %q, want vibeflow", tm.socketName)
	}
}

func TestNewTmuxManager_CustomSocket(t *testing.T) {
	tm := NewTmuxManager("custom")
	if tm.socketName != "custom" {
		t.Errorf("socketName = %q, want custom", tm.socketName)
	}
}

func TestRedactCommandSecrets(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "sh-quoted key flag is masked",
			in:   `qwen --yolo --openai-api-key 'sk-secret-123' --model 'glm-4.6'`,
			want: `qwen --yolo --openai-api-key <redacted> --model 'glm-4.6'`,
		},
		{
			name: "quoted key with embedded sh-escaped quote is fully masked",
			in:   `qwen --openai-api-key 'weird'\''key' -i 'prompt'`,
			want: `qwen --openai-api-key <redacted> -i 'prompt'`,
		},
		{
			name: "bare (unquoted) key value is masked",
			in:   `qwen --openai-api-key sk-bare-456 --model glm`,
			want: `qwen --openai-api-key <redacted> --model glm`,
		},
		{
			name: "equals form is masked",
			in:   `qwen --openai-api-key=sk-equals-789`,
			want: `qwen --openai-api-key <redacted>`,
		},
		{
			name: "command without key flag is unchanged",
			in:   `qwen --yolo --openai-base-url 'https://api.z.ai/api/coding/paas/v4' -i 'hi'`,
			want: `qwen --yolo --openai-base-url 'https://api.z.ai/api/coding/paas/v4' -i 'hi'`,
		},
		{
			name: "empty command is unchanged",
			in:   "",
			want: "",
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactCommandSecrets(tc.in); got != tc.want {
				t.Errorf("redactCommandSecrets(%q):\n got:  %q\n want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestRedactSpawnArg(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"gemini key env", "GEMINI_API_KEY=abc123", "GEMINI_API_KEY=<redacted>"},
		{"mcp token env", "MCP_TOKEN=tok", "MCP_TOKEN=<redacted>"},
		{"vibeflow token env", "VIBEFLOW_TOKEN=tok", "VIBEFLOW_TOKEN=<redacted>"},
		{"gateway api key env", "GATEWAY_API_KEY=tok", "GATEWAY_API_KEY=<redacted>"},
		{"openai key env", "OPENAI_API_KEY=sk-test", "OPENAI_API_KEY=<redacted>"},
		{
			name: "qwen custom api key env (dynamic endpoint-encoded name)",
			in:   "QWEN_CUSTOM_API_KEY_OPENAI_HTTPS_API_Z_AI_API_PAAS_V4=sk-zai",
			want: "QWEN_CUSTOM_API_KEY_OPENAI_HTTPS_API_Z_AI_API_PAAS_V4=<redacted>",
		},
		{
			name: "anthropic custom headers env (gateway token inside header value)",
			in:   "ANTHROPIC_CUSTOM_HEADERS=x-axiom-api-key: tok-123",
			want: "ANTHROPIC_CUSTOM_HEADERS=<redacted>",
		},
		{"anthropic auth token env", "ANTHROPIC_AUTH_TOKEN=tok", "ANTHROPIC_AUTH_TOKEN=<redacted>"},
		{"anthropic api key env", "ANTHROPIC_API_KEY=sk-ant", "ANTHROPIC_API_KEY=<redacted>"},
		{"non-secret env passes through", "OPENAI_MODEL=glm-4.6", "OPENAI_MODEL=glm-4.6"},
		{"plain arg passes through", "-e", "-e"},
		{
			name: "command arg gets key-flag redaction",
			in:   `qwen --openai-api-key 'sk-x' -i 'go'`,
			want: `qwen --openai-api-key <redacted> -i 'go'`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := redactSpawnArg(tc.in); got != tc.want {
				t.Errorf("redactSpawnArg(%q):\n got:  %q\n want: %q", tc.in, got, tc.want)
			}
		})
	}
}

func TestJoinPaneArgs(t *testing.T) {
	got := joinPaneArgs("vibeflow_claude-a", "vibeflow_workbench")
	want := []string{"join-pane", "-s", "vibeflow_claude-a", "-t", "vibeflow_workbench"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("joinPaneArgs = %v, want %v", got, want)
	}
}

func TestTiledLayoutArgs(t *testing.T) {
	got := tiledLayoutArgs("vibeflow_workbench")
	want := []string{"select-layout", "-t", "vibeflow_workbench", "tiled"}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("tiledLayoutArgs = %v, want %v", got, want)
	}
}

func TestWorkbenchHolderName_NoProviderCollision(t *testing.T) {
	if !strings.HasPrefix(workbenchHolderName, sessionPrefix) {
		t.Errorf("holder %q must carry the vibeflow prefix", workbenchHolderName)
	}
	// No provider dash → the holder can never be mistaken for an agent session
	// ("vibeflow_<provider>-<name>").
	if p := ParseSessionProvider(workbenchHolderName); p != "" {
		t.Errorf("holder %q must not parse as a provider session (got %q)", workbenchHolderName, p)
	}
}

func TestComposeWorkbench_TooFewSessions(t *testing.T) {
	// The <2 guard returns before any tmux call, so no server is required.
	tm := NewTmuxManager("vftest-workbench-few")
	if _, err := tm.ComposeWorkbench([]string{"only-one"}); err == nil {
		t.Fatal("expected an error when composing fewer than 2 sessions")
	}
}

// TestComposeWorkbench_RoundTrip exercises the real tmux compose+restore path on
// an isolated throwaway socket: three sessions are joined into one holder window
// and then restored to their own sessions. It proves the restore is
// non-destructive — each session comes back by name with the SAME pane id, so
// the agent process was preserved rather than restarted. Skipped when tmux is
// not installed.
func TestComposeWorkbench_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-workbench-roundtrip")
	// Guarantee a clean slate and always tear the server down.
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	names := []string{"a", "b", "c"}
	full := make([]string, len(names))
	for i, n := range names {
		if err := tm.CreateSessionWithOpts(SessionOpts{
			Name: n, Provider: "claude", WorkDir: dir, Command: "sleep 300",
		}); err != nil {
			t.Fatalf("create session %s: %v", n, err)
		}
		full[i] = tm.FullSessionName("claude", n)
	}

	// Capture each pane id up front to prove the process survives the round trip.
	before := make(map[string]string, len(full))
	for _, fn := range full {
		pid, err := tm.paneID(fn)
		if err != nil {
			t.Fatalf("paneID(%s): %v", fn, err)
		}
		before[fn] = pid
	}

	comp, err := tm.ComposeWorkbench(full)
	if err != nil {
		t.Fatalf("ComposeWorkbench: %v", err)
	}

	// join-pane MOVES panes, so the source sessions are consumed by the compose.
	for _, fn := range full {
		if tm.HasSession(fn) {
			t.Errorf("source session %s should be consumed by compose", fn)
		}
	}
	if !tm.HasSession(comp.HolderName()) {
		t.Fatalf("holder %s missing after compose", comp.HolderName())
	}
	// Holder holds exactly one pane per source (the placeholder was dropped).
	out, err := tm.run("list-panes", "-t", comp.HolderName(), "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes holder: %v", err)
	}
	if got := len(strings.Fields(out)); got != len(full) {
		t.Errorf("holder pane count = %d, want %d:\n%s", got, len(full), out)
	}

	if err := comp.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}

	// Holder is gone; every source is back by name with its ORIGINAL pane id.
	if tm.HasSession(comp.HolderName()) {
		t.Errorf("holder %s should be destroyed after restore", comp.HolderName())
	}
	for _, fn := range full {
		if !tm.HasSession(fn) {
			t.Errorf("session %s was not restored", fn)
			continue
		}
		pid, err := tm.paneID(fn)
		if err != nil {
			t.Errorf("paneID(%s) after restore: %v", fn, err)
			continue
		}
		if pid != before[fn] {
			t.Errorf("session %s pane id changed %s -> %s (process not preserved)", fn, before[fn], pid)
		}
	}
}

func TestIsSecretEnvKey(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"GEMINI_API_KEY", true},
		{"MCP_TOKEN", true},
		{"VIBEFLOW_TOKEN", true},
		{"GATEWAY_API_KEY", true},
		{"OPENAI_API_KEY", true},
		{"ANTHROPIC_CUSTOM_HEADERS", true},
		{"ANTHROPIC_AUTH_TOKEN", true},
		{"ANTHROPIC_API_KEY", true},
		{"QWEN_CUSTOM_API_KEY_OPENAI_HTTPS_API_Z_AI_API_PAAS_V4", true},
		{"OPENAI_BASE_URL", false},
		{"ANTHROPIC_BASE_URL", false},
		{"OPENAI_MODEL", false},
		{"OPENAI_API_KEY_EXTRA", false},
		{"PATH", false},
	}
	for _, tc := range tests {
		if got := isSecretEnvKey(tc.key); got != tc.want {
			t.Errorf("isSecretEnvKey(%q) = %v, want %v", tc.key, got, tc.want)
		}
	}
}
