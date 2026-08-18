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

func BuildVibeflowCloudDispatchInitPrompt(mcpName, projectName, persona, sessionID string) string {
	if mcpName == "" {
		mcpName = DefaultMCPToolName
	}
	return fmt.Sprintf(
		"Initialize a %s session for project %s with persona %q using session_id %s. Pass dispatch_mode=\"cloud_queue\" to session_init. Do not call wait_for_work; vibeflow-cli will inject VIBEFLOW_DISPATCH handoffs when work is available.",
		mcpName, projectName, persona, sessionID,
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
//   - kiro → falls through to default (positional argument). VERIFIED
//     against the real `kiro-cli` binary (v2.15.2): `kiro-cli chat
//     'prompt'` (no extra flags) processes the prompt, then returns to its
//     interactive composer awaiting further input — the same shape as
//     claude/codex/cursor. Confirmed via a scripted multi-turn session
//     (first prompt answered, second distinct follow-up prompt answered in
//     the same process) and cross-checked against live vibeflow-launched
//     Kiro sessions using this exact command shape. `kiro-cli`'s documented
//     `--no-interactive` flag is a separate ONE-SHOT mode (process prompt,
//     print result, exit) — it is intentionally NOT added to the
//     LaunchTemplate or to this switch, since a one-shot process can't back
//     vibeflow's persistent tmux session that stays alive polling
//     wait_for_work.
//   - copilot → `-i 'prompt'` (start interactive mode and auto-execute the
//     prompt). VERIFIED against the real `copilot` binary (v1.0.79): the
//     seeded turn executes and the composer stays alive for follow-up
//     turns (scripted tmux session, process liveness confirmed after the
//     seeded response). Copilot's `-p/--prompt` is ONE-SHOT (documented
//     "exits after completion") and there is NO positional prompt argument
//     (usage is `copilot [options] [command]`, a positional would parse as
//     a subcommand) — so copilot must NOT fall through to default.
func AppendVibeflowInitPrompt(baseCommand, providerKey, prompt string) string {
	escaped := strings.ReplaceAll(prompt, "'", `'\''`)
	switch providerKey {
	case "gemini":
		return baseCommand + fmt.Sprintf(" -p '%s'", escaped)
	case "qwen", "copilot":
		return baseCommand + fmt.Sprintf(" -i '%s'", escaped)
	default:
		return baseCommand + fmt.Sprintf(" '%s'", escaped)
	}
}

// resumeFlags maps a provider key to the CLI flag that reattaches the agent to
// the most recent conversation for its working directory. Used when a dead
// session is brought back so the user keeps the conversation instead of
// starting from zero (issue #4534). Providers absent from the map restart with
// a fresh conversation, exactly as they did before.
//
// Verified against the installed binaries, not from memory:
//   - claude → `-c, --continue`: "Continue the most recent conversation in
//     [this directory]".
//   - gemini → `-r, --resume` takes a value; "latest" selects the most recent.
//   - qwen   → `-c, --continue`: "Resume the most recent session for the
//     current project". Inert unless the user enabled `--chat-recording`
//     (qwen's help says history is not saved without it), which is harmless:
//     it degrades to today's fresh restart rather than failing.
//
// Deliberately absent:
//   - codex → resume is a SUBCOMMAND (`codex resume --last`), not a flag, so it
//     cannot be appended to an already-rendered launch command. Supporting it
//     means rewriting the command shape, which is a separate change.
//   - cursor / kiro → resume capability not verified against the real binary.
//     A wrong flag here breaks restart entirely, so they stay on fresh restart
//     until someone confirms the real flag.
//   - copilot → `copilot --help` does document `--continue` ("Resume the most
//     recent session"), but the copilot provider (feature #667, PR #9) is not
//     on this branch. Adding the key now would be an entry for a provider that
//     does not exist. TestResumeFlagsMatchBuiltinProviders fails on it by
//     design. Add it in the same change that adds the provider.
//
// This lives in code rather than in the provider's LaunchTemplate or a new
// Provider field because migrateProviders (config.go) never refreshes an
// existing built-in's template or backfills new struct fields: every current
// user has the old provider block persisted in their config.yaml, so a
// config-side change would silently do nothing for them. Appending after
// render is the same pattern AppendCodexGatewayProviderFlags and
// AppendQwenAPIFlags already use for provider-conditional flags.
var resumeFlags = map[string]string{
	"claude": "--continue",
	"gemini": "--resume latest",
	"qwen":   "--continue",
}

// AppendResumeFlag appends the provider's resume flag to a rendered launch
// command. Providers with no known resume flag are returned unchanged.
//
// Ordering: this MUST run before AppendVibeflowInitPrompt. claude, codex,
// cursor and kiro take the init prompt as a POSITIONAL argument, so a flag
// appended afterwards would land on the far side of the prompt.
//
// The flag is passed unconditionally for known providers, with no check that a
// conversation actually exists to resume: `claude --continue` in a directory
// with no history starts a fresh conversation rather than failing (verified by
// running it under `--print` in an empty temp directory). That makes a
// transcript-existence probe (which would need a per-provider history
// location, path escaping and a filesystem race) pure cost for identical
// behaviour. A session being restarted has by definition already run once, so
// the no-history case is close to unreachable regardless.
func AppendResumeFlag(baseCommand, providerKey string) string {
	flag, ok := resumeFlags[providerKey]
	if !ok {
		return baseCommand
	}
	return baseCommand + " " + flag
}

// ProviderResumesConversation reports whether a restart of the given provider
// keeps the prior conversation. The dead-session picker uses it to tell the
// user which of the two they are about to get.
func ProviderResumesConversation(providerKey string) bool {
	_, ok := resumeFlags[providerKey]
	return ok
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
			codexConfigBoolArg("model_providers."+codexGatewayProviderID+".requires_openai_auth", true),
			// codex-cli >= 0.139 hard-removed the chat wire API (config-load
			// error on wire_api="chat"), so Responses is the only wire API
			// codex accepts. The gateway does not serve /v1/responses yet —
			// until that route ships server-side (tracked on issue #2781),
			// codex requests through the gateway fail with 404.
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".wire_api", "responses"),
			codexConfigBoolArg("model_providers."+codexGatewayProviderID+".supports_websockets", false),
			codexConfigStringArg("model_providers."+codexGatewayProviderID+".env_http_headers.x-axiom-api-key", "GATEWAY_API_KEY"),
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
