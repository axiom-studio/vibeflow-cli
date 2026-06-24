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
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/spf13/cobra"
)

func readJSONFile(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var m map[string]any
	if err := json.Unmarshal(data, &m); err != nil {
		t.Fatalf("parse %s: %v\n%s", path, err, data)
	}
	return m
}

func mcpServerEntry(t *testing.T, root map[string]any, name string) map[string]any {
	t.Helper()
	servers, ok := root["mcpServers"].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers missing or wrong type: %T", root["mcpServers"])
	}
	entry, ok := servers[name].(map[string]any)
	if !ok {
		t.Fatalf("mcpServers[%q] missing or wrong type: %T", name, servers[name])
	}
	return entry
}

// --- JSON mcpServers writers ---

func TestWriteJSONMCPServer_PreservesSiblingsAndKeys(t *testing.T) {
	path := filepath.Join(t.TempDir(), ".claude.json")
	seed := map[string]any{
		"numStartups": 3,
		"mcpServers": map[string]any{
			"figma":    map[string]any{"type": "http", "url": "https://figma"},
			"vibeflow": map[string]any{"stale": "old-entry"},
		},
		"projects": map[string]any{"/p": map[string]any{}},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	entry := jsonHTTPEntry("http", false)("https://cloud.example/rest/v1/vibeflow/mcp", "secret")
	action, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatalf("writeJSONMCPServer: %v", err)
	}
	if action != "updated" {
		t.Fatalf("action = %q, want updated", action)
	}

	root := readJSONFile(t, path)
	if got := root["numStartups"]; !equalJSON(got, 3) {
		t.Errorf("numStartups clobbered: %v", got)
	}
	if _, ok := root["projects"].(map[string]any); !ok {
		t.Errorf("projects key lost")
	}
	if figma := mcpServerEntry(t, root, "figma"); figma["url"] != "https://figma" {
		t.Errorf("sibling figma clobbered: %v", figma)
	}
	vibe := mcpServerEntry(t, root, "vibeflow")
	if !equalJSON(vibe, entry) {
		t.Errorf("vibeflow entry = %v, want %v", vibe, entry)
	}
	headers, _ := vibe["headers"].(map[string]any)
	if headers["Authorization"] != "Bearer ${MCP_TOKEN}" {
		t.Errorf("Authorization = %v, want Bearer ${MCP_TOKEN}", headers["Authorization"])
	}
}

func TestWriteJSONMCPServer_CreatesFileWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	entry := jsonHTTPEntry("streamable-http", true)("https://cloud.example/rest/v1/vibeflow/mcp", "")

	action, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatalf("writeJSONMCPServer: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	vibe := mcpServerEntry(t, readJSONFile(t, path), "vibeflow")
	if vibe["type"] != "streamable-http" {
		t.Errorf("type = %v, want streamable-http", vibe["type"])
	}
	if !equalJSON(vibe["timeout"], mcpClientTimeoutMS) {
		t.Errorf("timeout = %v, want %d", vibe["timeout"], mcpClientTimeoutMS)
	}
}

func TestWriteJSONMCPServer_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	entry := jsonHTTPEntry("streamable-http", true)("https://cloud.example/rest/v1/vibeflow/mcp", "")

	if _, err := writeJSONMCPServer(path, "vibeflow", entry); err != nil {
		t.Fatal(err)
	}
	action, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("second write action = %q, want unchanged", action)
	}
}

func TestRemoveJSONMCPServer_RemovesOnlyVibeflow(t *testing.T) {
	path := filepath.Join(t.TempDir(), "mcp.json")
	seed := map[string]any{
		"mcpServers": map[string]any{
			"figma":    map[string]any{"url": "https://figma"},
			"vibeflow": map[string]any{"url": "https://vibeflow"},
		},
	}
	data, _ := json.MarshalIndent(seed, "", "  ")
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := removeJSONMCPServer(path, "vibeflow")
	if err != nil {
		t.Fatal(err)
	}
	if action != "removed" {
		t.Fatalf("action = %q, want removed", action)
	}
	root := readJSONFile(t, path)
	servers, _ := root["mcpServers"].(map[string]any)
	if _, ok := servers["vibeflow"]; ok {
		t.Errorf("vibeflow not removed")
	}
	if _, ok := servers["figma"]; !ok {
		t.Errorf("sibling figma removed")
	}

	// Second removal is a no-op.
	if action, _ := removeJSONMCPServer(path, "vibeflow"); action != "unchanged" {
		t.Errorf("repeat removal action = %q, want unchanged", action)
	}
	// Missing file is absent, not an error.
	if action, err := removeJSONMCPServer(filepath.Join(t.TempDir(), "nope.json"), "vibeflow"); err != nil || action != "absent" {
		t.Errorf("absent file: action=%q err=%v", action, err)
	}
}

// --- Codex TOML writer ---

const seedCodexTOML = `[mcp_servers.vibeflow]
url = "https://old.example/rest/v1/vibeflow/mcp"
bearer_token_env_var = "MCP_TOKEN"

[mcp_servers.vibeflow-uat]
url = "https://uat.example/rest/v1/vibeflow/mcp"
bearer_token_env_var = "MCP_TOKEN"

[mcp_servers.vflocal]
url = "http://localhost:8082/rest/v1/vibeflow/mcp"
bearer_token_env_var = "MCP_TOKEN"
`

func TestWriteCodexTOMLServer_UpsertPreservesOtherSections(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(seedCodexTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	newURL := "https://cloud.example/rest/v1/vibeflow/mcp"
	action, err := writeCodexTOMLServer(path, "vibeflow", newURL)
	if err != nil {
		t.Fatalf("writeCodexTOMLServer: %v", err)
	}
	if action != "updated" {
		t.Fatalf("action = %q, want updated", action)
	}

	got, _ := os.ReadFile(path)
	content := string(got)
	if !strings.Contains(content, `url = "`+newURL+`"`) {
		t.Errorf("new vibeflow url missing:\n%s", content)
	}
	if strings.Contains(content, "old.example") {
		t.Errorf("old vibeflow url not replaced:\n%s", content)
	}
	for _, want := range []string{"[mcp_servers.vibeflow-uat]", "uat.example", "[mcp_servers.vflocal]", "localhost:8082"} {
		if !strings.Contains(content, want) {
			t.Errorf("sibling content %q lost:\n%s", want, content)
		}
	}
}

func TestWriteCodexTOMLServer_CreatesFileWhenAbsent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	action, err := writeCodexTOMLServer(path, "vibeflow", "https://cloud.example/rest/v1/vibeflow/mcp")
	if err != nil {
		t.Fatalf("writeCodexTOMLServer: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	data, _ := os.ReadFile(path)
	if got := parseCodexBearerTokenEnvVar(string(data)); got != "MCP_TOKEN" {
		t.Errorf("bearer_token_env_var = %q, want MCP_TOKEN\n%s", got, data)
	}
}

func TestWriteCodexTOMLServer_Idempotent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	url := "https://cloud.example/rest/v1/vibeflow/mcp"
	if _, err := writeCodexTOMLServer(path, "vibeflow", url); err != nil {
		t.Fatal(err)
	}
	action, err := writeCodexTOMLServer(path, "vibeflow", url)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("second write action = %q, want unchanged", action)
	}
}

func TestRemoveCodexTOMLServer_RemovesOnlyTarget(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(seedCodexTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	action, err := removeCodexTOMLServer(path, "vibeflow")
	if err != nil {
		t.Fatal(err)
	}
	if action != "removed" {
		t.Fatalf("action = %q, want removed", action)
	}
	data, _ := os.ReadFile(path)
	content := string(data)
	if strings.Contains(content, "[mcp_servers.vibeflow]\n") {
		t.Errorf("vibeflow section not removed:\n%s", content)
	}
	// The similarly-named uat section and vflocal must survive.
	for _, want := range []string{"[mcp_servers.vibeflow-uat]", "[mcp_servers.vflocal]"} {
		if !strings.Contains(content, want) {
			t.Errorf("section %q lost:\n%s", want, content)
		}
	}

	if action, _ := removeCodexTOMLServer(path, "vibeflow"); action != "unchanged" {
		t.Errorf("repeat removal action = %q, want unchanged", action)
	}
	if action, err := removeCodexTOMLServer(filepath.Join(t.TempDir(), "nope.toml"), "vibeflow"); err != nil || action != "absent" {
		t.Errorf("absent file: action=%q err=%v", action, err)
	}
}

// --- entry shape builders ---

func TestClaudeDesktopEntry_TokenInEnvBlockReferencedByName(t *testing.T) {
	entry := claudeDesktopEntry("https://cloud.example/rest/v1/vibeflow/mcp", "secret-key")
	env, _ := entry["env"].(map[string]any)
	if env[mcpTokenEnvVar] != "secret-key" {
		t.Errorf("env.%s = %v, want secret-key", mcpTokenEnvVar, env[mcpTokenEnvVar])
	}
	args, _ := entry["args"].([]any)
	joined := ""
	for _, a := range args {
		joined += a.(string) + " "
	}
	if !strings.Contains(joined, "Authorization: Bearer ${MCP_TOKEN}") {
		t.Errorf("args missing ${MCP_TOKEN} header reference: %v", args)
	}
	if !strings.Contains(joined, "mcp-remote") {
		t.Errorf("args missing mcp-remote bridge: %v", args)
	}
}

func TestJSONHTTPEntry_TransportAndTimeout(t *testing.T) {
	cli := jsonHTTPEntry("http", false)("https://u", "")
	if cli["type"] != "http" {
		t.Errorf("claude-cli type = %v, want http", cli["type"])
	}
	if _, ok := cli["timeout"]; ok {
		t.Errorf("claude-cli should not carry a per-server timeout: %v", cli["timeout"])
	}

	gem := jsonHTTPEntry("streamable-http", true)("https://u", "")
	if gem["type"] != "streamable-http" {
		t.Errorf("gemini type = %v, want streamable-http", gem["type"])
	}
	if !equalJSON(gem["timeout"], mcpClientTimeoutMS) {
		t.Errorf("gemini timeout = %v, want %d", gem["timeout"], mcpClientTimeoutMS)
	}
	// No agent's HTTP entry should embed the literal token.
	for _, e := range []map[string]any{cli, gem} {
		h, _ := e["headers"].(map[string]any)
		if h["Authorization"] != "Bearer ${MCP_TOKEN}" {
			t.Errorf("Authorization = %v, want Bearer ${MCP_TOKEN}", h["Authorization"])
		}
	}
}

func TestBootstrapMCPURL(t *testing.T) {
	cases := map[string]string{
		"https://cloud.axiomstudio.ai":  "https://cloud.axiomstudio.ai/rest/v1/vibeflow/mcp",
		"https://cloud.axiomstudio.ai/": "https://cloud.axiomstudio.ai/rest/v1/vibeflow/mcp",
		"  https://x.test///  ":         "https://x.test/rest/v1/vibeflow/mcp",
	}
	for in, want := range cases {
		if got := bootstrapMCPURL(in); got != want {
			t.Errorf("bootstrapMCPURL(%q) = %q, want %q", in, got, want)
		}
	}
}

// --- initial config setup ---

func TestSetupInitialConfig_StoresValuesAndHonorsName(t *testing.T) {
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	action, err := setupInitialConfig(cfgPath, "https://cloud.example/", "the-key", "custommcp")
	if err != nil {
		t.Fatalf("setupInitialConfig: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ServerURL != "https://cloud.example" {
		t.Errorf("ServerURL = %q, want https://cloud.example (trailing slash trimmed)", cfg.ServerURL)
	}
	if cfg.APIToken != "the-key" {
		t.Errorf("APIToken = %q, want the-key", cfg.APIToken)
	}
	if cfg.MCPToolName != "custommcp" {
		t.Errorf("MCPToolName = %q, want custommcp", cfg.MCPToolName)
	}

	// Re-running updates rather than re-creating.
	if action, _ := setupInitialConfig(cfgPath, "https://cloud.example", "the-key", "custommcp"); action != "updated" {
		t.Errorf("second run action = %q, want updated", action)
	}
}

// --- agent selection ---

func TestResolveAgentsCSV(t *testing.T) {
	agents := bootstrapAgents()

	got, err := resolveAgentsCSV(agents, "codex, claude , cursor")
	if err != nil {
		t.Fatalf("resolveAgentsCSV: %v", err)
	}
	keys := agentKeys(got)
	want := []string{"codex", "claude-cli", "cursor"}
	if strings.Join(keys, ",") != strings.Join(want, ",") {
		t.Errorf("keys = %v, want %v (alias claude->claude-cli)", keys, want)
	}

	// Duplicates collapse.
	if got, _ := resolveAgentsCSV(agents, "codex,codex"); len(got) != 1 {
		t.Errorf("duplicate codex not deduped: %d", len(got))
	}
	// Unknown agent errors.
	if _, err := resolveAgentsCSV(agents, "codex,bogus"); err == nil {
		t.Errorf("expected error for unknown agent")
	}
}

func TestParseAgentSelection(t *testing.T) {
	agents := bootstrapAgents()

	if got, _ := parseAgentSelection("1, 3 4", agents); len(got) != 3 {
		t.Errorf("numbered selection len = %d, want 3", len(got))
	}
	if got, _ := parseAgentSelection("a", agents); len(got) != len(agents) {
		t.Errorf("'a' should select all")
	}
	if got, _ := parseAgentSelection("2 2", agents); len(got) != 1 {
		t.Errorf("duplicate index not deduped: %d", len(got))
	}
	for _, empty := range []string{"", "q", "  ", "quit\n"} {
		if got, _ := parseAgentSelection(empty, agents); got != nil {
			t.Errorf("parseAgentSelection(%q) = %v, want nil", empty, got)
		}
	}
	for _, bad := range []string{"0", "99", "x"} {
		if _, err := parseAgentSelection(bad, agents); err == nil {
			t.Errorf("parseAgentSelection(%q) expected error", bad)
		}
	}
}

func TestBootstrapAgents_OrderAndKeys(t *testing.T) {
	got := agentKeys(bootstrapAgents())
	want := []string{"codex", "gemini", "cursor", "claude-cli", "claude-desktop"}
	if strings.Join(got, ",") != strings.Join(want, ",") {
		t.Errorf("agent order = %v, want %v", got, want)
	}
}

// --- end-to-end command ---

func newBootstrapTestRoot() *cobra.Command {
	root := &cobra.Command{Use: "vibeflow-cli"}
	root.PersistentFlags().String("root", "", "")
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().String("mcp", "", "")
	root.AddCommand(bootstrapCmd())
	root.AddCommand(uninstallCmd())
	return root
}

func TestBootstrapAndUninstall_EndToEnd(t *testing.T) {
	origRoot := rootDir
	t.Cleanup(func() { rootDir = origRoot })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")
	os.Unsetenv("VIBEFLOW_ROOT")
	SetRootDir("")

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	// Bootstrap all agents non-interactively.
	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--api-key", "K-123", "--base-url", "https://cloud.example", "--config", cfgPath, "--all"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bootstrap execute: %v\n%s", err, out.String())
	}

	// vibeflow-cli config stores the api key (becomes MCP_TOKEN at launch).
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.APIToken != "K-123" {
		t.Errorf("config APIToken = %q, want K-123", cfg.APIToken)
	}
	if cfg.ServerURL != "https://cloud.example" {
		t.Errorf("config ServerURL = %q, want https://cloud.example", cfg.ServerURL)
	}

	// Each agent config file exists and references the endpoint.
	wantURL := "https://cloud.example/rest/v1/vibeflow/mcp"
	jsonAgents := map[string]func() (string, error){
		"claude-cli":     claudeCLIConfigPath,
		"gemini":         geminiConfigPath,
		"cursor":         cursorConfigPath,
		"claude-desktop": claudeDesktopConfigPath,
	}
	for name, resolve := range jsonAgents {
		p, _ := resolve()
		vibe := mcpServerEntry(t, readJSONFile(t, p), "vibeflow")
		if name == "claude-desktop" {
			env, _ := vibe["env"].(map[string]any)
			if env[mcpTokenEnvVar] != "K-123" {
				t.Errorf("%s: env.MCP_TOKEN = %v, want K-123", name, env[mcpTokenEnvVar])
			}
		} else if vibe["url"] != wantURL {
			t.Errorf("%s: url = %v, want %v", name, vibe["url"], wantURL)
		}
	}
	codexPath, _ := codexBootstrapConfigPath()
	codexData, _ := os.ReadFile(codexPath)
	if !strings.Contains(string(codexData), wantURL) {
		t.Errorf("codex config missing endpoint:\n%s", codexData)
	}
	if got := parseCodexBearerTokenEnvVar(string(codexData)); got != "MCP_TOKEN" {
		t.Errorf("codex bearer_token_env_var = %q, want MCP_TOKEN", got)
	}

	// Uninstall removes the entry from every agent.
	root2 := newBootstrapTestRoot()
	var out2 bytes.Buffer
	root2.SetOut(&out2)
	root2.SetErr(&out2)
	root2.SetArgs([]string{"uninstall", "--all"})
	if err := root2.Execute(); err != nil {
		t.Fatalf("uninstall execute: %v\n%s", err, out2.String())
	}
	for name, resolve := range jsonAgents {
		p, _ := resolve()
		servers, _ := readJSONFile(t, p)["mcpServers"].(map[string]any)
		if _, ok := servers["vibeflow"]; ok {
			t.Errorf("%s: vibeflow entry not removed by uninstall", name)
		}
	}
	codexAfter, _ := os.ReadFile(codexPath)
	if strings.Contains(string(codexAfter), "[mcp_servers.vibeflow]") {
		t.Errorf("codex vibeflow section not removed:\n%s", codexAfter)
	}
}

func TestBootstrapCmd_CancelWritesNothing(t *testing.T) {
	origRoot := rootDir
	t.Cleanup(func() { rootDir = origRoot })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")
	os.Unsetenv("VIBEFLOW_ROOT")
	SetRootDir("")

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetIn(strings.NewReader("q\n")) // cancel the interactive picker
	root.SetArgs([]string{"bootstrap", "--api-key", "K", "--config", cfgPath})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}
	if ConfigFileExists(cfgPath) {
		t.Errorf("cancel should not write the vibeflow-cli config")
	}
	if ConfigFileExists(filepath.Join(home, ".claude.json")) {
		t.Errorf("cancel should not write any agent config")
	}
}

func TestBootstrapCmd_RequiresAPIKey(t *testing.T) {
	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--all"})
	if err := root.Execute(); err == nil {
		t.Fatalf("expected error when --api-key missing")
	}
}
