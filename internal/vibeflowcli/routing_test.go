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
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestEndpointSupportByHarness(t *testing.T) {
	for provider, want := range map[string]string{
		"copilot": "OpenAI-compatible",
		"qwen":    "OpenAI-compatible",
		"codex":   "OpenAI-compatible, Responses API",
		"claude":  "Anthropic-compatible, Messages API",
		"gemini":  "Gemini-compatible",
	} {
		got, ok := EndpointAPIFormat(provider)
		if !ok || got != want {
			t.Errorf("EndpointAPIFormat(%q) = %q, %v; want %q, true", provider, got, ok, want)
		}
	}
	// Harnesses with no custom-endpoint mechanism.
	for _, provider := range []string{"cursor", "kiro"} {
		if providerSupportsEndpoint(provider) {
			t.Errorf("%s must not support endpoint routing", provider)
		}
	}
}

func TestResolveRouting(t *testing.T) {
	tests := []struct {
		explicit string
		gateway  bool
		want     string
	}{
		{"", false, RoutingDirect},
		{"", true, RoutingGateway},
		{RoutingEndpoint, false, RoutingEndpoint},
		{RoutingDirect, true, RoutingDirect}, // explicit wins
	}
	for _, tt := range tests {
		if got := resolveRouting(tt.explicit, tt.gateway); got != tt.want {
			t.Errorf("resolveRouting(%q, %v) = %q, want %q", tt.explicit, tt.gateway, got, tt.want)
		}
	}
	// Records written before the routing field existed.
	if got := routingForMeta(SessionMeta{Provider: "claude", LLMGatewayEnabled: true}); got != RoutingGateway {
		t.Errorf("legacy gateway record → %q, want gateway", got)
	}
	if got := routingForMeta(SessionMeta{Provider: "codex"}); got != RoutingDirect {
		t.Errorf("legacy direct record → %q, want direct", got)
	}
}

func TestRoutingSuppliesKey(t *testing.T) {
	if !routingSuppliesKey(RoutingEndpoint, "gemini", "GEMINI_API_KEY") || !routingSuppliesKey(RoutingEndpoint, "qwen", "OPENAI_API_KEY") {
		t.Error("the endpoint key must replace the harness's own provider key")
	}
	// The gateway issues its own key for the harnesses it wires.
	if !routingSuppliesKey(RoutingGateway, "qwen", "OPENAI_API_KEY") || !routingSuppliesKey(RoutingGateway, "gemini", "GEMINI_API_KEY") {
		t.Error("gateway routing supplies the provider key")
	}
	if routingSuppliesKey(RoutingGateway, "codex", "MCP_TOKEN") {
		t.Error("the gateway does not supply the codex MCP bearer token")
	}
	if routingSuppliesKey(RoutingDirect, "gemini", "GEMINI_API_KEY") || routingSuppliesKey(RoutingShell, "qwen", "OPENAI_API_KEY") {
		t.Error("direct and shell routing still need the provider's own key")
	}
	if routingSuppliesKey(RoutingEndpoint, "codex", "MCP_TOKEN") {
		t.Error("the MCP bearer token is still required with an endpoint")
	}
}

func TestBuildEndpointEnv(t *testing.T) {
	const url, model = "http://llm-proxy.local:4000/v1", "some-model"
	// Claude Code and Gemini CLI append their own versioned path.
	const root = "http://llm-proxy.local:4000"
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "")
	withKey := &Config{SavedEnvVars: map[string]string{"OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR": "sk-vendor"}}
	keyless := &Config{}

	tests := []struct {
		provider string
		cfg      *Config
		want     map[string]string
	}{
		{"copilot", withKey, map[string]string{
			"COPILOT_PROVIDER_BASE_URL": url, "COPILOT_PROVIDER_TYPE": "openai", "COPILOT_PROVIDER_API_KEY": "sk-vendor",
			"COPILOT_PROVIDER_BEARER_TOKEN": "", "COPILOT_PROVIDER_WIRE_API": "", "COPILOT_MODEL": model,
		}},
		{"copilot", keyless, map[string]string{ // key is optional for copilot
			"COPILOT_PROVIDER_BASE_URL": url, "COPILOT_PROVIDER_TYPE": "openai", "COPILOT_PROVIDER_API_KEY": "",
			"COPILOT_PROVIDER_BEARER_TOKEN": "", "COPILOT_PROVIDER_WIRE_API": "", "COPILOT_MODEL": model,
		}},
		{"qwen", keyless, map[string]string{"OPENAI_BASE_URL": url, "OPENAI_MODEL": model, "OPENAI_API_KEY": openAICompatNoKey}},
		{"codex", withKey, map[string]string{"OPENAI_API_KEY": "sk-vendor", "OPENAI_BASE_URL": ""}},
		{"codex", keyless, map[string]string{"OPENAI_API_KEY": openAICompatNoKey, "OPENAI_BASE_URL": ""}},
		{"claude", keyless, map[string]string{ // placeholder so the subscription login is never sent
			"ANTHROPIC_BASE_URL": root, "ANTHROPIC_AUTH_TOKEN": openAICompatNoKey, "ANTHROPIC_API_KEY": "", "ANTHROPIC_CUSTOM_HEADERS": "",
			"ANTHROPIC_MODEL": model, "ANTHROPIC_DEFAULT_HAIKU_MODEL": model, "ANTHROPIC_DEFAULT_SONNET_MODEL": model,
			"ANTHROPIC_DEFAULT_OPUS_MODEL": model, "ANTHROPIC_DEFAULT_FABLE_MODEL": model,
		}},
		{"gemini", withKey, map[string]string{"GOOGLE_GEMINI_BASE_URL": root, "GEMINI_API_KEY": "sk-vendor"}},
		{"cursor", withKey, map[string]string{}}, // unsupported: nothing set
	}
	for _, tt := range tests {
		got := BuildEndpointEnv(tt.provider, tt.cfg, "example-vendor", url, model)
		if !reflect.DeepEqual(got, tt.want) {
			t.Errorf("BuildEndpointEnv(%s) =\n  %v\nwant\n  %v", tt.provider, got, tt.want)
		}
	}
}

func TestEndpointRootURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://host:4000/v1":     "http://host:4000", // documented form
		"http://host:4000/v1/":    "http://host:4000",
		"http://host:4000":        "http://host:4000", // root passes through
		"http://host:4000/":       "http://host:4000",
		"https://h/api/anthropic": "https://h/api/anthropic", // other paths untouched
		"https://h/proxy/v1":      "https://h/proxy",
		"https://h/v1beta":        "https://h/v1beta",
		"https://h/v1/v1":         "https://h/v1", // only one segment removed
	} {
		if got := endpointRootURL(in); got != want {
			t.Errorf("endpointRootURL(%q) = %q, want %q", in, got, want)
		}
	}
	// Harnesses that take the /v1 URL as-is keep it.
	for _, p := range []string{"copilot", "qwen"} {
		for _, v := range BuildEndpointEnv(p, &Config{}, "", "http://host:4000/v1", "m") {
			if strings.HasPrefix(v, "http://host:4000") && v != "http://host:4000/v1" {
				t.Errorf("%s base URL = %q, want the /v1 URL unchanged", p, v)
			}
		}
	}
}

func TestAppendEndpointFlags(t *testing.T) {
	const url = "http://llm-proxy.local:4000/v1"
	codex := AppendEndpointFlags("codex", "codex", url)
	for _, want := range []string{
		`-c 'model_provider="vibeflow-endpoint"'`,
		`-c 'model_providers.vibeflow-endpoint.base_url="http://llm-proxy.local:4000/v1"'`,
		`-c 'model_providers.vibeflow-endpoint.env_key="OPENAI_API_KEY"'`,
		`-c 'model_providers.vibeflow-endpoint.wire_api="responses"'`,
		`-c model_providers.vibeflow-endpoint.supports_websockets=false`,
	} {
		if !strings.Contains(codex, want) {
			t.Errorf("codex flags missing %s\n%s", want, codex)
		}
	}
	if got := AppendEndpointFlags("qwen --yolo", "qwen", url); got != "qwen --yolo --auth-type openai" {
		t.Errorf("qwen flags = %q", got)
	}
	// Harnesses configured purely through env get no extra flags.
	for _, p := range []string{"claude", "copilot", "gemini", "cursor"} {
		if got := AppendEndpointFlags(p, p, url); got != p {
			t.Errorf("%s flags = %q, want unchanged", p, got)
		}
	}
}

// TestRestartSession_EndpointRoutingPerHarness restarts an endpoint-routed
// session on each supported harness in a real tmux server with a fake agent
// that records argv and its endpoint-related env. Real-looking keys and URLs
// are exported in the test (and so in the tmux server's env) and must never
// reach the session.
func TestRestartSession_EndpointRoutingPerHarness(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("MCP_TOKEN", "")
	// Shell values that must be masked.
	for k, v := range map[string]string{
		"OPENAI_API_KEY": "sk-shell-openai", "OPENAI_BASE_URL": "https://api.openai.com/v1",
		"ANTHROPIC_API_KEY": "sk-shell-anthropic", "ANTHROPIC_AUTH_TOKEN": "sk-shell-auth", "ANTHROPIC_BASE_URL": "https://api.anthropic.com",
		"COPILOT_PROVIDER_API_KEY": "sk-shell-copilot", "COPILOT_PROVIDER_BEARER_TOKEN": "sk-shell-bearer",
		"GEMINI_API_KEY": "sk-shell-gemini",
	} {
		t.Setenv(k, v)
	}
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "sk-vendor")

	tm := NewTmuxManager(fmt.Sprintf("vftest-routing-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-agent")
	vars := []string{
		"OPENAI_API_KEY", "OPENAI_BASE_URL", "OPENAI_MODEL",
		"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY", "ANTHROPIC_MODEL",
		"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN", "COPILOT_MODEL",
		"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY",
	}
	script := "#!/bin/sh\n{ printf 'ARG=%s\\n' \"$@\"; "
	for _, v := range vars {
		script += fmt.Sprintf("printf '%s=%%s\\n' \"$%s\"; ", v, v)
	}
	script += "} > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	for _, p := range []string{"claude", "codex", "copilot", "qwen", "gemini"} {
		prov := cfg.Providers[p]
		prov.Binary = binary
		cfg.Providers[p] = prov
	}

	const url = "http://llm-proxy.local:4000/v1"
	const root = "http://llm-proxy.local:4000" // claude/gemini append their own /v1 path
	tests := []struct {
		provider string
		want     []string
		reads    []string // endpoint/credential vars this harness reads: no shell value may survive
	}{
		{"copilot", []string{"COPILOT_PROVIDER_BASE_URL=" + url, "COPILOT_PROVIDER_API_KEY=sk-vendor", "COPILOT_PROVIDER_BEARER_TOKEN=\n", "COPILOT_MODEL=some-model", "ARG=--model\nARG=some-model"},
			[]string{"COPILOT_PROVIDER_BASE_URL", "COPILOT_PROVIDER_API_KEY", "COPILOT_PROVIDER_BEARER_TOKEN"}},
		{"claude", []string{"ANTHROPIC_BASE_URL=" + root + "\n", "ANTHROPIC_AUTH_TOKEN=sk-vendor", "ANTHROPIC_API_KEY=\n", "ANTHROPIC_MODEL=some-model"},
			[]string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "ANTHROPIC_API_KEY"}},
		{"codex", []string{"OPENAI_API_KEY=sk-vendor", "OPENAI_BASE_URL=\n", `ARG=model_providers.vibeflow-endpoint.base_url="` + url + `"`, "ARG=-m\nARG=some-model"},
			[]string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}},
		{"qwen", []string{"OPENAI_API_KEY=sk-vendor", "OPENAI_BASE_URL=" + url, "ARG=--auth-type\nARG=openai", "ARG=--openai-base-url\nARG=" + url, "ARG=--model\nARG=some-model"},
			[]string{"OPENAI_API_KEY", "OPENAI_BASE_URL"}},
		{"gemini", []string{"GOOGLE_GEMINI_BASE_URL=" + root + "\n", "GEMINI_API_KEY=sk-vendor"},
			[]string{"GOOGLE_GEMINI_BASE_URL", "GEMINI_API_KEY"}},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			_ = os.Remove(record)
			meta := SessionMeta{
				Name: "rt-" + tt.provider, Provider: tt.provider, WorkingDir: repo, Branch: "main",
				SessionType: "vanilla", Routing: RoutingEndpoint,
				Vendor: "example-vendor", BaseURL: url, Model: "some-model",
			}
			meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)
			updated, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg))
			if err != nil {
				t.Fatal(err)
			}
			var got string
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				if data, err := os.ReadFile(record); err == nil && strings.Contains(string(data), "GEMINI_API_KEY=") {
					got = string(data)
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("record missing %q", want)
				}
			}
			// No shell secret or default endpoint may survive in a variable
			// this harness reads, and the vendor key is never on the command line.
			for _, line := range strings.Split(got, "\n") {
				name, val, _ := strings.Cut(line, "=")
				for _, v := range tt.reads {
					if name == v && (strings.HasPrefix(val, "sk-shell-") || strings.Contains(val, "api.openai.com") || strings.Contains(val, "api.anthropic.com")) {
						t.Errorf("%s kept the shell value", v)
					}
				}
			}
			if strings.Contains(got, "ARG=sk-vendor") {
				t.Error("vendor key passed on the command line")
			}
			if updated.Routing != RoutingEndpoint {
				t.Errorf("updated routing = %q, want endpoint", updated.Routing)
			}
		})
	}
}
