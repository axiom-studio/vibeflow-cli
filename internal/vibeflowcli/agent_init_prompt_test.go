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
