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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildVibeflowInitPrompt(t *testing.T) {
	tests := []struct {
		name        string
		mcpName     string
		projectName string
		persona     string
		want        string
	}{
		{
			name:        "empty mcp name falls back to default",
			mcpName:     "",
			projectName: "vibeflow-cli",
			persona:     "developer",
			want:        `Initialize a vibeflow session for project vibeflow-cli with persona "developer" and follow the agent prompt.`,
		},
		{
			name:        "default mcp name preserves the existing wording verbatim",
			mcpName:     DefaultMCPToolName,
			projectName: "vibeflow-cli",
			persona:     "developer",
			want:        `Initialize a vibeflow session for project vibeflow-cli with persona "developer" and follow the agent prompt.`,
		},
		{
			name:        "custom mcp name overrides the default in the prompt body",
			mcpName:     "myvibeflow",
			projectName: "vibeflow-cli",
			persona:     "developer",
			want:        `Initialize a myvibeflow session for project vibeflow-cli with persona "developer" and follow the agent prompt.`,
		},
		{
			name:        "custom mcp name with non-default persona",
			mcpName:     "vf-staging",
			projectName: "demo",
			persona:     "architect",
			want:        `Initialize a vf-staging session for project demo with persona "architect" and follow the agent prompt.`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := BuildVibeflowInitPrompt(tc.mcpName, tc.projectName, tc.persona)
			if got != tc.want {
				t.Errorf("BuildVibeflowInitPrompt(%q, %q, %q):\n got:  %q\n want: %q",
					tc.mcpName, tc.projectName, tc.persona, got, tc.want)
			}
		})
	}
}

func TestBuildVibeflowCloudDispatchInitPrompt(t *testing.T) {
	got := BuildVibeflowCloudDispatchInitPrompt("", "demo", "developer", "session-20260626-120000-abcd1234")
	for _, want := range []string{
		"Initialize a vibeflow session",
		`dispatch_mode="cloud_queue"`,
		"Do not call wait_for_work",
		"VIBEFLOW_DISPATCH",
		"session-20260626-120000-abcd1234",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("cloud dispatch prompt missing %q:\n%s", want, got)
		}
	}
}

func TestDefaultMCPToolName(t *testing.T) {
	if DefaultMCPToolName != "vibeflow" {
		t.Errorf("DefaultMCPToolName = %q, want %q (changing this is a breaking behavioral change — every existing session restart would receive a different init prompt)", DefaultMCPToolName, "vibeflow")
	}
}

func TestAppendVibeflowInitPrompt(t *testing.T) {
	const prompt = `Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.`

	tests := []struct {
		name        string
		providerKey string
		want        string
	}{
		{
			name:        "claude — positional argument",
			providerKey: "claude",
			want:        `claude --dangerously-skip-permissions 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "codex — positional argument",
			providerKey: "codex",
			want:        `codex --dangerously-skip-permissions 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "cursor — positional argument",
			providerKey: "cursor",
			want:        `cursor --dangerously-skip-permissions 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "gemini — -p (non-interactive headless)",
			providerKey: "gemini",
			want:        `gemini --dangerously-skip-permissions -p 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "qwen — -i (continue interactive after prompt) — regression test for issue #1981",
			providerKey: "qwen",
			want:        `qwen --dangerously-skip-permissions -i 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "kiro — positional argument (verified interactive, see doc comment)",
			providerKey: "kiro",
			want:        `kiro --dangerously-skip-permissions 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "copilot — -i (start interactive and auto-execute, verified v1.0.79)",
			providerKey: "copilot",
			want:        `copilot --dangerously-skip-permissions -i 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
		{
			name:        "unknown provider — defaults to positional",
			providerKey: "rogue-provider",
			want:        `rogue-provider --dangerously-skip-permissions 'Initialize a vibeflow session for project demo with persona "developer" and follow the agent prompt.'`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			base := tc.providerKey + " --dangerously-skip-permissions"
			got := AppendVibeflowInitPrompt(base, tc.providerKey, prompt)
			if got != tc.want {
				t.Errorf("AppendVibeflowInitPrompt(%q, %q, prompt):\n got:  %q\n want: %q",
					base, tc.providerKey, got, tc.want)
			}
		})
	}
}

func TestAppendVibeflowInitPrompt_QwenIsInteractive(t *testing.T) {
	// Regression guard for issue #1981. qwen's positional argument is
	// one-shot mode (qwen exits after processing); a vibeflow autonomous
	// session must use `-i` so qwen continues running for the
	// wait_for_work polling loop. If a future refactor silently moves qwen
	// back into the `default` (positional) case, this test catches it.
	got := AppendVibeflowInitPrompt("qwen --yolo", "qwen", "hello world")
	const wantFlag = " -i '"
	if !strings.Contains(got, wantFlag) {
		t.Errorf("AppendVibeflowInitPrompt qwen output %q is missing the %q flag — qwen must use -i (continue interactive), NOT positional one-shot. See issue #1981.", got, wantFlag)
	}
	if strings.Contains(got, " -p '") {
		t.Errorf("AppendVibeflowInitPrompt qwen output %q uses the deprecated -p flag. Use -i / --prompt-interactive instead.", got)
	}
}

func TestAppendVibeflowInitPrompt_EscapesSingleQuotes(t *testing.T) {
	// Embedded single quotes in the prompt must be sh-escaped via the
	// '\'' idiom so the wrapping single-quoted argument stays balanced
	// when tmux passes the command through `sh -c`.
	got := AppendVibeflowInitPrompt("claude", "claude", "it's fine")
	const want = `claude 'it'\''s fine'`
	if got != want {
		t.Errorf("AppendVibeflowInitPrompt(claude, \"it's fine\"):\n got:  %q\n want: %q", got, want)
	}
}

// qwenConfiguredHome points HOME at a fresh Qwen Code install that has
// already completed auth setup, so tests that pin the qwen command shape are
// not affected by the machine's own ~/.qwen/settings.json (or the
// fresh-install --auth-type flag, covered by its own test).
func qwenConfiguredHome(t *testing.T) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("QWEN_OAUTH", "")
	if err := os.MkdirAll(filepath.Join(home, ".qwen"), 0o755); err != nil {
		t.Fatal(err)
	}
	settings := []byte(`{"security":{"auth":{"selectedType":"openai"}}}`)
	if err := os.WriteFile(filepath.Join(home, ".qwen", "settings.json"), settings, 0o600); err != nil {
		t.Fatal(err)
	}
}

func TestAppendQwenAPIFlags(t *testing.T) {
	qwenConfiguredHome(t)
	tests := []struct {
		name        string
		providerKey string
		base        string
		env         map[string]string
		want        string
	}{
		{
			name:        "qwen with all three env vars — emits base-url/model flags, never the key (issue #1993)",
			providerKey: "qwen",
			base:        "qwen --yolo",
			env: map[string]string{
				"OPENAI_API_KEY":  "sk-test-123",
				"OPENAI_BASE_URL": "https://api.z.ai/api/coding/paas/v4",
				"OPENAI_MODEL":    "GLM-4.6",
			},
			want: `qwen --yolo --openai-base-url 'https://api.z.ai/api/coding/paas/v4' --model 'GLM-4.6'`,
		},
		{
			name:        "qwen gateway mode — only key + base-url present (no OPENAI_MODEL)",
			providerKey: "qwen",
			base:        "qwen --yolo",
			env: map[string]string{
				"OPENAI_API_KEY":  "gateway-token",
				"OPENAI_BASE_URL": "https://gateway.example/rest/v1/llm-gateway/v1",
			},
			want: `qwen --yolo --openai-base-url 'https://gateway.example/rest/v1/llm-gateway/v1'`,
		},
		{
			name:        "qwen with only OPENAI_API_KEY — no flags; key is env-only (ps-aux exposure, issue #1993)",
			providerKey: "qwen",
			base:        "qwen --yolo",
			env: map[string]string{
				"OPENAI_API_KEY": "sk-test-123",
			},
			want: `qwen --yolo`,
		},
		{
			name:        "qwen with empty env values — no flags emitted (empty != present)",
			providerKey: "qwen",
			base:        "qwen --yolo",
			env: map[string]string{
				"OPENAI_API_KEY":  "",
				"OPENAI_BASE_URL": "",
				"OPENAI_MODEL":    "",
			},
			want: `qwen --yolo`,
		},
		{
			name:        "qwen with nil env — command unchanged",
			providerKey: "qwen",
			base:        "qwen --yolo",
			env:         nil,
			want:        `qwen --yolo`,
		},
		{
			name:        "claude — non-qwen provider, command unchanged even with OPENAI_* in env",
			providerKey: "claude",
			base:        "claude --dangerously-skip-permissions",
			env: map[string]string{
				"OPENAI_API_KEY":  "sk-test",
				"OPENAI_BASE_URL": "https://api.example",
				"OPENAI_MODEL":    "gpt-4",
			},
			want: `claude --dangerously-skip-permissions`,
		},
		{
			name:        "codex — non-qwen provider, command unchanged even though codex also reads OPENAI_*",
			providerKey: "codex",
			base:        "codex --yolo",
			env: map[string]string{
				"OPENAI_API_KEY": "sk-test",
			},
			want: `codex --yolo`,
		},
		{
			name:        "gemini — non-qwen provider, command unchanged",
			providerKey: "gemini",
			base:        "gemini --yolo",
			env: map[string]string{
				"OPENAI_MODEL": "should-not-leak",
			},
			want: `gemini --yolo`,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendQwenAPIFlags(tc.base, tc.providerKey, tc.env)
			if got != tc.want {
				t.Errorf("AppendQwenAPIFlags(%q, %q, env):\n got:  %q\n want: %q",
					tc.base, tc.providerKey, got, tc.want)
			}
		})
	}
}

func TestAppendQwenAPIFlags_EscapesSingleQuotes(t *testing.T) {
	qwenConfiguredHome(t)
	// Single quotes in values must be sh-escaped via the '\'' idiom so the
	// wrapping single-quoted argument stays balanced when tmux passes the
	// assembled command through `sh -c`. The same idiom is used by
	// AppendVibeflowInitPrompt — keeping them consistent simplifies review.
	env := map[string]string{
		"OPENAI_API_KEY":  "weird'key",
		"OPENAI_BASE_URL": "https://host/api?q=it's",
		"OPENAI_MODEL":    "model'name",
	}
	got := AppendQwenAPIFlags("qwen", "qwen", env)
	const want = `qwen --openai-base-url 'https://host/api?q=it'\''s' --model 'model'\''name'`
	if got != want {
		t.Errorf("AppendQwenAPIFlags escape:\n got:  %q\n want: %q", got, want)
	}
}

func TestAppendQwenAPIFlags_OrderingWithInitPrompt(t *testing.T) {
	qwenConfiguredHome(t)
	// Integration: flags must land between the base command (e.g. `qwen --yolo`)
	// and the `-i 'prompt'` arg appended by AppendVibeflowInitPrompt, so qwen's
	// arg parser sees them as options rather than as part of the seed prompt.
	env := map[string]string{
		"OPENAI_API_KEY":  "sk-test",
		"OPENAI_BASE_URL": "https://api.z.ai/api/coding/paas/v4",
		"OPENAI_MODEL":    "GLM-4.6",
	}
	cmd := "qwen --yolo"
	cmd = AppendQwenAPIFlags(cmd, "qwen", env)
	cmd = AppendVibeflowInitPrompt(cmd, "qwen", "hello world")
	const want = `qwen --yolo --openai-base-url 'https://api.z.ai/api/coding/paas/v4' --model 'GLM-4.6' -i 'hello world'`
	if cmd != want {
		t.Errorf("Ordering integration:\n got:  %q\n want: %q", cmd, want)
	}
}
func TestAppendCodexGatewayProviderFlags(t *testing.T) {
	tests := []struct {
		name        string
		providerKey string
		base        string
		env         map[string]string
		wantPieces  []string
	}{
		{
			name:        "codex with routed base URL",
			providerKey: "codex",
			base:        "codex --yolo",
			env: map[string]string{
				"OPENAI_BASE_URL": "https://gateway.example/rest/v1/llm-gateway/v1",
			},
			wantPieces: []string{
				`codex --yolo -c 'model_provider="vibeflow_gateway"'`,
				`-c 'model_providers.vibeflow_gateway.name="VibeFlowGateway"'`,
				`-c 'model_providers.vibeflow_gateway.base_url="https://gateway.example/rest/v1/llm-gateway/v1"'`,
				`-c model_providers.vibeflow_gateway.requires_openai_auth=true`,
				`-c 'model_providers.vibeflow_gateway.wire_api="responses"'`,
				`-c model_providers.vibeflow_gateway.supports_websockets=false`,
				`-c 'model_providers.vibeflow_gateway.env_http_headers.x-axiom-api-key="GATEWAY_API_KEY"'`,
			},
		},
		{
			name:        "codex with special characters escapes as one arg",
			providerKey: "codex",
			base:        "codex --yolo",
			env: map[string]string{
				"OPENAI_BASE_URL": "https://host/api?q=it's",
			},
			wantPieces: []string{
				`-c 'model_providers.vibeflow_gateway.base_url="https://host/api?q=it'\''s"'`,
			},
		},
		{
			name:        "non-codex provider unchanged",
			providerKey: "claude",
			base:        "claude --dangerously-skip-permissions",
			env: map[string]string{
				"OPENAI_BASE_URL": "https://gateway.example/rest/v1/llm-gateway/v1",
			},
			wantPieces: []string{`claude --dangerously-skip-permissions`},
		},
		{
			name:        "empty env leaves command unchanged",
			providerKey: "codex",
			base:        "codex --yolo",
			env: map[string]string{
				"OPENAI_BASE_URL": "",
			},
			wantPieces: []string{`codex --yolo`},
		},
		{
			name:        "nil env leaves command unchanged",
			providerKey: "codex",
			base:        "codex --yolo",
			env:         nil,
			wantPieces:  []string{`codex --yolo`},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := AppendCodexGatewayProviderFlags(tc.base, tc.providerKey, tc.env)
			prev := -1
			for _, want := range tc.wantPieces {
				idx := strings.Index(got, want)
				if idx < 0 {
					t.Fatalf("AppendCodexGatewayProviderFlags(%q, %q, env) missing piece %q in %q",
						tc.base, tc.providerKey, want, got)
				}
				if idx < prev {
					t.Fatalf("AppendCodexGatewayProviderFlags(%q, %q, env) out of order: %q appears before previous piece in %q",
						tc.base, tc.providerKey, want, got)
				}
				prev = idx
			}
		})
	}
}

func TestAppendCodexGatewayProviderFlags_OrderingWithInitPrompt(t *testing.T) {
	env := map[string]string{
		"OPENAI_BASE_URL": "https://gateway.example/rest/v1/llm-gateway/v1",
	}
	cmd := "codex --yolo"
	cmd = AppendCodexGatewayProviderFlags(cmd, "codex", env)
	cmd = AppendVibeflowInitPrompt(cmd, "codex", "hello world")
	if !strings.HasSuffix(cmd, ` 'hello world'`) {
		t.Fatalf("ordering integration: init prompt must remain the last argument, got %q", cmd)
	}
	if !strings.Contains(cmd, `-c 'model_provider="vibeflow_gateway"'`) {
		t.Fatalf("ordering integration: missing Codex gateway provider flags in %q", cmd)
	}
	if !strings.Contains(cmd, `-c 'model_providers.vibeflow_gateway.env_http_headers.x-axiom-api-key="GATEWAY_API_KEY"'`) {
		t.Fatalf("ordering integration: missing Codex gateway env_http_headers flag in %q", cmd)
	}
	if strings.Contains(cmd, "env_key") {
		t.Fatalf("ordering integration: Codex gateway auth must not use env_key, got %q", cmd)
	}
}

func TestApplyQwenModelPassthrough(t *testing.T) {
	t.Run("copies shell OPENAI_MODEL for qwen when unset", func(t *testing.T) {
		t.Setenv("OPENAI_MODEL", "glm-4.6")
		env := map[string]string{"OPENAI_API_KEY": "tok"}
		applyQwenModelPassthrough("qwen", env)
		if env["OPENAI_MODEL"] != "glm-4.6" {
			t.Errorf("OPENAI_MODEL = %q, want glm-4.6", env["OPENAI_MODEL"])
		}
	})
	t.Run("existing session value wins over shell", func(t *testing.T) {
		t.Setenv("OPENAI_MODEL", "shell-model")
		env := map[string]string{"OPENAI_MODEL": "wizard-model"}
		applyQwenModelPassthrough("qwen", env)
		if env["OPENAI_MODEL"] != "wizard-model" {
			t.Errorf("OPENAI_MODEL = %q, want wizard-model (session env wins)", env["OPENAI_MODEL"])
		}
	})
	t.Run("non-qwen providers untouched", func(t *testing.T) {
		t.Setenv("OPENAI_MODEL", "glm-4.6")
		env := map[string]string{}
		applyQwenModelPassthrough("codex", env)
		if _, ok := env["OPENAI_MODEL"]; ok {
			t.Error("codex must not receive the qwen model passthrough")
		}
	})
	t.Run("no shell var is a no-op", func(t *testing.T) {
		t.Setenv("OPENAI_MODEL", "")
		env := map[string]string{}
		applyQwenModelPassthrough("qwen", env)
		if _, ok := env["OPENAI_MODEL"]; ok {
			t.Error("empty shell var must not be copied")
		}
	})
	t.Run("nil env is safe", func(t *testing.T) {
		t.Setenv("OPENAI_MODEL", "glm-4.6")
		applyQwenModelPassthrough("qwen", nil) // must not panic
	})
}

func TestExactConversationResume(t *testing.T) {
	const id = "7ae74319-242d-45d1-b251-0495f225448c"
	claudeHint := "Resume this session with:\nclaude --resume " + id
	// VERBATIM from a real dead codex pane (codex-cli 0.154.0). The previous
	// fixture here was hand-written as a single line with no colon, a format
	// codex has never emitted, so the parser and the test agreed with each other
	// and neither agreed with reality (#5176). Codex prints a colon, puts the
	// command on its own INDENTED line, and adds a trailing "Or run ..." line
	// AFTER it, so the id is not on the last line at all.
	codexHint := "To continue this session, run:\n  codex resume " + id
	codexHintWithTrailer := codexHint + "\nOr run codex resume and select Initialize Vibeflow session."
	// Both captures from #5176, byte for byte: the noise codex prints above its
	// hint, the real ids, and each pane's real tmux footer.
	codexCapture := `Token usage: total=51,447 input=50,483 (+ 200,064 cached) output=964 (reasoning 136)
To continue this session, run:
  codex resume 01a08e18-da02-7401-97c0-a77f0eecb388
Or run codex resume and select Initialize Vibeflow session.
Pane is dead (status 0, Fri Sep 11 07:04:08 2026)`
	claudeCapture := `Resume this session with:
claude --resume aee506f9-70b1-42c1-824a-483977ad08de
Pane is dead (status 143, Mon Sep  7 18:19:42 2026)`
	for _, tc := range []struct{ provider, output, want string }{
		{"claude", claudeHint, id},
		{"codex", codexHint, id},
		{"codex", codexHintWithTrailer, id},
		// The exact bytes a user hits: tmux appends its own footer below.
		{"codex", codexHintWithTrailer + "\nPane is dead (status 0, Fri Sep 11 07:04:08 2026)", id},
		{"claude", claudeHint + "\nPane is dead (status 143, Mon Sep  7 18:19:42 2026)", id},
		{"codex", codexCapture, "01a08e18-da02-7401-97c0-a77f0eecb388"},
		{"claude", claudeCapture, "aee506f9-70b1-42c1-824a-483977ad08de"},
		// Still anchored: a bare command line without the "To continue" line
		// above it is arbitrary output, not codex's exit hint.
		{"codex", "  codex resume " + id, ""},
		{"codex", "chat mentioned codex resume " + id + "\nOr run codex resume and select x.", ""},
		// The trailer alone carries no id.
		{"codex", "To continue this session, run:\nOr run codex resume and select x.", ""},
		// tmux TRUNCATES its footer to the pane width, so on a narrow pane it
		// ends mid-timestamp with no closing paren. Stripping it by suffix left
		// it in place and broke resume for every provider (#5176).
		{"codex", codexHintWithTrailer + "\nPane is dead (status 0, Sat Sep 12 06:30", id},
		{"claude", claudeHint + "\nPane is dead (status 143, Mon Sep  7 18:1", id},
		// The recovery footer vibeflow writes on a dead pane, which replaces
		// tmux's default and must be stripped like it.
		{"codex", codexHintWithTrailer + "\nPane is dead (status 0) | Press Enter to resume | Ctrl+Q", id},
		// Panes captured over a CRLF terminal must parse the same.
		{"codex", strings.ReplaceAll(codexHintWithTrailer, "\n", "\r\n"), id},
		// Anything trailing the command is not part of the id.
		{"codex", codexHint + "; touch /tmp/not-allowed", ""},
		{"codex", "To continue this session, run:\n  codex resume not-a-uuid", ""},
		{"codex", codexHintWithTrailer + "\nTo continue this session, run:\n  codex resume not-a-uuid", ""},
		{"codex", codexHintWithTrailer + "\nnew conversation", ""},
		{"claude", claudeHint + "\nnew conversation", ""},
		{"claude", claudeHint + "\nPane is dead (status 0, Fri Sep 11 07:04:08 2026)", id},
		{"claude", "chat mentioned claude --resume " + id, ""},
		{"claude", "Resume this session with:\n" + id, ""},
		{"codex", id, ""},
		{"claude", claudeHint + "; touch /tmp/not-allowed", ""},
		{"copilot", claudeHint, ""},
	} {
		if got := conversationIDFromExitHint(tc.provider, tc.output); got != tc.want {
			t.Errorf("%s hint %q: got %q, want %q", tc.provider, tc.output, got, tc.want)
		}
	}
	for _, tc := range []struct{ provider, binary, id, want string }{
		{"claude", "claude", id, "env TEST=1 claude --model test --resume " + id},
		{"codex", "codex", id, "env TEST=1 codex resume " + id + " --model test"},
		{"claude", "claude", "", "env TEST=1 claude --model test"},
		{"copilot", "copilot", id, "env TEST=1 copilot --model test"},
		{"claude", "claude", "$(touch /tmp/not-allowed)", "env TEST=1 claude --model test"},
	} {
		got, err := renderResumeCommand("env TEST=1 {{.Binary}} --model test", LaunchTemplateVars{Binary: tc.binary}, tc.provider, tc.id, false)
		if err != nil || got != tc.want {
			t.Errorf("%s resume = %q, %v; want %q", tc.provider, got, err, tc.want)
		}
		if strings.Contains(got, "--continue") || strings.Contains(got, "--last") {
			t.Error("restart may not select a directory's latest conversation")
		}
	}
}

func TestQwenEndpointLaunchShape(t *testing.T) {
	env := map[string]string{
		"OPENAI_API_KEY":  "sk-vendor",
		"OPENAI_BASE_URL": "http://llm-proxy.local:4000/v1",
		"OPENAI_MODEL":    "qwen3-coder",
	}
	// Same order as the launch paths: endpoint flags, qwen API flags, prompt.
	cmd := AppendEndpointFlags("qwen --yolo", "qwen", env["OPENAI_BASE_URL"])
	cmd = AppendQwenAPIFlags(cmd, "qwen", env)
	cmd = AppendVibeflowInitPrompt(cmd, "qwen", "hi")
	const want = `qwen --yolo --auth-type openai --openai-base-url 'http://llm-proxy.local:4000/v1' --model 'qwen3-coder' -i 'hi'`
	if cmd != want {
		t.Errorf("command:\n got:  %q\n want: %q", cmd, want)
	}
	if strings.Contains(cmd, "sk-vendor") {
		t.Error("API key must never appear on the command line (issue #1993)")
	}
}

// TestAppendQwenAPIFlags_AuthTypeOnlyWhenQwenWouldStall covers the fresh-install
// hang: qwen stops on its interactive provider picker unless it has a saved
// auth type, and it only infers one from the env when OPENAI_API_KEY,
// OPENAI_MODEL and OPENAI_BASE_URL are all set.
func TestAppendQwenAPIFlags_AuthTypeOnlyWhenQwenWouldStall(t *testing.T) {
	endpoint := map[string]string{"OPENAI_BASE_URL": "http://llm-proxy.local/v1"}
	writeSettings := func(t *testing.T, content string) {
		t.Helper()
		home := t.TempDir()
		t.Setenv("HOME", home)
		if content == "" {
			return // no settings file at all
		}
		if err := os.MkdirAll(filepath.Join(home, ".qwen"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, ".qwen", "settings.json"), []byte(content), 0o600); err != nil {
			t.Fatal(err)
		}
	}
	tests := []struct {
		name, settings, qwenOAuth, command string
		env                                map[string]string
		wantFlag                           bool
	}{
		{name: "no settings file", command: "qwen", env: endpoint, wantFlag: true},
		{name: "settings without an auth type", settings: `{"ui":{"autoModeAcknowledged":true}}`, command: "qwen", env: endpoint, wantFlag: true},
		{name: "saved openai auth", settings: `{"security":{"auth":{"selectedType":"openai"}}}`, command: "qwen", env: endpoint},
		{name: "saved qwen-oauth is left alone", settings: `{"security":{"auth":{"selectedType":"qwen-oauth"}}}`, command: "qwen", env: endpoint},
		{name: "unreadable settings are left alone", settings: `{"security": BROKEN`, command: "qwen", env: endpoint},
		{name: "QWEN_OAUTH wins", qwenOAuth: "1", command: "qwen", env: endpoint},
		{name: "no endpoint supplied", command: "qwen", env: map[string]string{"OPENAI_MODEL": "m"}},
		{name: "endpoint routing already set it", command: "qwen --auth-type openai", env: endpoint},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			writeSettings(t, tt.settings)
			t.Setenv("QWEN_OAUTH", tt.qwenOAuth)
			got := AppendQwenAPIFlags(tt.command, "qwen", tt.env)
			if hasFlag := strings.Count(got, "--auth-type openai") == 1 && !strings.Contains(tt.command, "--auth-type"); hasFlag != tt.wantFlag {
				t.Errorf("command = %q, want --auth-type openai added = %v", got, tt.wantFlag)
			}
			if strings.Count(got, "--auth-type") > 1 {
				t.Errorf("duplicate auth type flag: %q", got)
			}
		})
	}
	// Other providers are never touched.
	if got := AppendQwenAPIFlags("claude", "claude", endpoint); got != "claude" {
		t.Errorf("claude command = %q, want unchanged", got)
	}
}

func TestRecoveryPickerCommands(t *testing.T) {
	for _, tc := range []struct{ provider, want string }{
		{"claude", " --resume"}, {"codex", " resume"},
		{"cursor", " --resume"}, {"qwen", " --resume"},
		{"copilot", " --resume"}, {"kiro", " chat --resume-picker"},
		{"gemini", " -i /resume"},
	} {
		prov := DefaultConfig().Providers[tc.provider]
		got, err := renderResumeCommand(prov.LaunchTemplate, LaunchTemplateVars{Binary: "agent"}, tc.provider, "", true)
		if err != nil || got != "agent"+tc.want {
			t.Errorf("%s picker: got %q, %v; want %q", tc.provider, got, err, "agent"+tc.want)
		}
	}
}
