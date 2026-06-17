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
	"strings"
)

// DefaultMCPToolName is the default name of the vibeflow MCP server. Users
// running a renamed or forked MCP server can override it via the `--mcp`
// CLI flag or the `mcp_tool_name` field in config.yaml.
const DefaultMCPToolName = "vibeflow"
const codexGatewayProviderID = "vibeflow_gateway"
const codexGatewayProviderName = "VibeFlowGateway"

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

// AppendCodexGatewayProviderFlags appends a temporary Codex CLI custom
// provider definition when the launch env has a routed OpenAI-compatible
// base URL.
//
// The built-in `openai` provider can still probe websocket transport even
// when pointed at a gateway. Defining a dedicated provider with websocket
// support disabled avoids that startup fallback noise while preserving the
// gateway routing.
func AppendCodexGatewayProviderFlags(baseCommand, providerKey string, env map[string]string) string {
	if providerKey != "codex" || env == nil {
		return baseCommand
	}
	if v := env["OPENAI_BASE_URL"]; v != "" {
		flags := []string{
			codexConfigStringArg("model_provider", codexGatewayProviderID),
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".name", codexGatewayProviderName),
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".base_url", v),
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".env_key", "OPENAI_API_KEY"),
			// codex-cli >= 0.139 hard-removed the chat wire API (config-load
			// error on wire_api="chat"), so Responses is the only wire API
			// codex accepts. The gateway does not serve /v1/responses yet —
			// until that route ships server-side (tracked on issue #2781),
			// codex requests through the gateway fail with 404.
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".wire_api", "responses"),
			codexConfigBoolArg("model_providers."+codexGatewayProviderID+".supports_websockets", false),
			codexConfigRawArg(`env_http_headers = { "x-axiom-api-key" = "GATEWAY_API_KEY" }`),
		}
		for _, flag := range flags {
			baseCommand += " -c " + flag
		}
	}
	return baseCommand
}

func codexConfigStringArg(key, value string) string {
	return shellQuote(fmt.Sprintf("%s=%q", key, value))
}

func codexConfigBoolArg(key string, value bool) string {
	return shellQuote(fmt.Sprintf("%s=%t", key, value))
}

func codexConfigRawArg(value string) string {
	return shellQuote(value)
}

// AppendQwenAPIFlags appends `--openai-base-url` and `--model` flags to the
// qwen launch command when the corresponding env vars are present in env.
// Non-qwen providers are returned unchanged.
//
// Why: qwen-code does not consistently honor `OPENAI_MODEL` env var for
// model reporting in tool calls (observed: env says GLM-5-turbo, MCP tool
// calls say `qwen 235b`). The CLI flags are authoritative — passing them
// explicitly forces qwen-code to use the vendor/model the user picked in
// `StepQwenLaunchConfig`. The env vars are left in the session env as a
// fallback for any qwen-code code path that still reads them.
//
// The API key is deliberately NOT passed as a `--openai-api-key` flag: a
// flag value is world-readable via `ps aux` / `/proc/<pid>/cmdline` (issue
// #1993, SOC2 CC6.1 / PCI-DSS 3.5 / GDPR Art.32). qwen-code reads
// OPENAI_API_KEY from the process env on every auth path we ship, and the
// env var is set on all launch paths, so the flag added no functionality —
// only exposure.
//
// Ordering: flags are inserted after the base command (e.g. `qwen --yolo`)
// and BEFORE `AppendVibeflowInitPrompt` appends `-i 'prompt'`, so qwen's
// arg parser sees them as options rather than as part of the seed prompt.
//
// Sh-escaping mirrors `AppendVibeflowInitPrompt`: each value is wrapped in
// single quotes and embedded single quotes use standard shell escaping, since
// the assembled command is handed to `sh -c` via tmux send-keys.
func AppendQwenAPIFlags(baseCommand, providerKey string, env map[string]string) string {
	if providerKey != "qwen" {
		return baseCommand
	}
	out := baseCommand
	if v := env["OPENAI_BASE_URL"]; v != "" {
		out += fmt.Sprintf(" --openai-base-url '%s'", strings.ReplaceAll(v, "'", `'\''`))
	}
	if v := env["OPENAI_MODEL"]; v != "" {
		out += fmt.Sprintf(" --model '%s'", strings.ReplaceAll(v, "'", `'\''`))
	}
	return out
}

// applyQwenModelPassthrough copies OPENAI_MODEL from the calling shell into
// the session env for qwen launches when it isn't already set. Wizard-driven
// launches carry the model via WizardResult.EnvVars, but headless launches
// and restarts have no wizard state (wizard env vars are not persisted), so
// the shell export is the only model source — copying it in lets
// AppendQwenAPIFlags emit an explicit `--model` flag on those paths too.
func applyQwenModelPassthrough(providerKey string, sessionEnv map[string]string) {
	if providerKey != "qwen" || sessionEnv == nil || sessionEnv["OPENAI_MODEL"] != "" {
		return
	}
	if v := os.Getenv("OPENAI_MODEL"); v != "" {
		sessionEnv["OPENAI_MODEL"] = v
	}
}
