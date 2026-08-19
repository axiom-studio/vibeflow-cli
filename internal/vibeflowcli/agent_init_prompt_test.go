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

func TestAppendQwenAPIFlags(t *testing.T) {
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

// resumeCases is the single source of truth for the per-provider resume shape
// (issues #4534, #4670). Every entry mirrors a capability probed against the
// real installed binary; `base` mirrors that provider's real LaunchTemplate
// output so the assertions are about commands we actually spawn.
var resumeCases = []struct {
	name        string
	providerKey string
	base        string
	want        string
}{
	{
		name:        "claude - --continue appended",
		providerKey: "claude",
		base:        "claude --dangerously-skip-permissions",
		want:        "claude --dangerously-skip-permissions --continue",
	},
	{
		name:        "copilot - --continue appended (provider merged from main)",
		providerKey: "copilot",
		base:        "copilot --yolo --autopilot --no-ask-user --max-autopilot-continues 1000",
		want:        "copilot --yolo --autopilot --no-ask-user --max-autopilot-continues 1000 --continue",
	},
	{
		name:        "cursor - --continue appended (binary is agent)",
		providerKey: "cursor",
		base:        "agent --yolo --approve-mcps",
		want:        "agent --yolo --approve-mcps --continue",
	},
	{
		name:        "gemini - --resume takes a value",
		providerKey: "gemini",
		base:        "gemini --yolo",
		want:        "gemini --yolo --resume latest",
	},
	{
		name:        "kiro - --resume is a flag on the chat subcommand",
		providerKey: "kiro",
		base:        "kiro-cli chat --trust-all-tools",
		want:        "kiro-cli chat --trust-all-tools --resume",
	},
	{
		name:        "qwen - --continue appended",
		providerKey: "qwen",
		base:        "qwen --yolo",
		want:        "qwen --yolo --continue",
	},
	{
		name:        "codex - subcommand inserted between binary and its options",
		providerKey: "codex",
		base:        "codex --yolo -m 'gpt-5'",
		want:        "codex resume --last --yolo -m 'gpt-5'",
	},
	{
		name:        "codex - bare binary with no options still gets the subcommand",
		providerKey: "codex",
		base:        "codex",
		want:        "codex resume --last",
	},
	{
		name:        "unknown provider is returned unchanged",
		providerKey: "totally-made-up",
		base:        "whatever --flag",
		want:        "whatever --flag",
	},
	{
		name:        "empty provider key is returned unchanged",
		providerKey: "",
		base:        "claude",
		want:        "claude",
	},
	{
		name:        "empty command is returned unchanged",
		providerKey: "codex",
		base:        "",
		want:        "",
	},
}

func TestApplyResume(t *testing.T) {
	for _, tt := range resumeCases {
		t.Run(tt.name, func(t *testing.T) {
			if got := ApplyResume(tt.base, tt.providerKey, false); got != tt.want {
				t.Errorf("ApplyResume(%q, %q, false):\n got:  %q\n want: %q", tt.base, tt.providerKey, got, tt.want)
			}
		})
	}
}

func TestApplyResume_PrecedesInitPrompt(t *testing.T) {
	// The resume flag/subcommand must land BEFORE the init prompt for EVERY
	// provider, not just the positional-argument ones: a flag on the far side of
	// the prompt is read as prompt text (or as a codex SESSION_ID), which fails
	// silently rather than loudly. One row per real provider.
	for _, tt := range []struct{ providerKey, base, want string }{
		{"claude", "claude --dangerously-skip-permissions",
			`claude --dangerously-skip-permissions --continue 'hi'`},
		{"copilot", "copilot --yolo",
			`copilot --yolo --continue -i 'hi'`},
		{"cursor", "agent --yolo",
			`agent --yolo --continue 'hi'`},
		{"gemini", "gemini --yolo",
			`gemini --yolo --resume latest -p 'hi'`},
		{"kiro", "kiro-cli chat --trust-all-tools",
			`kiro-cli chat --trust-all-tools --resume 'hi'`},
		{"qwen", "qwen --yolo",
			`qwen --yolo --continue -i 'hi'`},
		// codex is deliberately NOT resumed when a prompt follows: its single
		// positional would bind to [SESSION_ID] rather than [PROMPT]. See
		// takesPositionalSessionID.
		{"codex", "codex --yolo",
			`codex --yolo 'hi'`},
	} {
		t.Run(tt.providerKey, func(t *testing.T) {
			cmd := ApplyResume(tt.base, tt.providerKey, true)
			cmd = AppendVibeflowInitPrompt(cmd, tt.providerKey, "hi")
			if cmd != tt.want {
				t.Errorf("%s ordering:\n got:  %q\n want: %q", tt.providerKey, cmd, tt.want)
			}
		})
	}
}

func TestResumeStrategiesMatchBuiltinProviders(t *testing.T) {
	// A resume strategy keyed on a provider that does not exist never fires, and
	// the failure is silent: the session just restarts fresh forever. Pin every
	// key to the real built-in registry so a typo, or a provider that only
	// exists on another branch, fails here instead of in production.
	builtins := DefaultConfig().Providers
	for key := range resumeStrategies {
		if _, ok := builtins[key]; !ok {
			t.Errorf("resumeStrategies has key %q, which is not a built-in provider in DefaultConfig()", key)
		}
	}
}

func TestResumeStrategiesExactlyOneShape(t *testing.T) {
	// flag and subcommand are mutually exclusive; setting both would append AND
	// insert, producing a command no CLI accepts.
	for key, s := range resumeStrategies {
		switch {
		case s.flag == "" && s.subcommand == "":
			t.Errorf("resumeStrategies[%q] sets neither flag nor subcommand", key)
		case s.flag != "" && s.subcommand != "":
			t.Errorf("resumeStrategies[%q] sets both flag %q and subcommand %q", key, s.flag, s.subcommand)
		}
	}
}

func TestEveryBuiltinProviderResumes(t *testing.T) {
	// As of #4670 every built-in provider has a verified resume capability. If a
	// new provider is added without one, this fails and forces a decision:
	// either probe the real binary and add a strategy, or document why the
	// provider genuinely cannot resume.
	for key := range DefaultConfig().Providers {
		if !ProviderResumesConversation(key, false) {
			t.Errorf("provider %q has no resume strategy; probe the real binary and add one, or document why it cannot resume", key)
		}
	}
}

func TestApplyResume_CodexSkippedWhenInitPromptFollows(t *testing.T) {
	// `codex resume [OPTIONS] [SESSION_ID] [PROMPT]` binds a single positional to
	// SESSION_ID, so resuming AND appending the vibeflow init prompt would hand
	// codex the entire prompt as a session id. The agent would come back looking
	// resumed while never receiving its instructions — a silent failure. Assert
	// the guard, and assert that vanilla codex sessions (no init prompt) still
	// get the resume they can safely take.
	const base = "codex --yolo"

	if got := ApplyResume(base, "codex", true); got != base {
		t.Errorf("vibeflow codex session must NOT resume:\n got:  %q\n want: %q", got, base)
	}

	const wantVanilla = "codex resume --last --yolo"
	if got := ApplyResume(base, "codex", false); got != wantVanilla {
		t.Errorf("vanilla codex session must resume:\n got:  %q\n want: %q", got, wantVanilla)
	}

	// The whole point is that the init prompt survives intact for the vibeflow
	// case: it must still be the trailing positional of a plain `codex` command.
	full := AppendVibeflowInitPrompt(ApplyResume(base, "codex", true), "codex", "run the loop")
	if full != `codex --yolo 'run the loop'` {
		t.Errorf("init prompt mangled for codex: %q", full)
	}

	// Every other provider is unaffected by the guard.
	for _, key := range []string{"claude", "copilot", "cursor", "gemini", "kiro", "qwen"} {
		if !ProviderResumesConversation(key, true) {
			t.Errorf("provider %q must still resume when an init prompt follows", key)
		}
	}
	if ProviderResumesConversation("codex", true) {
		t.Error("picker would mislabel a vibeflow codex session as resuming")
	}
	if !ProviderResumesConversation("codex", false) {
		t.Error("picker would mislabel a vanilla codex session as a fresh start")
	}
}

// TestCanResumeSession_SameWorkdirTeamIsRefused is the acceptance check for
// #4618. Every resume strategy resolves "the most recent conversation for the
// working DIRECTORY", so a team launch sharing one workDir would have persona A
// come back holding persona B's transcript - B's init prompt, B's session id,
// and anything sensitive echoed into it. Reproduced for real against the claude
// binary: seed ALPHA, seed BRAVO in the same directory, `--continue` returns
// BRAVO. Until resume is bound to a conversation id, the only safe
// directory-scoped resume is a directory with exactly one session.
func TestCanResumeSession_SameWorkdirTeamIsRefused(t *testing.T) {
	dev := SessionMeta{
		Name: "dev", Provider: "claude", SessionType: "vibeflow",
		Persona: "developer", WorkingDir: "/repo",
	}
	sec := SessionMeta{
		Name: "sec", Provider: "claude", SessionType: "vibeflow",
		Persona: "security_lead", WorkingDir: "/repo",
	}
	other := SessionMeta{
		Name: "other", Provider: "claude", SessionType: "vibeflow",
		Persona: "qa_lead", WorkingDir: "/elsewhere",
	}

	t.Run("alone in its directory resumes", func(t *testing.T) {
		ok, why := canResumeSession(dev, []SessionMeta{dev, other})
		if !ok {
			t.Errorf("a solitary session must resume, refused with: %s", why)
		}
	})

	t.Run("sharing a directory refuses", func(t *testing.T) {
		ok, why := canResumeSession(dev, []SessionMeta{dev, sec, other})
		if ok {
			t.Error("two personas in one workdir must NOT resume: persona A would attach to persona B's conversation")
		}
		if why == "" {
			t.Error("refusal must explain itself; the picker shows this to the user")
		}
	})

	t.Run("both peers refuse, not just one", func(t *testing.T) {
		// Whichever pane the user restarts first must be refused. Guarding only
		// one direction would still leak in the other.
		for _, m := range []SessionMeta{dev, sec} {
			if ok, _ := canResumeSession(m, []SessionMeta{dev, sec}); ok {
				t.Errorf("%s must not resume while sharing a workdir", m.Name)
			}
		}
	})

	t.Run("duplicate peer entries do not count twice", func(t *testing.T) {
		// The active store and the dead-session cache overlap, so the same
		// session arrives twice. Counting it twice would refuse every resume.
		if ok, why := canResumeSession(dev, []SessionMeta{dev, dev, dev}); !ok {
			t.Errorf("deduplication failed, refused with: %s", why)
		}
	})

	t.Run("relative workdirs are treated as possibly-shared", func(t *testing.T) {
		// "." from the CLI launch path cannot be compared across repos. Counting
		// them as shared costs a resume; the opposite leaks a conversation.
		a := SessionMeta{Name: "a", Provider: "claude", WorkingDir: "."}
		b := SessionMeta{Name: "b", Provider: "claude", WorkingDir: "."}
		if ok, _ := canResumeSession(a, []SessionMeta{a, b}); ok {
			t.Error("two sessions both recorded as \".\" must not be assumed distinct")
		}
	})

	t.Run("path forms that mean the same directory are matched", func(t *testing.T) {
		a := SessionMeta{Name: "a", Provider: "claude", WorkingDir: "/repo/"}
		b := SessionMeta{Name: "b", Provider: "claude", WorkingDir: "/repo/./"}
		if ok, _ := canResumeSession(a, []SessionMeta{a, b}); ok {
			t.Error("trailing-slash and dot forms of one directory must count as shared")
		}
	})

	t.Run("a provider with no resume support is still refused", func(t *testing.T) {
		codexVibeflow := SessionMeta{
			Name: "cx", Provider: "codex", SessionType: "vibeflow", WorkingDir: "/solo",
		}
		if ok, _ := canResumeSession(codexVibeflow, []SessionMeta{codexVibeflow}); ok {
			t.Error("codex vibeflow sessions cannot resume (positional SESSION_ID); label must say fresh start")
		}
	})
}
