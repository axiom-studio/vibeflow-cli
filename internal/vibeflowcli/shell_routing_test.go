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
	"sort"
	"strings"
	"testing"
	"time"

	"github.com/spf13/cobra"
)

// clearShellEndpoints blanks every endpoint and related variable a harness
// reads from the shell, so tests don't depend on the host's environment.
func clearShellEndpoints(t *testing.T) {
	t.Helper()
	for _, se := range shellEndpoints {
		t.Setenv(se.urlVar, "")
		for _, name := range se.related {
			t.Setenv(name, "")
		}
	}
}

func TestResolveShellEndpoint(t *testing.T) {
	clearShellEndpoints(t)
	for provider, urlVar := range map[string]string{
		"claude": "ANTHROPIC_BASE_URL", "copilot": "COPILOT_PROVIDER_BASE_URL", "codex": "OPENAI_BASE_URL",
		"qwen": "OPENAI_BASE_URL", "gemini": "GOOGLE_GEMINI_BASE_URL",
	} {
		t.Run(provider, func(t *testing.T) {
			if _, err := ResolveShellEndpoint(provider); err == nil || !strings.Contains(err.Error(), urlVar+" is not set") {
				t.Errorf("unset: err = %v, want %s is not set", err, urlVar)
			}
			t.Setenv(urlVar, " http://proxy.local:4000/v1 ")
			if got, err := ResolveShellEndpoint(provider); err != nil || got != "http://proxy.local:4000/v1" {
				t.Errorf("set: got %q, %v", got, err)
			}
			for _, bad := range []string{"https://u:S3cret@h/v1", "https://h/v1?key=S3cret", "localhost:4000"} {
				t.Setenv(urlVar, bad)
				_, err := ResolveShellEndpoint(provider)
				if err == nil || strings.Contains(err.Error(), "S3cret") {
					t.Errorf("%q: err = %v, want a rejection that doesn't echo the secret", bad, err)
				}
			}
		})
	}
	for _, provider := range []string{"cursor", "kiro"} {
		if _, err := ResolveShellEndpoint(provider); err == nil || !strings.Contains(err.Error(), "cannot use an endpoint from the shell") {
			t.Errorf("%s: err = %v", provider, err)
		}
	}
}

func TestBuildShellEndpointEnv(t *testing.T) {
	clearShellEndpoints(t)
	const url = "http://proxy.local:4000"
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-shell-auth")
	t.Setenv("ANTHROPIC_MODEL", "shell-model")
	t.Setenv("COPILOT_PROVIDER_API_KEY", "sk-shell-copilot")
	t.Setenv("OPENAI_API_KEY", "sk-shell-openai")
	tests := []struct {
		provider string
		want     map[string]string
	}{
		// Only the related vars that are set are passed; unset ones stay unset.
		{"claude", map[string]string{"ANTHROPIC_BASE_URL": url, "ANTHROPIC_AUTH_TOKEN": "sk-shell-auth", "ANTHROPIC_MODEL": "shell-model"}},
		{"copilot", map[string]string{"COPILOT_PROVIDER_BASE_URL": url, "COPILOT_PROVIDER_API_KEY": "sk-shell-copilot"}},
		// Codex takes the URL through flags; OPENAI_BASE_URL stays blank.
		{"codex", map[string]string{"OPENAI_BASE_URL": "", "OPENAI_API_KEY": "sk-shell-openai"}},
		{"qwen", map[string]string{"OPENAI_BASE_URL": url, "OPENAI_API_KEY": "sk-shell-openai"}},
		{"gemini", map[string]string{"GOOGLE_GEMINI_BASE_URL": url}},
		{"cursor", map[string]string{}},
	}
	for _, tt := range tests {
		if got := BuildShellEndpointEnv(tt.provider, url); !reflect.DeepEqual(got, tt.want) {
			t.Errorf("%s: env keys %v, want %v", tt.provider, sortedKeys(got), sortedKeys(tt.want))
		}
	}
	// A keyless codex endpoint still gets a value for env_key.
	t.Setenv("OPENAI_API_KEY", "")
	if got := BuildShellEndpointEnv("codex", url)["OPENAI_API_KEY"]; got != openAICompatNoKey {
		t.Errorf("keyless codex OPENAI_API_KEY = %q, want the placeholder", got)
	}
	if got := AppendShellEndpointFlags("codex", "codex", url); !strings.Contains(got, `model_providers.vibeflow-endpoint.base_url="`+url+`"`) {
		t.Errorf("codex flags = %q", got)
	}
	for _, p := range []string{"claude", "copilot", "qwen", "gemini"} {
		if got := AppendShellEndpointFlags(p, p, url); got != p {
			t.Errorf("%s flags = %q, want unchanged", p, got)
		}
	}
}

// sortedKeys lists a map's keys (values may be secrets, so tests report keys).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func TestClearShellEndpointEnv(t *testing.T) {
	clearShellEndpoints(t)
	// Nothing detected → nothing added, so direct launches stay as before.
	for _, p := range []string{"claude", "codex", "copilot", "qwen", "gemini", "cursor"} {
		if got := ClearShellEndpointEnv(p); len(got) != 0 {
			t.Errorf("%s with no shell endpoint: %v", p, got)
		}
	}
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://proxy.local/v1")
	if got := ClearShellEndpointEnv("copilot"); !reflect.DeepEqual(got, map[string]string{"COPILOT_PROVIDER_BASE_URL": ""}) {
		t.Errorf("copilot = %v, want the base URL blanked", got)
	}
	// Claude/Codex/Gemini are already cleared by ClearLLMGatewayEnv; Qwen's
	// direct flow sets its own endpoint.
	t.Setenv("ANTHROPIC_BASE_URL", "http://proxy.local")
	if got := ClearShellEndpointEnv("claude"); len(got) != 0 {
		t.Errorf("claude = %v, want nothing extra", got)
	}
}

func TestDisplayEndpointURL(t *testing.T) {
	for in, want := range map[string]string{
		"http://proxy.local:4000/v1":             "http://proxy.local:4000/v1",
		"https://u:S3cret@proxy.local/v1":        "https://proxy.local/v1",
		"https://proxy.local/v1?key=S3cret#frag": "https://proxy.local/v1",
	} {
		if got := displayEndpointURL(in); got != want {
			t.Errorf("displayEndpointURL(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestWizard_DetectedEndpointIsOfferedAndPreselected(t *testing.T) {
	w := endpointWizardFixture(t, &Config{}, "claude")
	t.Setenv("ANTHROPIC_BASE_URL", "http://proxy.local:4000")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-shell-secret")
	w, _ = w.advance()
	if got := strings.Join(routingModes(w), ","); got != "shell,direct,endpoint" {
		t.Fatalf("routing modes = %s, want shell,direct,endpoint", got)
	}
	if opt := w.routingOptions()[w.cursor]; opt.mode != RoutingShell {
		t.Errorf("cursor on %q, want the detected endpoint pre-selected", opt.mode)
	}
	view := w.View()
	for _, want := range []string{"Use detected endpoint", "ANTHROPIC_BASE_URL = http://proxy.local:4000", "ignores the detected endpoint"} {
		if !strings.Contains(view, want) {
			t.Errorf("routing view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "sk-shell-secret") {
		t.Error("routing view shows the key")
	}

	// Keeping it continues to branch; confirm shows the endpoint, not the key.
	w, _ = w.advance()
	if w.step != StepBranch || w.routing != RoutingShell {
		t.Fatalf("step=%v routing=%q, want branch with shell routing", w.step, w.routing)
	}
	w.step = StepConfirm
	w.selectedBranch = 1
	w.worktreeOpts = []string{"Current directory"}
	w.permissionOpts = []string{"Yes", "No"}
	view = w.View()
	for _, want := range []string{"Routing:       Detected endpoint", "Endpoint:      http://proxy.local:4000 (from ANTHROPIC_BASE_URL)"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "sk-shell-secret") {
		t.Error("confirm view shows the key")
	}
	w, _ = w.advance()
	if !w.done || w.result.Routing != RoutingShell {
		t.Errorf("result routing = %q, want shell", w.result.Routing)
	}
}

func TestWizard_DetectedEndpointChoiceIsKeptOnBack(t *testing.T) {
	// Once the user picks direct, going back to Routing keeps that choice
	// instead of pre-selecting the detected endpoint again.
	w := endpointWizardFixture(t, &Config{}, "copilot")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://proxy.local:4000/v1")
	w, _ = w.advance()
	w = chooseRouting(t, w, RoutingDirect)
	w, _ = w.goBack()
	if opt := w.routingOptions()[w.cursor]; opt.mode != RoutingDirect {
		t.Errorf("cursor on %q after back, want direct", opt.mode)
	}
}

func TestWizard_UnusableDetectedEndpointIsDisabled(t *testing.T) {
	w := endpointWizardFixture(t, &Config{}, "claude")
	t.Setenv("ANTHROPIC_BASE_URL", "https://user:S3cret@proxy.local")
	w, _ = w.advance()
	opts := w.routingOptions()
	if opts[0].mode != RoutingShell || opts[0].enabled || !strings.Contains(opts[0].note, "contains credentials") {
		t.Fatalf("first option = %+v, want a disabled detected endpoint", opts[0])
	}
	if opt := opts[w.cursor]; opt.mode != RoutingDirect {
		t.Errorf("cursor on %q, want direct", opt.mode)
	}
	if strings.Contains(w.View(), "S3cret") {
		t.Error("routing view shows the credential")
	}
	w.cursor = 0
	if w, _ = w.advance(); w.step != StepLLMGateway {
		t.Errorf("disabled option was accepted: step=%v", w.step)
	}
}

func TestWizard_QwenDetectedEndpointSkipsPresetsAndAsksForMissingKey(t *testing.T) {
	w := endpointWizardFixture(t, &Config{}, "qwen")
	t.Setenv("OPENAI_BASE_URL", "http://proxy.local:4000/v1")
	w, _ = w.advance()
	// OPENAI_API_KEY is not set anywhere: shell routing uses the harness's
	// own key, so it is asked for, then the presets step is skipped.
	w = chooseRouting(t, w, RoutingShell)
	if w.step != StepEnvToken || w.envTokenVarName != "OPENAI_API_KEY" {
		t.Fatalf("step=%v var=%q, want the OPENAI_API_KEY prompt", w.step, w.envTokenVarName)
	}
	w = typeText(w, "sk-typed")
	w = press(w, keyEnter)
	if w.step != StepBranch {
		t.Errorf("after key: step = %v, want StepBranch (no qwen presets)", w.step)
	}
}

func TestExecuteLaunch_ShellRoutingNeedsTheEndpoint(t *testing.T) {
	clearShellEndpoints(t)
	m := Model{config: &Config{}}
	msg := m.executeLaunch(WizardResult{ProviderKey: "claude", Routing: RoutingShell, WorktreeChoice: WorktreeCurrent})
	if sm, ok := msg.(sessionsMsg); !ok || sm.err == nil || !strings.Contains(sm.err.Error(), "ANTHROPIC_BASE_URL is not set") {
		t.Errorf("executeLaunch = %#v, want a missing-endpoint error", msg)
	}
}

func TestValidateRoutingFlags_Shell(t *testing.T) {
	clearShellEndpoints(t)
	if err := validateRoutingFlags("claude", RoutingShell, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL is not set") {
		t.Errorf("unset: err = %v", err)
	}
	t.Setenv("ANTHROPIC_BASE_URL", "http://proxy.local:4000")
	if err := validateRoutingFlags("claude", RoutingShell, false, "", "", ""); err != nil {
		t.Errorf("set: %v", err)
	}
	if err := validateRoutingFlags("claude", RoutingShell, false, "http://other/v1", "", ""); err == nil || !strings.Contains(err.Error(), "only valid with --routing endpoint") {
		t.Errorf("--base-url with shell: err = %v", err)
	}
	if err := validateRoutingFlags("claude", RoutingShell, true, "", "", ""); err == nil || !strings.Contains(err.Error(), "conflicts") {
		t.Errorf("--llm-gateway with shell: err = %v", err)
	}
	if err := validateRoutingFlags("cursor", RoutingShell, false, "", "", ""); err == nil || !strings.Contains(err.Error(), "cannot use an endpoint from the shell") {
		t.Errorf("cursor: err = %v", err)
	}
}

// shellTestAgent writes a fake agent that records argv and the endpoint
// variables it sees, and returns the record reader.
func shellTestAgent(t *testing.T, dir string) (binary string, read func() string) {
	t.Helper()
	record := filepath.Join(dir, "agent-record")
	binary = filepath.Join(dir, "fake-agent")
	script := "#!/bin/sh\n{ printf 'ARG=%s\\n' \"$@\"; "
	for _, v := range []string{"ANTHROPIC_BASE_URL", "ANTHROPIC_AUTH_TOKEN", "COPILOT_PROVIDER_BASE_URL", "OPENAI_BASE_URL", "OPENAI_API_KEY"} {
		script += fmt.Sprintf("printf '%s=%%s\\n' \"$%s\"; ", v, v)
	}
	script += "echo END; } > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	read = func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(record); err == nil && strings.Contains(string(data), "END") {
				_ = os.Remove(record)
				return string(data)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fake agent did not record its launch")
		return ""
	}
	return binary, read
}

// TestLaunchCmd_ShellRouting runs headless launches on a real tmux server
// whose env (like a user's shell) exports endpoints for Claude Code, Codex
// and Copilot, and checks what each routing choice hands the agent.
func TestLaunchCmd_ShellRouting(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	t.Chdir(repo)
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("MCP_TOKEN", "")
	clearShellEndpoints(t)
	t.Setenv("ANTHROPIC_BASE_URL", "http://claude-proxy.local:4000")
	t.Setenv("ANTHROPIC_AUTH_TOKEN", "sk-shell-auth")
	t.Setenv("OPENAI_BASE_URL", "http://codex-proxy.local:4000/v1")
	t.Setenv("OPENAI_API_KEY", "sk-shell-openai")
	t.Setenv("COPILOT_PROVIDER_BASE_URL", "http://copilot-proxy.local:4000/v1")

	socket := fmt.Sprintf("vftest-shell-launch-%d-%d", os.Getpid(), time.Now().UnixNano())
	t.Cleanup(func() { _, _ = NewTmuxManager(socket).run("kill-server") })
	binary, read := shellTestAgent(t, state)
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	for _, p := range []string{"claude", "codex", "copilot"} {
		prov := cfg.Providers[p]
		prov.Binary = binary
		cfg.Providers[p] = prov
	}
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	launch := func(args ...string) string {
		t.Helper()
		root := &cobra.Command{Use: "vibeflow"}
		root.PersistentFlags().String("config", "", "")
		root.PersistentFlags().String("mcp", "", "")
		root.AddCommand(launchCmd())
		root.SilenceErrors, root.SilenceUsage = true, true
		root.SetArgs(append([]string{"launch"}, args...))
		if err := root.Execute(); err != nil {
			t.Fatal(err)
		}
		return read()
	}

	tests := []struct {
		name    string
		args    []string
		want    []string
		notWant []string
	}{
		{"claude shell keeps the endpoint and token", []string{"--provider", "claude", "--routing", "shell"},
			[]string{"ANTHROPIC_BASE_URL=http://claude-proxy.local:4000\n", "ANTHROPIC_AUTH_TOKEN=sk-shell-auth\n"}, nil},
		{"claude default clears the endpoint as before", []string{"--provider", "claude"},
			[]string{"ANTHROPIC_BASE_URL=\n"}, nil},
		{"codex shell uses the URL through a model provider", []string{"--provider", "codex", "--routing", "shell"},
			[]string{`ARG=model_providers.vibeflow-endpoint.base_url="http://codex-proxy.local:4000/v1"`, "OPENAI_BASE_URL=\n", "OPENAI_API_KEY=sk-shell-openai\n"},
			[]string{"ARG=sk-shell-openai", "llm-gateway"}},
		{"copilot default keeps BYOK as before", []string{"--provider", "copilot"},
			[]string{"COPILOT_PROVIDER_BASE_URL=http://copilot-proxy.local:4000/v1\n"}, nil},
		{"copilot explicit direct clears BYOK", []string{"--provider", "copilot", "--routing", "direct"},
			[]string{"COPILOT_PROVIDER_BASE_URL=\n"}, nil},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := launch(tt.args...)
			for _, want := range tt.want {
				if !strings.Contains(got, want) {
					t.Errorf("agent record missing %q:\n%s", want, got)
				}
			}
			for _, bad := range tt.notWant {
				if strings.Contains(got, bad) {
					t.Errorf("agent record contains %q:\n%s", bad, got)
				}
			}
		})
	}

	// The routing is recorded so restart reconnects the same way.
	metas, err := NewStore().List()
	if err != nil {
		t.Fatal(err)
	}
	var shellCount int
	for _, m := range metas {
		if m.Routing == RoutingShell {
			shellCount++
		}
	}
	if shellCount != 2 {
		t.Errorf("sessions recorded with shell routing = %d, want 2", shellCount)
	}
}

func TestRestartSession_ShellRouting(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("MCP_TOKEN", "")
	clearShellEndpoints(t)
	tm := NewTmuxManager(fmt.Sprintf("vftest-shell-restart-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	binary, read := shellTestAgent(t, state)
	cfg := DefaultConfig()
	prov := cfg.Providers["claude"]
	prov.Binary = binary
	cfg.Providers["claude"] = prov
	meta := SessionMeta{Name: "shell-rt", Provider: "claude", WorkingDir: repo, Branch: "main", SessionType: "vanilla", Routing: RoutingShell}
	meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)

	// The endpoint is gone from this environment: fail before launching.
	if _, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg)); err == nil || !strings.Contains(err.Error(), "ANTHROPIC_BASE_URL is not set") {
		t.Fatalf("err = %v, want missing-endpoint error", err)
	}
	if tm.HasSession(meta.TmuxSession) {
		t.Fatal("a session was launched despite the error")
	}

	t.Setenv("ANTHROPIC_BASE_URL", "http://claude-proxy.local:4000")
	updated, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg))
	if err != nil {
		t.Fatal(err)
	}
	if got := read(); !strings.Contains(got, "ANTHROPIC_BASE_URL=http://claude-proxy.local:4000\n") {
		t.Errorf("restart did not pass the shell endpoint:\n%s", got)
	}
	if updated.Routing != RoutingShell {
		t.Errorf("updated routing = %q, want shell", updated.Routing)
	}
}
