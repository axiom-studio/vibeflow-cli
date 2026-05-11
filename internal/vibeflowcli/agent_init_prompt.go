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
	"strings"
)

// DefaultMCPToolName is the default name of the vibeflow MCP server. Users
// running a renamed or forked MCP server can override it via the `--mcp`
// CLI flag or the `mcp_tool_name` field in config.yaml.
const DefaultMCPToolName = "vibeflow"

// BuildVibeflowInitPrompt returns the prompt vibeflow-cli passes to a
// vibecoding agent when launching a vibeflow-managed session. mcpName names
// the MCP server the agent should call `session_init` on; an empty value
// falls back to DefaultMCPToolName.
func BuildVibeflowInitPrompt(mcpName, projectName, persona string) string {
	if mcpName == "" {
		mcpName = DefaultMCPToolName
	}
	return fmt.Sprintf(
		"Initialize a %s session for project %s with persona %q and follow the agent prompt.",
		mcpName, projectName, persona,
	)
}

// AppendVibeflowInitPrompt appends a vibeflow init prompt to a rendered
// launch command in the argument shape each provider's CLI expects, and
// sh-escapes embedded single quotes so the result is a safe single-string
// shell command for tmux to pass through `sh -c`.
//
// Per-provider shape (verified against the upstream CLIs):
//   - claude / codex / cursor / default → positional argument (` 'prompt'`).
//     These CLIs treat a positional arg as an initial prompt and stay
//     interactive — which is what an autonomous vibeflow session needs.
//   - gemini → `-p 'prompt'` (non-interactive headless mode). This is the
//     historical shape used by vibeflow-cli; behavioral correctness for
//     long-running autonomous loops is being tracked separately.
//   - qwen → `-i 'prompt'` (execute prompt + continue interactive). qwen's
//     positional argument is ONE-SHOT mode: qwen processes the prompt and
//     exits, which is wrong for autonomous sessions. The `-i` /
//     `--prompt-interactive` flag is the documented way to seed an
//     interactive run with an initial prompt.
func AppendVibeflowInitPrompt(baseCommand, providerKey, prompt string) string {
	escaped := strings.ReplaceAll(prompt, "'", `'\''`)
	switch providerKey {
	case "gemini":
		return baseCommand + fmt.Sprintf(" -p '%s'", escaped)
	case "qwen":
		return baseCommand + fmt.Sprintf(" -i '%s'", escaped)
	default:
		return baseCommand + fmt.Sprintf(" '%s'", escaped)
	}
}
