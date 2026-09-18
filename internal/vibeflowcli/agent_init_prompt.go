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
	"regexp"
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
	// openai-compatible runs the qwen binary, so it takes qwen's -i
	// (interactive) prompt shape rather than the one-shot positional form.
	case "qwen", "openai-compatible", "copilot":
		return baseCommand + fmt.Sprintf(" -i '%s'", escaped)
	default:
		return baseCommand + fmt.Sprintf(" '%s'", escaped)
	}
}

var conversationUUID = regexp.MustCompile(`^[a-fA-F0-9]{8}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{4}-[a-fA-F0-9]{12}$`)

func supportsExactResume(provider, id string) bool {
	return (provider == "claude" || provider == "codex") && conversationUUID.MatchString(id)
}

// conversationIDFromExitHint accepts only a provider's final exit hint, never
// a directory's latest conversation or an ID embedded in arbitrary output.
func conversationIDFromExitHint(provider, output string) string {
	lines := strings.Split(strings.TrimSpace(output), "\n")
	// tmux appends its remain-on-exit footer below the captured process output.
	// Match it by LINE PREFIX rather than requiring the whole capture to end in
	// ")": tmux truncates that footer to the pane width, so on a narrow pane it
	// ends mid-timestamp ("Pane is dead (status 0, Sat Sep 12 06:30") and a
	// suffix check silently leaves it in place. The footer then occupies the
	// last line, every anchor check below looks at the wrong line, and NO
	// provider resumes. Measured at 40 columns; it survived review because the
	// footer happens to fit on a wide pane (#5176).
	for len(lines) > 0 && strings.HasPrefix(strings.TrimSpace(lines[len(lines)-1]), "Pane is dead (") {
		lines = lines[:len(lines)-1]
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return ""
	}
	last := strings.TrimSpace(lines[len(lines)-1])
	var id string
	switch provider {
	case "claude":
		if len(lines) < 2 || strings.TrimSpace(lines[len(lines)-2]) != "Resume this session with:" || !strings.HasPrefix(last, "claude --resume ") {
			return ""
		}
		id = strings.TrimPrefix(last, "claude --resume ")
	case "codex":
		// Codex closes with THREE lines, not one (verified against codex-cli
		// 0.154.0 by capturing a real dead pane):
		//
		//	To continue this session, run:
		//	  codex resume <uuid>
		//	Or run codex resume and select <thread title>.
		//
		// So the id is not on the last line, and the first line ends in a colon
		// with the command indented beneath it. This previously looked for a
		// single line "To continue this session, run codex resume <uuid>", which
		// codex has never emitted, so codex panes never resumed (#5176).
		//
		// The trailing "Or run" line is optional (it names the thread), so it is
		// dropped before looking for the command. Both anchor lines must still be
		// adjacent at the tail: that is what keeps this the provider's own exit
		// hint rather than any UUID that happened to appear in the output.
		tail := lines
		if strings.HasPrefix(strings.TrimSpace(tail[len(tail)-1]), "Or run codex resume and select ") {
			tail = tail[:len(tail)-1]
		}
		if len(tail) < 2 {
			return ""
		}
		command := strings.TrimSpace(tail[len(tail)-1])
		if strings.TrimSpace(tail[len(tail)-2]) != "To continue this session, run:" ||
			!strings.HasPrefix(command, "codex resume ") {
			return ""
		}
		id = strings.TrimPrefix(command, "codex resume ")
	}
	if supportsExactResume(provider, id) {
		return id
	}
	return ""
}

// renderResumeCommand supplies Codex's subcommand through the binary template
// variable so custom wrappers (for example env ... {{.Binary}}) remain valid.
func renderResumeCommand(tmpl string, vars LaunchTemplateVars, provider, id string) (string, error) {
	if supportsExactResume(provider, id) && provider == "codex" {
		// Inserting a subcommand into a quoted executable makes it part of the
		// executable's filename. Support raw binary tokens; refuse other custom
		// templates before the old pane is replaced.
		if tmpl != "" {
			const token = "{{.Binary}}"
			i := strings.Index(tmpl, token)
			if i < 0 || strings.Count(tmpl, token) != 1 {
				return "", fmt.Errorf("exact Codex resume requires an unquoted {{.Binary}} token in the launch template")
			}
			before, after := tmpl[:i], tmpl[i+len(token):]
			if (before != "" && strings.TrimRight(before, " \t\r\n") == before) || (after != "" && strings.TrimLeft(after, " \t\r\n") == after && !strings.HasPrefix(after, "{{")) {
				return "", fmt.Errorf("exact Codex resume requires an unquoted {{.Binary}} token in the launch template")
			}
		}
		vars.Binary = shellQuote(vars.Binary) + " resume " + shellQuote(id)
	}
	command, err := RenderLaunchCommand(tmpl, vars)
	if err != nil {
		return "", err
	}
	if command == "" {
		command = vars.Binary
	}
	if supportsExactResume(provider, id) && provider == "claude" {
		command += " --resume " + shellQuote(id)
	}
	return command, nil
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
	// Only qwen-binary providers understand --openai-base-url / --model.
	if !usesQwenHarness(providerKey) {
		return baseCommand
	}
	out := baseCommand
	// A fresh qwen install has no saved auth type; without this flag the
	// interactive session stops on an auth-provider picker and an unattended
	// pane hangs. The qwen provider keeps relying on the user's own settings.
	if providerKey == "openai-compatible" {
		out += " --auth-type openai"
	}
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
	// Skip non-qwen-binary providers, and never override a model the
	// session already carries (wizard, --model flag, or stored metadata).
	if !usesQwenHarness(providerKey) || sessionEnv == nil || sessionEnv["OPENAI_MODEL"] != "" {
		return
	}
	if v := os.Getenv("OPENAI_MODEL"); v != "" {
		sessionEnv["OPENAI_MODEL"] = v
	}
}

// usesQwenHarness reports whether a provider runs the qwen binary and so
// takes its model and endpoint from OPENAI_MODEL / OPENAI_BASE_URL. It is the
// single switch every launch/restart path checks, so a new qwen-backed
// provider only needs to be added here.
func usesQwenHarness(providerKey string) bool {
	return providerKey == "qwen" || providerKey == "openai-compatible"
}
