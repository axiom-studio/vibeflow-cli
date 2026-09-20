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
	"fmt"
	"os"
	"os/exec"
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

// withTempRoot points RootDir() at a fresh temp dir for the test so that
// backup files (written under <RootDir>/.backup) never touch the real
// ~/.vibeflow-cli. Returns the temp root.
func withTempRoot(t *testing.T) string {
	t.Helper()
	orig := rootDir
	t.Cleanup(func() { rootDir = orig })
	dir := t.TempDir()
	SetRootDir(dir)
	return dir
}

// backupFiles lists the backup files under <root>/.backup.
func backupFiles(t *testing.T, root string) []string {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, ".backup", "*"))
	if err != nil {
		t.Fatalf("glob backups: %v", err)
	}
	return matches
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
	withTempRoot(t)
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
	action, backup, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatalf("writeJSONMCPServer: %v", err)
	}
	if action != "updated" {
		t.Fatalf("action = %q, want updated", action)
	}
	if backup == "" {
		t.Errorf("expected a backup path for an updated existing file")
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
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "nested", "mcp.json")
	entry := jsonHTTPEntry("streamable-http", true)("https://cloud.example/rest/v1/vibeflow/mcp", "")

	action, backup, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatalf("writeJSONMCPServer: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	if backup != "" {
		t.Errorf("a newly-created file should have no backup, got %q", backup)
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
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "mcp.json")
	entry := jsonHTTPEntry("streamable-http", true)("https://cloud.example/rest/v1/vibeflow/mcp", "")

	if _, _, err := writeJSONMCPServer(path, "vibeflow", entry); err != nil {
		t.Fatal(err)
	}
	action, backup, err := writeJSONMCPServer(path, "vibeflow", entry)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("second write action = %q, want unchanged", action)
	}
	if backup != "" {
		t.Errorf("unchanged re-run should not back up, got %q", backup)
	}
}

func TestRemoveJSONMCPServer_RemovesOnlyVibeflow(t *testing.T) {
	withTempRoot(t)
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

	action, backup, err := removeJSONMCPServer(path, "vibeflow")
	if err != nil {
		t.Fatal(err)
	}
	if action != "removed" {
		t.Fatalf("action = %q, want removed", action)
	}
	if backup == "" {
		t.Errorf("removal of an existing entry should back up the prior file")
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
	if action, _, _ := removeJSONMCPServer(path, "vibeflow"); action != "unchanged" {
		t.Errorf("repeat removal action = %q, want unchanged", action)
	}
	// Missing file is absent, not an error.
	if action, _, err := removeJSONMCPServer(filepath.Join(t.TempDir(), "nope.json"), "vibeflow"); err != nil || action != "absent" {
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
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(seedCodexTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	newURL := "https://cloud.example/rest/v1/vibeflow/mcp"
	action, backup, err := writeCodexTOMLServer(path, "vibeflow", newURL)
	if err != nil {
		t.Fatalf("writeCodexTOMLServer: %v", err)
	}
	if action != "updated" {
		t.Fatalf("action = %q, want updated", action)
	}
	if backup == "" {
		t.Errorf("expected a backup path for an updated existing file")
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
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "nested", "config.toml")
	action, backup, err := writeCodexTOMLServer(path, "vibeflow", "https://cloud.example/rest/v1/vibeflow/mcp")
	if err != nil {
		t.Fatalf("writeCodexTOMLServer: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	if backup != "" {
		t.Errorf("a newly-created file should have no backup, got %q", backup)
	}
	data, _ := os.ReadFile(path)
	if got := parseCodexBearerTokenEnvVar(string(data)); got != "MCP_TOKEN" {
		t.Errorf("bearer_token_env_var = %q, want MCP_TOKEN\n%s", got, data)
	}
}

func TestWriteCodexTOMLServer_Idempotent(t *testing.T) {
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	url := "https://cloud.example/rest/v1/vibeflow/mcp"
	if _, _, err := writeCodexTOMLServer(path, "vibeflow", url); err != nil {
		t.Fatal(err)
	}
	action, backup, err := writeCodexTOMLServer(path, "vibeflow", url)
	if err != nil {
		t.Fatal(err)
	}
	if action != "unchanged" {
		t.Fatalf("second write action = %q, want unchanged", action)
	}
	if backup != "" {
		t.Errorf("unchanged re-run should not back up, got %q", backup)
	}
}

func TestRemoveCodexTOMLServer_RemovesOnlyTarget(t *testing.T) {
	withTempRoot(t)
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte(seedCodexTOML), 0o600); err != nil {
		t.Fatal(err)
	}

	action, backup, err := removeCodexTOMLServer(path, "vibeflow")
	if err != nil {
		t.Fatal(err)
	}
	if action != "removed" {
		t.Fatalf("action = %q, want removed", action)
	}
	if backup == "" {
		t.Errorf("removal of an existing section should back up the prior file")
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

	if action, _, _ := removeCodexTOMLServer(path, "vibeflow"); action != "unchanged" {
		t.Errorf("repeat removal action = %q, want unchanged", action)
	}
	if action, _, err := removeCodexTOMLServer(filepath.Join(t.TempDir(), "nope.toml"), "vibeflow"); err != nil || action != "absent" {
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

// TestClaudeDesktopEntry_RawTokenNeverInArgv locks the division of labor that
// makes the ${MCP_TOKEN} indirection work for Claude Desktop: the raw token
// lives ONLY in the entry's env block, and argv carries the placeholder.
//
// mcp-remote resolves the placeholder itself — it env-expands every --header
// value before use (dist/chunk-65X3S4HB.js in 0.1.38, present since 0.1.0):
//
//	headers[key] = value.replace(/\$\{([^}]+)}/g, (m, name) => process.env[name] ?? "")
//
// Verified on the wire against a local server that echoes Authorization, with
// npx spawned exactly as Claude Desktop spawns it (direct spawn, no shell, env
// block merged): every request carried the real key, never the placeholder.
//
// Inlining the token into argv instead would be a security regression — argv is
// readable by any local process via `ps`, whereas a child's environment is not.
func TestClaudeDesktopEntry_RawTokenNeverInArgv(t *testing.T) {
	const token = "sk-super-secret-token"
	entry := claudeDesktopEntry("https://cloud.example/rest/v1/vibeflow/mcp", token)

	args, _ := entry["args"].([]any)
	for _, a := range args {
		if s, _ := a.(string); strings.Contains(s, token) {
			t.Errorf("raw token leaked into argv (readable via ps): %q", s)
		}
	}

	// The env block is the only place the real token belongs.
	env, _ := entry["env"].(map[string]any)
	if env[mcpTokenEnvVar] != token {
		t.Errorf("env.%s = %v, want the raw token", mcpTokenEnvVar, env[mcpTokenEnvVar])
	}
}

// TestClaudeDesktopEntry_CommandIsAbsoluteOrBareNpx guards issue #4336: a GUI
// app gets the launchd PATH, not the shell's, so a bare "npx" often cannot be
// spawned. The entry must carry either an absolute path or exactly "npx" (the
// documented fallback) — never something half-resolved.
func TestClaudeDesktopEntry_CommandIsAbsoluteOrBareNpx(t *testing.T) {
	cmd, _ := claudeDesktopEntry("https://u", "k")["command"].(string)
	if cmd != "npx" && !filepath.IsAbs(cmd) {
		t.Errorf("command = %q, want an absolute path or exactly \"npx\"", cmd)
	}
	if cmd == "npx" {
		if p, err := exec.LookPath("npx"); err == nil {
			t.Errorf("npx resolved to %q but the entry kept the bare name", p)
		}
	}
}

// TestSetupInitialConfig_IdenticalRerunIsUnchanged guards issue #4342: an
// identical re-run used to report "updated" and write another 0600 backup of the
// API token, unlike the agent-config writers which correctly report "unchanged".
func TestSetupInitialConfig_IdenticalRerunIsUnchanged(t *testing.T) {
	root := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", root)
	cfgPath := filepath.Join(root, "config.yaml")

	action, _, err := setupInitialConfig(cfgPath, "https://cloud.example", "sk-same", "vibeflow")
	if err != nil || action != "created" {
		t.Fatalf("first run: action=%q err=%v, want created", action, err)
	}

	action, backup, err := setupInitialConfig(cfgPath, "https://cloud.example", "sk-same", "vibeflow")
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if action != "unchanged" {
		t.Errorf("identical re-run action = %q, want unchanged", action)
	}
	if backup != "" {
		t.Errorf("identical re-run wrote a backup at %q; it should write none", backup)
	}
	// No backup file should exist at all — each one is another at-rest copy of
	// the token.
	if entries, err := os.ReadDir(filepath.Join(root, ".backup")); err == nil && len(entries) > 0 {
		t.Errorf(".backup gained %d entries on a no-op re-run", len(entries))
	}

	// A real change must still be detected and still back up.
	action, backup, err = setupInitialConfig(cfgPath, "https://cloud.example", "sk-DIFFERENT", "vibeflow")
	if err != nil || action != "updated" {
		t.Fatalf("changed run: action=%q err=%v, want updated", action, err)
	}
	if backup == "" {
		t.Error("changed run wrote no backup; the prior config must be preserved")
	}
}

// TestAgentsFlagUsage_ListsEverySupportedAgent guards issue #4334: the help text
// omitted kiro, so it was undiscoverable from the CLI. Deriving it from the
// registry means adding a 7th agent cannot silently skip the docs.
func TestAgentsFlagUsage_ListsEverySupportedAgent(t *testing.T) {
	usage := agentsFlagUsage("configure")
	for _, a := range bootstrapAgents() {
		if !strings.Contains(usage, a.key) {
			t.Errorf("--agents usage omits %q: %s", a.key, usage)
		}
	}
}

// TestNpxReadmeAgentListMatchesRegistry ties npx/README.md's agent claims to the
// live registry, which is what #4341 asked for and #4369 filed as missing.
//
// #4341 items 4 and 5 were themselves README-drift bugs: the doc claimed five
// agents when six are registered, and claimed every agent gets the 300000 ms
// timeout when Claude CLI deliberately gets none. Correcting the prose fixed the
// instance; only a test tied to bootstrapAgents() closes the class, so the
// seventh agent cannot silently reopen it.
func TestNpxReadmeAgentListMatchesRegistry(t *testing.T) {
	// bootstrap_test.go lives in internal/vibeflowcli/, so the repo root is two up.
	readmePath := filepath.Join("..", "..", "npx", "README.md")
	data, err := os.ReadFile(readmePath)
	if err != nil {
		t.Fatalf("read %s: %v", readmePath, err)
	}
	readme := string(data)

	agents := bootstrapAgents()
	for _, a := range agents {
		if !strings.Contains(readme, a.label) {
			t.Errorf("npx/README.md never mentions %q — it documents the agents bootstrap configures, "+
				"so a newly registered agent must be added there too", a.label)
		}
	}

	// The count claim has to move with the registry. bootstrap writes one target
	// per agent plus the vibeflow-cli config.yaml.
	wantTargets := fmt.Sprintf("%d targets", len(agents)+1)
	if !strings.Contains(readme, wantTargets) {
		t.Errorf("npx/README.md does not state %q; with %d registered agents plus config.yaml "+
			"that is the correct total", wantTargets, len(agents))
	}

	// Guard the specific false claim #4341 item 5 corrected: Claude CLI takes no
	// per-server timeout, it honors MCP_TIMEOUT.
	if !strings.Contains(readme, "MCP_TIMEOUT") {
		t.Error("npx/README.md must explain that Claude CLI uses MCP_TIMEOUT rather than a " +
			"per-server timeout, or the corrected timeout claim can silently regress")
	}
}

// TestBootstrapHelp_NoStaleAgentListAnywhere asserts against the FULL rendered
// help, not just the generated flag string.
//
// The earlier version of this test inspected only agentsFlagUsage() and so
// passed while `--help` was visibly printing two contradictory agent lists: the
// derived flag line said six, and the hand-written Long description two lines
// above still said five with no Kiro. A guard that cannot see the defect it
// guards is worse than no guard, because it reads as coverage. QA caught this
// and reopened #4334 for it.
func TestBootstrapHelp_NoStaleAgentListAnywhere(t *testing.T) {
	for _, tc := range []struct {
		name string
		cmd  *cobra.Command
	}{
		{"bootstrap", bootstrapCmd()},
		{"uninstall", uninstallCmd()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var buf bytes.Buffer
			tc.cmd.SetOut(&buf)
			tc.cmd.SetErr(&buf)
			if err := tc.cmd.Help(); err != nil {
				t.Fatalf("render help: %v", err)
			}
			help := buf.String()

			// Every key must be discoverable from --help alone.
			for _, a := range bootstrapAgents() {
				if !strings.Contains(help, a.key) {
					t.Errorf("%s --help never mentions agent key %q", tc.name, a.key)
				}
			}

			// If the help enumerates agents by LABEL (prose), the list must be
			// complete. Partial label lists are exactly the #4334 defect.
			labels := agentLabels(bootstrapAgents())
			var present, missing []string
			for _, l := range labels {
				if strings.Contains(help, l) {
					present = append(present, l)
				} else {
					missing = append(missing, l)
				}
			}
			if len(present) > 0 && len(missing) > 0 {
				t.Errorf("%s --help lists some agents by label but not others; missing %v.\n"+
					"Derive the prose from agentLabels(bootstrapAgents()) instead of hardcoding.\n---\n%s",
					tc.name, missing, help)
			}
		})
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

	gem := jsonHTTPEntry("http", true)("https://u", "")
	if gem["type"] != "http" {
		t.Errorf("gemini type = %v, want http", gem["type"])
	}
	if !equalJSON(gem["timeout"], mcpClientTimeoutMS) {
		t.Errorf("gemini timeout = %v, want %d", gem["timeout"], mcpClientTimeoutMS)
	}

	cursor := jsonHTTPEntry("streamable-http", true)("https://u", "")
	if cursor["type"] != "streamable-http" {
		t.Errorf("cursor type = %v, want streamable-http", cursor["type"])
	}
	if !equalJSON(cursor["timeout"], mcpClientTimeoutMS) {
		t.Errorf("cursor timeout = %v, want %d", cursor["timeout"], mcpClientTimeoutMS)
	}

	// No agent's HTTP entry should embed the literal token.
	for _, e := range []map[string]any{cli, gem, cursor} {
		h, _ := e["headers"].(map[string]any)
		if h["Authorization"] != "Bearer ${MCP_TOKEN}" {
			t.Errorf("Authorization = %v, want Bearer ${MCP_TOKEN}", h["Authorization"])
		}
	}
}

func TestBootstrapAgents_GeminiUsesHTTPTransport(t *testing.T) {
	agents := bootstrapAgents()
	var gem bootstrapAgent
	for _, a := range agents {
		if a.key == "gemini" {
			gem = a
			break
		}
	}
	if gem.entry == nil {
		t.Fatal("gemini bootstrap agent missing entry builder")
	}
	entry := gem.entry("https://cloud.example/rest/v1/vibeflow/mcp", "")
	if entry["type"] != "http" {
		t.Errorf("gemini type = %v, want http", entry["type"])
	}
	if !equalJSON(entry["timeout"], mcpClientTimeoutMS) {
		t.Errorf("gemini timeout = %v, want %d", entry["timeout"], mcpClientTimeoutMS)
	}
}

func TestBootstrapAgents_CursorUsesStreamableHTTPTransport(t *testing.T) {
	agents := bootstrapAgents()
	var cursor bootstrapAgent
	for _, a := range agents {
		if a.key == "cursor" {
			cursor = a
			break
		}
	}
	if cursor.entry == nil {
		t.Fatal("cursor bootstrap agent missing entry builder")
	}
	entry := cursor.entry("https://cloud.example/rest/v1/vibeflow/mcp", "")
	if entry["type"] != "streamable-http" {
		t.Errorf("cursor type = %v, want streamable-http", entry["type"])
	}
	if !equalJSON(entry["timeout"], mcpClientTimeoutMS) {
		t.Errorf("cursor timeout = %v, want %d", entry["timeout"], mcpClientTimeoutMS)
	}
}

func TestBootstrapAgents_CopilotUsesHTTPTransportWithToolsWildcard(t *testing.T) {
	agents := bootstrapAgents()
	var copilot bootstrapAgent
	for _, a := range agents {
		if a.key == "copilot" {
			copilot = a
			break
		}
	}
	if copilot.entry == nil {
		t.Fatal("copilot bootstrap agent missing entry builder")
	}
	entry := copilot.entry("https://cloud.example/rest/v1/vibeflow/mcp", "")
	if entry["type"] != "http" {
		t.Errorf("copilot type = %v, want http", entry["type"])
	}
	if !equalJSON(entry["timeout"], mcpClientTimeoutMS) {
		t.Errorf("copilot timeout = %v, want %d", entry["timeout"], mcpClientTimeoutMS)
	}
	// copilot mcp add always writes a tools filter; "*" enables every tool.
	if !equalJSON(entry["tools"], []any{"*"}) {
		t.Errorf("copilot tools = %v, want [*]", entry["tools"])
	}
	// The bearer must be an env reference, never a literal token.
	headers, _ := entry["headers"].(map[string]any)
	if headers["Authorization"] != mcpBearerRef {
		t.Errorf("copilot Authorization = %v, want %s", headers["Authorization"], mcpBearerRef)
	}
	p, err := copilotConfigPath()
	if err != nil {
		t.Fatalf("copilotConfigPath: %v", err)
	}
	if filepath.Base(p) != "mcp-config.json" || filepath.Base(filepath.Dir(p)) != ".copilot" {
		t.Errorf("copilotConfigPath = %q, want ~/.copilot/mcp-config.json", p)
	}
}

func TestBootstrapCmd_WritesGeminiHTTPTransport(t *testing.T) {
	withTempRoot(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--api-key", "K", "--config", cfgPath, "--agents", "gemini"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}

	gemPath, _ := geminiConfigPath()
	entry := mcpServerEntry(t, readJSONFile(t, gemPath), "vibeflow")
	if entry["type"] != "http" {
		t.Errorf("gemini type = %v, want http", entry["type"])
	}
	if !equalJSON(entry["timeout"], mcpClientTimeoutMS) {
		t.Errorf("gemini timeout = %v, want %d", entry["timeout"], mcpClientTimeoutMS)
	}
}

func TestJSONHTTPEntry_StreamableTransport(t *testing.T) {
	gem := jsonHTTPEntry("streamable-http", true)("https://u", "")
	if gem["type"] != "streamable-http" {
		t.Errorf("type = %v, want streamable-http", gem["type"])
	}
	if !equalJSON(gem["timeout"], mcpClientTimeoutMS) {
		t.Errorf("timeout = %v, want %d", gem["timeout"], mcpClientTimeoutMS)
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
	withTempRoot(t)
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	action, backup, err := setupInitialConfig(cfgPath, "https://cloud.example/", "the-key", "custommcp")
	if err != nil {
		t.Fatalf("setupInitialConfig: %v", err)
	}
	if action != "created" {
		t.Fatalf("action = %q, want created", action)
	}
	if backup != "" {
		t.Errorf("creating a new config should not back up, got %q", backup)
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

	// Re-running with a CHANGED value updates rather than re-creating, and backs
	// up the prior file. The value must actually differ: a byte-identical re-run
	// is "unchanged" and deliberately writes no backup, since each backup is
	// another at-rest copy of the API token (issue #4342, covered by
	// TestSetupInitialConfig_IdenticalRerunIsUnchanged).
	action2, backup2, _ := setupInitialConfig(cfgPath, "https://cloud.example", "a-rotated-key", "custommcp")
	if action2 != "updated" {
		t.Errorf("second run action = %q, want updated", action2)
	}
	if backup2 == "" {
		t.Errorf("re-running over an existing config should back it up")
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
	want := []string{"codex", "gemini", "cursor", "claude-cli", "claude-desktop", "kiro", "copilot", "qwen"}
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
		"kiro":           kiroConfigPath,
		"copilot":        copilotConfigPath,
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

func TestBackup_OnUpdate_CarriesOriginalContent(t *testing.T) {
	root := withTempRoot(t)
	path := filepath.Join(t.TempDir(), "mcp.json")
	original := `{"mcpServers":{"vibeflow":{"old":"entry"}}}` + "\n"
	if err := os.WriteFile(path, []byte(original), 0o600); err != nil {
		t.Fatal(err)
	}

	entry := jsonHTTPEntry("http", false)("https://cloud.example/rest/v1/vibeflow/mcp", "")
	if _, backup, err := writeJSONMCPServer(path, "vibeflow", entry); err != nil || backup == "" {
		t.Fatalf("write: backup=%q err=%v", backup, err)
	}

	files := backupFiles(t, root)
	if len(files) != 1 {
		t.Fatalf("expected exactly 1 backup file, got %d: %v", len(files), files)
	}
	got, _ := os.ReadFile(files[0])
	if string(got) != original {
		t.Errorf("backup content = %q, want the original pre-write content %q", got, original)
	}
	if !strings.HasSuffix(files[0], ".bak") {
		t.Errorf("backup name %q should end in .bak", files[0])
	}
}

func TestBootstrapCmd_MCPNameHonored(t *testing.T) {
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
	root.SetArgs([]string{"bootstrap", "--api-key", "K", "--config", cfgPath, "--mcp", "myflow", "--agents", "codex,gemini"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}

	codexPath, _ := codexBootstrapConfigPath()
	codexData, _ := os.ReadFile(codexPath)
	if !strings.Contains(string(codexData), "[mcp_servers.myflow]") {
		t.Errorf("codex config missing [mcp_servers.myflow]:\n%s", codexData)
	}
	gemPath, _ := geminiConfigPath()
	servers, _ := readJSONFile(t, gemPath)["mcpServers"].(map[string]any)
	if _, ok := servers["myflow"]; !ok {
		t.Errorf("gemini mcpServers missing 'myflow' key: %v", servers)
	}
	if _, ok := servers["vibeflow"]; ok {
		t.Errorf("gemini should not have a default 'vibeflow' entry when --mcp=myflow")
	}
	cfg, _ := LoadConfig(cfgPath)
	if cfg.MCPToolName != "myflow" {
		t.Errorf("config MCPToolName = %q, want myflow", cfg.MCPToolName)
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

// dirMode returns path's permission bits, failing the test if it cannot stat.
func dirMode(t *testing.T, path string) os.FileMode {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("stat %s: %v", path, err)
	}
	return info.Mode().Perm()
}

// Regression test for issue #4559: writeConfigFileWithBackup used to run an
// unconditional os.Chmod(dir, 0700). For claude-cli the config is
// ~/.claude.json, so dir is $HOME and every bootstrap run silently re-moded
// the user's home directory. The two halves of the invariant are asserted
// together so a future "hardening" cannot restore the regression unnoticed:
// a directory we did not create keeps its mode, one we do create is 0700.
func TestWriteConfigFileWithBackup_DirModes(t *testing.T) {
	withTempRoot(t)

	t.Run("pre-existing dir keeps its mode", func(t *testing.T) {
		dir := t.TempDir()
		if err := os.Chmod(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if _, err := writeConfigFileWithBackup(filepath.Join(dir, "cfg.json"), []byte("{}\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := dirMode(t, dir); got != 0o755 {
			t.Errorf("pre-existing dir mode = %04o, want 0755 (unchanged)", got)
		}
	})

	t.Run("created dir is hardened to 0700", func(t *testing.T) {
		dir := filepath.Join(t.TempDir(), "made-by-vibeflow")
		if _, err := writeConfigFileWithBackup(filepath.Join(dir, "cfg.json"), []byte("{}\n")); err != nil {
			t.Fatalf("write: %v", err)
		}
		if got := dirMode(t, dir); got != 0o700 {
			t.Errorf("created dir mode = %04o, want 0700", got)
		}
	})
}

// End-to-end form of the issue #4559 repro: bootstrap claude-cli into an
// isolated HOME at 0755 and assert the home directory mode survives.
func TestBootstrapCmd_ClaudeCLIDoesNotRemodeHome(t *testing.T) {
	withTempRoot(t)
	home := t.TempDir()
	if err := os.Chmod(home, 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")

	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--api-key", "K", "--config", filepath.Join(t.TempDir(), "config.yaml"), "--agents", "claude-cli"})
	if err := root.Execute(); err != nil {
		t.Fatalf("execute: %v\n%s", err, out.String())
	}

	if _, err := os.Stat(filepath.Join(home, ".claude.json")); err != nil {
		t.Fatalf("bootstrap did not write ~/.claude.json: %v", err)
	}
	if got := dirMode(t, home); got != 0o755 {
		t.Errorf("$HOME mode = %04o after bootstrap, want 0755 (unchanged)", got)
	}
}

// Regression test for issue #4546 (and its duplicate #4604): the atomic write
// used to build a fully predictable temp path (`path + ".tmp"`) and open it
// with os.WriteFile, which sets neither O_EXCL nor O_NOFOLLOW. An attacker who
// could write to the config directory could pre-plant that name as a symlink;
// the write then followed it, leaking config contents (bearer tokens, for the
// gemini/cursor/codex/claude-desktop targets) to a path of their choosing, and
// the following rename installed the symlink as the permanent config file.
func TestWriteConfigFileWithBackup_IgnoresPrePlantedTmpSymlink(t *testing.T) {
	withTempRoot(t)
	dir := t.TempDir()
	path := filepath.Join(dir, "settings.json")

	canary := filepath.Join(t.TempDir(), "exfil")
	const canaryContent = "attacker-owned, must not be overwritten\n"
	if err := os.WriteFile(canary, []byte(canaryContent), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(canary, path+".tmp"); err != nil {
		t.Fatalf("plant symlink: %v", err)
	}

	secret := `{"headers":{"Authorization":"Bearer super-secret-token"}}` + "\n"
	if _, err := writeConfigFileWithBackup(path, []byte(secret)); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := os.ReadFile(canary)
	if err != nil {
		t.Fatalf("read canary: %v", err)
	}
	if string(got) != canaryContent {
		t.Errorf("symlink target was written through: canary = %q, want %q", got, canaryContent)
	}
	if strings.Contains(string(got), "super-secret-token") {
		t.Errorf("config contents leaked through the planted symlink")
	}

	written, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read destination: %v", err)
	}
	if string(written) != secret {
		t.Errorf("destination = %q, want %q", written, secret)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if info.Mode()&os.ModeSymlink != 0 {
		t.Errorf("destination is a symlink; the planted link was renamed into place")
	}
	if got := info.Mode().Perm(); got != 0o600 {
		t.Errorf("destination mode = %04o, want 0600 (config may carry bearer tokens)", got)
	}
}

// Issue #4546: the backup directory holds snapshots of config files that carry
// bearer tokens. The files are 0600, but the directory listing should not be
// world-readable either.
func TestBackupDir_IsNotWorldReadable(t *testing.T) {
	root := withTempRoot(t)
	path := filepath.Join(t.TempDir(), "cfg.json")
	if err := os.WriteFile(path, []byte("{\"old\":true}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := writeConfigFileWithBackup(path, []byte("{\"new\":true}\n")); err != nil {
		t.Fatalf("write: %v", err)
	}
	if files := backupFiles(t, root); len(files) != 1 {
		t.Fatalf("expected 1 backup, got %d: %v", len(files), files)
	}
	if got := dirMode(t, filepath.Join(root, ".backup")); got != 0o700 {
		t.Errorf("backup dir mode = %04o, want 0700", got)
	}
}

func TestBootstrapAgents_QwenUsesHTTPURLFormat(t *testing.T) {
	var qwen bootstrapAgent
	for _, a := range bootstrapAgents() {
		if a.key == "qwen" {
			qwen = a
		}
	}
	if qwen.entry == nil {
		t.Fatal("qwen bootstrap agent missing entry builder")
	}
	entry := qwen.entry("https://cloud.example/rest/v1/vibeflow/mcp", "raw-key")
	// qwen declares streamable HTTP with httpUrl; a plain url would mean SSE.
	if entry["httpUrl"] != "https://cloud.example/rest/v1/vibeflow/mcp" {
		t.Errorf("httpUrl = %v", entry["httpUrl"])
	}
	if _, ok := entry["url"]; ok {
		t.Error("qwen entry must not set url (qwen reads url as SSE)")
	}
	if !equalJSON(entry["timeout"], mcpClientTimeoutMS) {
		t.Errorf("timeout = %v, want %d", entry["timeout"], mcpClientTimeoutMS)
	}
	// The bearer is an env reference; the raw key is never written.
	headers, _ := entry["headers"].(map[string]any)
	if headers["Authorization"] != mcpBearerRef {
		t.Errorf("Authorization = %v, want %s", headers["Authorization"], mcpBearerRef)
	}
	data, _ := json.Marshal(entry)
	if strings.Contains(string(data), "raw-key") {
		t.Error("qwen entry contains the raw API key")
	}
	if normalizeAgentKey("qwen-code") != "qwen" {
		t.Error("alias qwen-code does not resolve to qwen")
	}
}

func TestBootstrapAndUninstall_QwenPreservesOtherSettings(t *testing.T) {
	withTempRoot(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")
	// An existing qwen settings file with unrelated settings and another server.
	settings := filepath.Join(home, ".qwen", "settings.json")
	if err := os.MkdirAll(filepath.Dir(settings), 0o700); err != nil {
		t.Fatal(err)
	}
	existing := `{"theme":"dark","mcpServers":{"other":{"command":"other-mcp"}}}`
	if err := os.WriteFile(settings, []byte(existing), 0o600); err != nil {
		t.Fatal(err)
	}
	cfgPath := filepath.Join(t.TempDir(), "config.yaml")

	run := func(args ...string) {
		t.Helper()
		root := newBootstrapTestRoot()
		var out bytes.Buffer
		root.SetOut(&out)
		root.SetErr(&out)
		root.SetArgs(args)
		if err := root.Execute(); err != nil {
			t.Fatalf("%v: %v\n%s", args, err, out.String())
		}
	}

	run("bootstrap", "--api-key", "K", "--config", cfgPath, "--agents", "qwen")
	got := readJSONFile(t, settings)
	entry := mcpServerEntry(t, got, "vibeflow")
	if entry["httpUrl"] != defaultBootstrapBaseURL+mcpEndpointPath {
		t.Errorf("httpUrl = %v", entry["httpUrl"])
	}
	if got["theme"] != "dark" {
		t.Error("bootstrap dropped an unrelated qwen setting")
	}
	mcpServerEntry(t, got, "other") // unrelated server kept
	if raw, _ := os.ReadFile(settings); strings.Contains(string(raw), `"K"`) {
		t.Error("API key written into qwen settings")
	}

	// Re-running is idempotent.
	before, _ := os.ReadFile(settings)
	run("bootstrap", "--api-key", "K", "--config", cfgPath, "--agents", "qwen")
	if after, _ := os.ReadFile(settings); string(after) != string(before) {
		t.Error("second bootstrap changed qwen settings")
	}

	run("uninstall", "--config", cfgPath, "--agents", "qwen")
	got = readJSONFile(t, settings)
	servers, _ := got["mcpServers"].(map[string]any)
	if _, ok := servers["vibeflow"]; ok {
		t.Error("uninstall left the vibeflow entry")
	}
	if _, ok := servers["other"]; !ok || got["theme"] != "dark" {
		t.Error("uninstall removed unrelated qwen settings")
	}
}

func TestStripJSONComments(t *testing.T) {
	tests := []struct {
		name, in, want string
	}{
		{"line comment", "{\n  // my model\n  \"a\": 1\n}", "{\n  \n  \"a\": 1\n}"}, // indentation before the comment is kept
		{"trailing line comment", `{"a": 1} // done`, `{"a": 1} `},
		{"block comment", "{/* note */\"a\": 1}", `{"a": 1}`},
		{"multi-line block", "{\n/* one\n   two */\n\"a\": 1}", "{\n\n\"a\": 1}"},
		// Comment markers inside strings must survive: URLs are the common case.
		{"url in string", `{"httpUrl": "https://cloud.example/rest/v1/mcp"}`, `{"httpUrl": "https://cloud.example/rest/v1/mcp"}`},
		{"block marker in string", `{"a": "/* not a comment */"}`, `{"a": "/* not a comment */"}`},
		{"escaped quote before marker", `{"a": "he said \"//\" ok"}`, `{"a": "he said \"//\" ok"}`},
		{"no comments", `{"a": 1}`, `{"a": 1}`},
		{"comment at EOF without newline", `{"a": 1}` + "\n// end", `{"a": 1}` + "\n"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := string(stripJSONComments([]byte(tt.in))); got != tt.want {
				t.Errorf("stripJSONComments(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

// TestBootstrapCmd_CommentedSettingsAreConfigured covers the JSONC settings
// files Gemini CLI and Qwen Code accept: bootstrap must configure them
// instead of failing with a raw parse error, keep the user's own settings,
// and say that comments are lost.
func TestBootstrapCmd_CommentedSettingsAreConfigured(t *testing.T) {
	origRoot := rootDir
	t.Cleanup(func() { rootDir = origRoot })
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("VIBEFLOW_ROOT", "")
	os.Unsetenv("VIBEFLOW_ROOT")
	SetRootDir("")

	commented := "{\n  // my model choice\n  \"model\": {\"name\": \"qwen3-coder-plus\"},\n  /* keep this */\n  \"ui\": {\"theme\": \"dark\"}\n}\n"
	for _, dir := range []string{".qwen", ".gemini"} {
		if err := os.MkdirAll(filepath.Join(home, dir), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(home, dir, "settings.json"), []byte(commented), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	var warnings bytes.Buffer
	SetWarnWriter(&warnings)
	t.Cleanup(func() { SetWarnWriter(nil) })

	cfgPath := filepath.Join(t.TempDir(), "config.yaml")
	root := newBootstrapTestRoot()
	var out bytes.Buffer
	root.SetOut(&out)
	root.SetErr(&out)
	root.SetArgs([]string{"bootstrap", "--api-key", "K-123", "--base-url", "https://cloud.example", "--config", cfgPath, "--agents", "qwen,gemini"})
	if err := root.Execute(); err != nil {
		t.Fatalf("bootstrap execute: %v\n%s", err, out.String())
	}

	for _, dir := range []string{".qwen", ".gemini"} {
		path := filepath.Join(home, dir, "settings.json")
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got map[string]any
		if err := json.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s is not valid JSON after bootstrap: %v\n%s", path, err, data)
		}
		if _, ok := got["mcpServers"]; !ok {
			t.Errorf("%s has no mcpServers after bootstrap:\n%s", path, data)
		}
		// The user's own settings survive the rewrite.
		if model, _ := got["model"].(map[string]any); model["name"] != "qwen3-coder-plus" {
			t.Errorf("%s lost the user's model setting:\n%s", path, data)
		}
		if ui, _ := got["ui"].(map[string]any); ui["theme"] != "dark" {
			t.Errorf("%s lost the user's ui setting:\n%s", path, data)
		}
		// The rewrite drops the comments, so the user is told and the
		// original file is kept.
		if !strings.Contains(warnings.String(), path) {
			t.Errorf("no warning naming %s:\n%s", path, warnings.String())
		}
	}
	if !strings.Contains(warnings.String(), "not preserved") {
		t.Errorf("warning does not say comments are lost: %q", warnings.String())
	}
	backups, _ := filepath.Glob(filepath.Join(RootDir(), ".backup", "*settings*"))
	if len(backups) == 0 {
		t.Error("no backup of the commented settings files")
	}
}

func TestReadJSONObject_InvalidJSONStillFails(t *testing.T) {
	// Only comments are tolerated: real syntax errors keep their message.
	path := filepath.Join(t.TempDir(), "settings.json")
	if err := os.WriteFile(path, []byte(`{"a": 1,,}`), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readJSONObject(path); err == nil || !strings.Contains(err.Error(), "parse "+path) {
		t.Errorf("err = %v, want a parse error naming the file", err)
	}
}
