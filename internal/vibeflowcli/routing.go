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

import "strings"

// Routing modes: how a harness reaches its model.
const (
	// RoutingDirect connects the harness to its own provider (subscription
	// or provider API key). The default.
	RoutingDirect = "direct"
	// RoutingGateway routes through the Axiom Studio AI Gateway.
	RoutingGateway = "gateway"
	// RoutingEndpoint connects to a user-supplied compatible endpoint
	// (hosted API, LiteLLM, vLLM, ...).
	RoutingEndpoint = "endpoint"
)

// endpointAPIFormats lists the harnesses that can use a compatible endpoint
// and the API the endpoint must speak for each. A harness missing from this
// map (Cursor, Kiro) has no way to point at a custom endpoint.
var endpointAPIFormats = map[string]string{
	"copilot": "OpenAI API",
	"qwen":    "OpenAI API",
	"codex":   "OpenAI Responses API",
	"claude":  "Anthropic Messages API",
	"gemini":  "Gemini API",
}

// EndpointAPIFormat returns the API a compatible endpoint must speak for the
// harness, and false when the harness cannot use a custom endpoint.
func EndpointAPIFormat(providerKey string) (string, bool) {
	format, ok := endpointAPIFormats[providerKey]
	return format, ok
}

// providerSupportsEndpoint reports whether a harness can be pointed at a
// user-supplied compatible endpoint.
func providerSupportsEndpoint(providerKey string) bool {
	_, ok := endpointAPIFormats[providerKey]
	return ok
}

// resolveRouting returns the routing mode for a launch. An explicit mode wins;
// otherwise it is derived the way launches worked before routing modes
// existed: the gateway flag means gateway, anything else is direct.
func resolveRouting(explicit string, gatewayEnabled bool) string {
	switch {
	case explicit != "":
		return explicit
	case gatewayEnabled:
		return RoutingGateway
	default:
		return RoutingDirect
	}
}

// routingForMeta returns the routing mode a stored session was launched with.
// Records written before the routing field existed fall back to the gateway
// flag and provider.
func routingForMeta(meta SessionMeta) string {
	return resolveRouting(meta.Routing, meta.LLMGatewayEnabled)
}

// endpointSuppliesKey reports whether a "missing env var" from
// ResolveProviderEnvVars can be ignored because the launch uses a compatible
// endpoint, whose key replaces the harness's own provider key. Other missing
// vars (e.g. the codex MCP bearer token) are still required.
func endpointSuppliesKey(routing, missingVar string) bool {
	return routing == RoutingEndpoint && isProviderAPIKeyVar(missingVar)
}

// isProviderAPIKeyVar reports whether a var ResolveProviderEnvVars can ask
// for is a harness's own model-provider API key (as opposed to e.g. the codex
// MCP bearer token). Only direct routing needs it.
func isProviderAPIKeyVar(name string) bool {
	return name == "GEMINI_API_KEY" || name == "OPENAI_API_KEY"
}

// BuildEndpointEnv returns the session env that points a harness at a
// compatible endpoint. The key comes from the vendor's OPENAI_COMPAT_API_KEY_*
// slot (or the default slot) and is only ever passed through the env.
//
// Every variable the harness reads for its endpoint and credentials is set —
// to an empty value or a placeholder when it has none — so a value exported
// in the user's shell (inherited through the tmux server) can never redirect
// the session or leak a key to the endpoint.
func BuildEndpointEnv(providerKey string, cfg *Config, vendor, baseURL, model string) map[string]string {
	env := make(map[string]string)
	key := ResolveOpenAICompatKey(cfg, vendor)
	// Harnesses that refuse to start without a key get the placeholder.
	keyOrPlaceholder := key
	if keyOrPlaceholder == "" {
		keyOrPlaceholder = openAICompatNoKey
	}

	switch providerKey {
	case "copilot":
		// Copilot CLI BYOK: setting COPILOT_PROVIDER_BASE_URL switches it to
		// the endpoint (no GitHub model routing, no GitHub auth needed).
		env["COPILOT_PROVIDER_BASE_URL"] = baseURL
		env["COPILOT_PROVIDER_TYPE"] = "openai"
		env["COPILOT_PROVIDER_API_KEY"] = key // optional for keyless endpoints
		env["COPILOT_PROVIDER_BEARER_TOKEN"] = ""
		env["COPILOT_PROVIDER_WIRE_API"] = ""
		env["COPILOT_MODEL"] = model
	case "qwen":
		// Qwen Code reads the OpenAI-compatible env trio.
		applyOpenAICompatEnv(env, cfg, vendor, baseURL, model)
	case "codex":
		// Codex gets the endpoint through -c model_provider flags (see
		// AppendEndpointFlags); here only the key it reads via env_key, and a
		// blank OPENAI_BASE_URL so the gateway provider flags stay off.
		env["OPENAI_API_KEY"] = keyOrPlaceholder
		env["OPENAI_BASE_URL"] = ""
	case "claude":
		// Claude Code: ANTHROPIC_AUTH_TOKEN is always set so the user's
		// subscription login is never sent to the endpoint; ANTHROPIC_API_KEY
		// and the gateway header are blanked for the same reason.
		env["ANTHROPIC_BASE_URL"] = endpointRootURL(baseURL)
		env["ANTHROPIC_AUTH_TOKEN"] = keyOrPlaceholder
		env["ANTHROPIC_API_KEY"] = ""
		env["ANTHROPIC_CUSTOM_HEADERS"] = ""
		// Every model tier maps to the endpoint's model so background and
		// sub-agent calls never ask the endpoint for a Claude model.
		for _, name := range []string{
			"ANTHROPIC_MODEL",
			"ANTHROPIC_DEFAULT_HAIKU_MODEL",
			"ANTHROPIC_DEFAULT_SONNET_MODEL",
			"ANTHROPIC_DEFAULT_OPUS_MODEL",
			"ANTHROPIC_DEFAULT_FABLE_MODEL",
		} {
			env[name] = model
		}
	case "gemini":
		// Gemini CLI: same variables the gateway uses.
		env["GOOGLE_GEMINI_BASE_URL"] = endpointRootURL(baseURL)
		env["GEMINI_API_KEY"] = keyOrPlaceholder
	}
	return env
}

// endpointRootURL returns the base URL for harnesses that append their own
// versioned path: Claude Code adds /v1/messages and Gemini CLI adds
// /v1beta/..., so the documented form ending in /v1 would otherwise become
// /v1/v1/messages. One trailing "/v1" (and trailing "/") is removed; any other
// URL passes through unchanged. The gateway wiring shapes these the same way
// (root for claude/gemini, root + /v1 for the OpenAI-style harnesses).
func endpointRootURL(baseURL string) string {
	return strings.TrimSuffix(strings.TrimRight(baseURL, "/"), "/v1")
}

// codexEndpointProviderID names the temporary Codex model provider that
// points at a compatible endpoint.
const codexEndpointProviderID = "vibeflow-endpoint"

// AppendEndpointFlags appends the launch flags a harness needs on top of its
// env to use a compatible endpoint. Must run before the init-prompt append.
//   - codex: a temporary model provider (Responses API, key read from
//     OPENAI_API_KEY via env_key — never on the command line).
//   - qwen: --auth-type openai, so a fresh install does not stop on its
//     interactive sign-in picker.
func AppendEndpointFlags(command, providerKey, baseURL string) string {
	switch providerKey {
	case "codex":
		for _, flag := range []string{
			codexConfigStringArg("model_provider", codexEndpointProviderID),
			codexConfigStringArg("model_providers."+codexEndpointProviderID+".name", "Compatible endpoint"),
			codexConfigStringArg("model_providers."+codexEndpointProviderID+".base_url", baseURL),
			codexConfigStringArg("model_providers."+codexEndpointProviderID+".env_key", "OPENAI_API_KEY"),
			codexConfigStringArg("model_providers."+codexEndpointProviderID+".wire_api", "responses"),
			codexConfigBoolArg("model_providers."+codexEndpointProviderID+".supports_websockets", false),
		} {
			command += " -c " + flag
		}
	case "qwen":
		command += " --auth-type openai"
	}
	return command
}
