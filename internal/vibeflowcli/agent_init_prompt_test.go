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

