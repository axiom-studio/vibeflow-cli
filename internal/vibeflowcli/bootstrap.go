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
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
)

// defaultBootstrapBaseURL is the VibeFlow base URL used when --base-url is
// omitted.
const defaultBootstrapBaseURL = "https://cloud.axiomstudio.ai"

// mcpEndpointPath is the REST path of the VibeFlow MCP server, appended to the
// base URL to form the endpoint each agent connects to.
const mcpEndpointPath = "/rest/v1/vibeflow/mcp"

// mcpTokenEnvVar is the environment variable every agent config references for
// the VibeFlow MCP bearer token. vibeflow-cli already injects it at launch for
// all providers (see WithMCPTokenEnv), and bootstrap stores the api key into
// the vibeflow-cli config's APIToken so it becomes this value. The token is
// therefore never written into the per-agent config files (except Claude
// Desktop, whose GUI does not inherit the launching shell's environment).
const mcpTokenEnvVar = "MCP_TOKEN"

// mcpBearerRef is the substitution string written into agent config headers.
const mcpBearerRef = "Bearer ${" + mcpTokenEnvVar + "}"

// mcpClientTimeoutMS is the per-server client timeout written into agent
// configs that support a timeout field. It must exceed wait_for_work's 115s
// poll window (115000ms) so long polls are not cut short; 300000ms (5 min)
// adds headroom for clock skew.
const mcpClientTimeoutMS = 300000

// bootstrapAgent describes a coding agent whose MCP configuration bootstrap can
// install or remove. JSON agents supply an `entry` builder for the
// mcpServers[name] payload; Codex (codex=true) uses a TOML section instead.
type bootstrapAgent struct {
	key   string                                  // stable CLI identifier, e.g. "claude-cli"
	label string                                  // human-facing name shown in the picker
	path  func() (string, error)                  // absolute config path ("" => unsupported on this OS)
	codex bool                                    // codex uses TOML; entry is nil
	entry func(url, apiKey string) map[string]any // JSON payload for mcpServers[name]
}

// bootstrapAgents returns the supported agents in the order shown to the user.
func bootstrapAgents() []bootstrapAgent {
	return []bootstrapAgent{
		{key: "codex", label: "Codex CLI", path: codexBootstrapConfigPath, codex: true},
		{key: "gemini", label: "Gemini CLI", path: geminiConfigPath, entry: jsonHTTPEntry("http", true)},
		{key: "cursor", label: "Cursor", path: cursorConfigPath, entry: jsonHTTPEntry("streamable-http", true)},
		{key: "claude-cli", label: "Claude CLI", path: claudeCLIConfigPath, entry: jsonHTTPEntry("http", false)},
		{key: "claude-desktop", label: "Claude Desktop", path: claudeDesktopConfigPath, entry: claudeDesktopEntry},
	}
}

// agentAliases maps friendly spellings to canonical agent keys.
var agentAliases = map[string]string{
	"claude":         "claude-cli",
	"claude-code":    "claude-cli",
	"claudecli":      "claude-cli",
	"codex-cli":      "codex",
	"gemini-cli":     "gemini",
	"cursor-agent":   "cursor",
	"claudedesktop":  "claude-desktop",
	"claude_desktop": "claude-desktop",
	"desktop":        "claude-desktop",
}

func normalizeAgentKey(key string) string {
	if canonical, ok := agentAliases[key]; ok {
		return canonical
	}
	return key
}

// jsonHTTPEntry builds the mcpServers entry for an HTTP/streamable-http agent.
// The bearer token is referenced via ${MCP_TOKEN} rather than embedded.
func jsonHTTPEntry(transport string, withTimeout bool) func(url, apiKey string) map[string]any {
	return func(url, _ string) map[string]any {
		entry := map[string]any{
			"type":    transport,
			"url":     url,
			"headers": map[string]any{"Authorization": mcpBearerRef},
		}
		if withTimeout {
			entry["timeout"] = mcpClientTimeoutMS
		}
		return entry
	}
}

// claudeDesktopEntry builds the npx/mcp-remote bridge entry for Claude Desktop.
// The desktop app is a GUI that does not inherit the launching shell, so the
// token must live in the entry's own env block, referenced as ${MCP_TOKEN}.
func claudeDesktopEntry(url, apiKey string) map[string]any {
	return map[string]any{
		"command": "npx",
		"args":    []any{"-y", "mcp-remote", url, "--header", "Authorization: " + mcpBearerRef},
		"env":     map[string]any{mcpTokenEnvVar: apiKey},
		"timeout": mcpClientTimeoutMS,
	}
}

// install writes the agent's MCP server entry, returning the action taken
// ("created" | "updated" | "unchanged").
func (a bootstrapAgent) install(path, serverName, url, apiKey string) (action, backup string, err error) {
	if a.codex {
		return writeCodexTOMLServer(path, serverName, url)
	}
	return writeJSONMCPServer(path, serverName, a.entry(url, apiKey))
}

// remove deletes the agent's MCP server entry, returning the action taken
// ("removed" | "unchanged" | "absent") and the backup path of the prior config.
func (a bootstrapAgent) remove(path, serverName string) (action, backup string, err error) {
	if a.codex {
		return removeCodexTOMLServer(path, serverName)
	}
	return removeJSONMCPServer(path, serverName)
}

// --- config path resolvers ---

func claudeCLIConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".claude.json"), nil
}

func geminiConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".gemini", "settings.json"), nil
}

func cursorConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".cursor", "mcp.json"), nil
}

// codexBootstrapConfigPath reuses CodexConfigPath so a custom --root keeps the
// codex MCP config isolated under the root directory, matching the existing
// codex session-launch behavior.
func codexBootstrapConfigPath() (string, error) {
	p := CodexConfigPath()
	if p == "" {
		return "", fmt.Errorf("resolve codex config path: home directory unavailable")
	}
	return p, nil
}

// claudeDesktopConfigPath returns the OS-specific Claude Desktop config path.
func claudeDesktopConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	switch runtime.GOOS {
	case "darwin":
		return filepath.Join(home, "Library", "Application Support", "Claude", "claude_desktop_config.json"), nil
	case "windows":
		if dir := os.Getenv("APPDATA"); dir != "" {
			return filepath.Join(dir, "Claude", "claude_desktop_config.json"), nil
		}
		return filepath.Join(home, "AppData", "Roaming", "Claude", "claude_desktop_config.json"), nil
	default:
		return filepath.Join(home, ".config", "Claude", "claude_desktop_config.json"), nil
	}
}

// bootstrapMCPURL builds the MCP endpoint URL from a base URL.
func bootstrapMCPURL(baseURL string) string {
	return strings.TrimRight(strings.TrimSpace(baseURL), "/") + mcpEndpointPath
}

// --- JSON mcpServers helpers (claude-cli, claude-desktop, gemini, cursor) ---

// writeJSONMCPServer loads (or creates) a JSON config file, sets
// mcpServers[serverName] to entry, and writes it back — preserving every other
// top-level key and sibling MCP server.
func writeJSONMCPServer(path, serverName string, entry map[string]any) (action, backup string, err error) {
	existed := ConfigFileExists(path)
	root, err := readJSONObject(path)
	if err != nil {
		return "", "", err
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		servers = map[string]any{}
	}
	if existed && equalJSON(servers[serverName], entry) {
		return "unchanged", "", nil
	}
	servers[serverName] = entry
	root["mcpServers"] = servers
	backup, err = writeJSONObject(path, root)
	if err != nil {
		return "", "", err
	}
	if existed {
		return "updated", backup, nil
	}
	return "created", backup, nil
}

// removeJSONMCPServer deletes mcpServers[serverName] from a JSON config file,
// leaving all sibling servers and other keys intact.
func removeJSONMCPServer(path, serverName string) (action, backup string, err error) {
	if !ConfigFileExists(path) {
		return "absent", "", nil
	}
	root, err := readJSONObject(path)
	if err != nil {
		return "", "", err
	}
	servers, _ := root["mcpServers"].(map[string]any)
	if servers == nil {
		return "unchanged", "", nil
	}
	if _, ok := servers[serverName]; !ok {
		return "unchanged", "", nil
	}
	delete(servers, serverName)
	root["mcpServers"] = servers
	backup, err = writeJSONObject(path, root)
	if err != nil {
		return "", "", err
	}
	return "removed", backup, nil
}

func readJSONObject(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	if strings.TrimSpace(string(data)) == "" {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}

func writeJSONObject(path string, root map[string]any) (string, error) {
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return "", fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	return writeConfigFileWithBackup(path, data)
}

// --- backup-before-write ---

// backupConfigFile copies an existing config file into <RootDir>/.backup before
// it is modified, so a bootstrap/uninstall run never destroys the prior config
// irretrievably. Returns the backup path, or "" when the source does not exist
// (a freshly-created file has nothing to back up).
func backupConfigFile(path string) (string, error) {
	if !ConfigFileExists(path) {
		return "", nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s for backup: %w", path, err)
	}
	backupDir := filepath.Join(RootDir(), ".backup")
	if err := os.MkdirAll(backupDir, 0o755); err != nil {
		return "", fmt.Errorf("create backup dir %s: %w", backupDir, err)
	}
	dst := filepath.Join(backupDir, filepath.Base(path)+"."+backupTimestamp()+".bak")
	if err := os.WriteFile(dst, data, 0o600); err != nil {
		return "", fmt.Errorf("write backup %s: %w", dst, err)
	}
	return dst, nil
}

// backupTimestamp returns a sortable UTC timestamp with nanosecond precision so
// two backups of the same file in quick succession (e.g. repeated bootstrap
// runs within the same second) never collide and overwrite each other.
func backupTimestamp() string {
	return time.Now().UTC().Format("20060102T150405.000000000")
}

// writeConfigFileWithBackup backs up an existing file (into <RootDir>/.backup),
// then writes data to path. Returns the backup path ("" when the file did not
// previously exist). 0o600: config files may carry bearer tokens (e.g. Claude
// Desktop's env block).
func writeConfigFileWithBackup(path string, data []byte) (string, error) {
	backup, err := backupConfigFile(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("create dir for %s: %w", path, err)
	}
	if err := os.WriteFile(path, data, 0o600); err != nil {
		return "", fmt.Errorf("write %s: %w", path, err)
	}
	return backup, nil
}

// equalJSON reports whether two values serialize to identical JSON. json.Marshal
// sorts map keys, so the comparison is field-order independent, and numeric
// types compare by value (int 300000 == float64 300000 from a round-trip).
func equalJSON(a, b any) bool {
	ab, err1 := json.Marshal(a)
	bb, err2 := json.Marshal(b)
	if err1 != nil || err2 != nil {
		return false
	}
	return string(ab) == string(bb)
}

// --- Codex TOML helpers (line-based; no external dependency) ---

// writeCodexTOMLServer upserts a [mcp_servers.<name>] section into the Codex
// TOML config, preserving every other section, key, comment, and ordering. The
// token is referenced via bearer_token_env_var=MCP_TOKEN, never written to disk.
func writeCodexTOMLServer(path, serverName, url string) (action, backup string, err error) {
	existed := ConfigFileExists(path)
	content := ""
	if existed {
		b, err := os.ReadFile(path)
		if err != nil {
			return "", "", fmt.Errorf("read %s: %w", path, err)
		}
		content = string(b)
	}
	header := codexSectionHeader(serverName)
	body := []string{
		fmt.Sprintf("url = %q", url),
		fmt.Sprintf("bearer_token_env_var = %q", mcpTokenEnvVar),
	}
	updated, changed := upsertTOMLSection(content, header, body)
	if existed && !changed {
		return "unchanged", "", nil
	}
	backup, err = writeConfigFileWithBackup(path, []byte(updated))
	if err != nil {
		return "", "", err
	}
	if existed {
		return "updated", backup, nil
	}
	return "created", backup, nil
}

// removeCodexTOMLServer deletes a [mcp_servers.<name>] section from the Codex
// TOML config, leaving all other sections intact.
func removeCodexTOMLServer(path, serverName string) (action, backup string, err error) {
	if !ConfigFileExists(path) {
		return "absent", "", nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", "", fmt.Errorf("read %s: %w", path, err)
	}
	updated, changed := removeTOMLSection(string(b), codexSectionHeader(serverName))
	if !changed {
		return "unchanged", "", nil
	}
	backup, err = writeConfigFileWithBackup(path, []byte(updated))
	if err != nil {
		return "", "", err
	}
	return "removed", backup, nil
}

func codexSectionHeader(serverName string) string {
	return "[mcp_servers." + serverName + "]"
}

// upsertTOMLSection replaces the body of an existing `header` section (the lines
// from the header up to the next section header or EOF) with header+body, or
// appends a new section when the header is absent. Trailing blank lines inside
// the replaced section are preserved. Returns the new content and whether it
// differs from the input.
func upsertTOMLSection(content, header string, body []string) (string, bool) {
	lines := strings.Split(content, "\n")
	headerIdx := indexOfTrimmed(lines, header)
	newSection := append([]string{header}, body...)

	if headerIdx == -1 {
		trimmed := strings.TrimRight(content, "\n")
		var b strings.Builder
		if trimmed != "" {
			b.WriteString(trimmed)
			b.WriteString("\n\n")
		}
		b.WriteString(strings.Join(newSection, "\n"))
		b.WriteString("\n")
		out := b.String()
		return out, out != content
	}

	endIdx := nextSectionIndex(lines, headerIdx+1)
	section := lines[headerIdx:endIdx]
	trailingBlanks := countTrailingBlanks(section)

	replacement := make([]string, 0, len(newSection)+trailingBlanks)
	replacement = append(replacement, newSection...)
	for i := 0; i < trailingBlanks; i++ {
		replacement = append(replacement, "")
	}

	out := make([]string, 0, len(lines))
	out = append(out, lines[:headerIdx]...)
	out = append(out, replacement...)
	out = append(out, lines[endIdx:]...)
	result := strings.Join(out, "\n")
	return result, result != content
}

// removeTOMLSection deletes the `header` section and any blank lines that
// immediately precede it (to avoid leaving a double blank gap).
func removeTOMLSection(content, header string) (string, bool) {
	lines := strings.Split(content, "\n")
	headerIdx := indexOfTrimmed(lines, header)
	if headerIdx == -1 {
		return content, false
	}
	endIdx := nextSectionIndex(lines, headerIdx+1)
	startIdx := headerIdx
	for startIdx > 0 && strings.TrimSpace(lines[startIdx-1]) == "" {
		startIdx--
	}
	out := make([]string, 0, len(lines))
	out = append(out, lines[:startIdx]...)
	out = append(out, lines[endIdx:]...)
	return strings.Join(out, "\n"), true
}

func indexOfTrimmed(lines []string, target string) int {
	for i, line := range lines {
		if strings.TrimSpace(line) == target {
			return i
		}
	}
	return -1
}

// nextSectionIndex returns the index of the next TOML section header at or after
// `from`, or len(lines) if none remain.
func nextSectionIndex(lines []string, from int) int {
	for i := from; i < len(lines); i++ {
		if strings.HasPrefix(strings.TrimSpace(lines[i]), "[") {
			return i
		}
	}
	return len(lines)
}

func countTrailingBlanks(section []string) int {
	n := 0
	for i := len(section) - 1; i >= 1; i-- { // i >= 1: never count the header line
		if strings.TrimSpace(section[i]) == "" {
			n++
		} else {
			break
		}
	}
	return n
}

// --- vibeflow-cli config setup ---

// setupInitialConfig writes the initial vibeflow-cli config at cfgPath, storing
// the api key as APIToken (which vibeflow-cli injects as MCP_TOKEN at launch),
// the base URL as ServerURL, and the MCP server name. Existing config values
// are loaded first and preserved.
func setupInitialConfig(cfgPath, baseURL, apiKey, serverName string) (action, backup string, err error) {
	existed := ConfigFileExists(cfgPath)
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return "", "", err
	}
	cfg.ServerURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	cfg.APIToken = strings.TrimSpace(apiKey)
	if serverName != "" {
		cfg.MCPToolName = serverName
	}
	if existed {
		// Back up before SaveConfig overwrites. SaveConfig is shared with other
		// callers (wizard, migrations) and must stay backup-free, so the backup
		// lives here in the bootstrap path only.
		if backup, err = backupConfigFile(cfgPath); err != nil {
			return "", "", err
		}
	}
	if err := SaveConfig(cfg, cfgPath); err != nil {
		return "", "", err
	}
	if existed {
		return "updated", backup, nil
	}
	return "created", backup, nil
}

// --- agent selection ---

func selectBootstrapAgents(cmd *cobra.Command, agents []bootstrapAgent, csv string, all bool) ([]bootstrapAgent, error) {
	if all {
		return agents, nil
	}
	if strings.TrimSpace(csv) != "" {
		return resolveAgentsCSV(agents, csv)
	}
	return promptBootstrapAgents(cmd, agents)
}

func resolveAgentsCSV(agents []bootstrapAgent, csv string) ([]bootstrapAgent, error) {
	byKey := make(map[string]bootstrapAgent, len(agents))
	for _, a := range agents {
		byKey[a.key] = a
	}
	var out []bootstrapAgent
	seen := make(map[string]bool)
	for _, raw := range strings.Split(csv, ",") {
		key := normalizeAgentKey(strings.ToLower(strings.TrimSpace(raw)))
		if key == "" {
			continue
		}
		a, ok := byKey[key]
		if !ok {
			return nil, fmt.Errorf("unknown agent %q (valid: %s)", strings.TrimSpace(raw), strings.Join(agentKeys(agents), ", "))
		}
		if !seen[key] {
			out = append(out, a)
			seen[key] = true
		}
	}
	return out, nil
}

func agentKeys(agents []bootstrapAgent) []string {
	keys := make([]string, len(agents))
	for i, a := range agents {
		keys[i] = a.key
	}
	return keys
}

// promptBootstrapAgents shows a numbered picker and reads one line of input.
func promptBootstrapAgents(cmd *cobra.Command, agents []bootstrapAgent) ([]bootstrapAgent, error) {
	out := cmd.OutOrStdout()
	fmt.Fprintln(out, "Select coding agents to configure for the VibeFlow MCP server:")
	for i, a := range agents {
		fmt.Fprintf(out, "  [%d] %s\n", i+1, a.label)
	}
	fmt.Fprint(out, "Enter numbers (comma/space separated), 'a' for all, or 'q' to cancel: ")

	reader := bufio.NewReader(cmd.InOrStdin())
	line, err := reader.ReadString('\n')
	if err != nil && strings.TrimSpace(line) == "" {
		return nil, fmt.Errorf("read selection: %w", err)
	}
	return parseAgentSelection(line, agents)
}

// parseAgentSelection parses a picker response into the chosen agents. An empty
// response, 'q', or 'quit' selects nothing; 'a' or 'all' selects everything.
func parseAgentSelection(line string, agents []bootstrapAgent) ([]bootstrapAgent, error) {
	line = strings.TrimSpace(strings.ToLower(line))
	switch line {
	case "", "q", "quit":
		return nil, nil
	case "a", "all":
		return agents, nil
	}
	fields := strings.FieldsFunc(line, func(r rune) bool {
		return r == ',' || r == ' ' || r == '\t'
	})
	var out []bootstrapAgent
	seen := make(map[int]bool)
	for _, f := range fields {
		n, err := strconv.Atoi(f)
		if err != nil || n < 1 || n > len(agents) {
			return nil, fmt.Errorf("invalid selection %q (choose 1-%d, 'a', or 'q')", f, len(agents))
		}
		if !seen[n] {
			out = append(out, agents[n-1])
			seen[n] = true
		}
	}
	return out, nil
}

// --- change reporting ---

type bootstrapChange struct {
	target string
	path   string
	action string
	backup string
}

func printBootstrapChanges(out io.Writer, heading string, changes []bootstrapChange) {
	fmt.Fprintln(out, heading)
	for _, c := range changes {
		fmt.Fprintf(out, "  %-16s %-11s %s\n", c.target, "("+c.action+")", c.path)
		if c.backup != "" {
			fmt.Fprintf(out, "  %-16s %-11s backed up to %s\n", "", "", c.backup)
		}
	}
}

// --- commands ---

func bootstrapCmd() *cobra.Command {
	var apiKey, baseURL, agentsCSV string
	var allAgents bool

	cmd := &cobra.Command{
		Use:   "bootstrap",
		Short: "Configure the VibeFlow MCP server for your coding agents",
		Long: `Configure the VibeFlow MCP server for your coding agents and write the
initial vibeflow-cli config.

You will be prompted for which agents to configure (Codex, Gemini, Cursor,
Claude CLI, Claude Desktop) unless --agents or --all is supplied. Each agent's
config references the token via the MCP_TOKEN environment variable, which
vibeflow-cli injects at launch; the --api-key value is stored in the
vibeflow-cli config.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			apiKey = strings.TrimSpace(apiKey)
			if apiKey == "" {
				return fmt.Errorf("--api-key is required")
			}
			if strings.TrimSpace(baseURL) == "" {
				baseURL = defaultBootstrapBaseURL
			}
			serverName := resolveMCPServerName(cmd)
			cfgPath := resolveConfigPath(cmd)

			agents := bootstrapAgents()
			selected, err := selectBootstrapAgents(cmd, agents, agentsCSV, allAgents)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(selected) == 0 {
				fmt.Fprintln(out, "No agents selected; nothing to do.")
				return nil
			}

			url := bootstrapMCPURL(baseURL)
			var changes []bootstrapChange

			// 1. vibeflow-cli config: stores the api key (becomes MCP_TOKEN at
			//    launch) and honors --root/--mcp.
			cfgAction, cfgBackup, err := setupInitialConfig(cfgPath, baseURL, apiKey, serverName)
			if err != nil {
				return fmt.Errorf("setup vibeflow-cli config: %w", err)
			}
			changes = append(changes, bootstrapChange{target: "vibeflow-cli", path: cfgPath, action: cfgAction, backup: cfgBackup})

			// 2. Per-agent MCP config.
			haveClaudeCLI := false
			for _, a := range selected {
				if a.key == "claude-cli" {
					haveClaudeCLI = true
				}
				path, err := a.path()
				if err != nil {
					return fmt.Errorf("%s: %w", a.label, err)
				}
				if path == "" {
					changes = append(changes, bootstrapChange{target: a.label, path: "(unsupported on this OS)", action: "skipped"})
					continue
				}
				action, backup, err := a.install(path, serverName, url, apiKey)
				if err != nil {
					return fmt.Errorf("%s: %w", a.label, err)
				}
				changes = append(changes, bootstrapChange{target: a.label, path: path, action: action, backup: backup})
			}

			printBootstrapChanges(out, "Bootstrap complete. Changes:", changes)
			if haveClaudeCLI {
				fmt.Fprintf(out, "\nNote: Claude CLI honors the MCP_TIMEOUT env var (not a per-server timeout).\n"+
					"      If you configure long wait_for_work polls, export MCP_TIMEOUT=%d.\n", mcpClientTimeoutMS)
			}
			return nil
		},
	}

	cmd.Flags().StringVar(&apiKey, "api-key", "", "VibeFlow API key (required)")
	cmd.Flags().StringVar(&baseURL, "base-url", defaultBootstrapBaseURL, "VibeFlow base URL")
	cmd.Flags().StringVar(&agentsCSV, "agents", "", "Comma-separated agents to configure (codex,gemini,cursor,claude-cli,claude-desktop); skips the interactive picker")
	cmd.Flags().BoolVar(&allAgents, "all", false, "Configure all supported agents (skips the interactive picker)")
	return cmd
}

func uninstallCmd() *cobra.Command {
	var agentsCSV string
	var allAgents bool

	cmd := &cobra.Command{
		Use:   "uninstall",
		Short: "Remove the VibeFlow MCP server config that bootstrap installed",
		Long: `Remove the VibeFlow MCP server entry from your coding agents' config files.

Only the VibeFlow server entry is removed; sibling MCP servers and all other
config keys are preserved. You will be prompted for which agents to clean unless
--agents or --all is supplied.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			serverName := resolveMCPServerName(cmd)
			agents := bootstrapAgents()
			selected, err := selectBootstrapAgents(cmd, agents, agentsCSV, allAgents)
			if err != nil {
				return err
			}

			out := cmd.OutOrStdout()
			if len(selected) == 0 {
				fmt.Fprintln(out, "No agents selected; nothing to do.")
				return nil
			}

			var changes []bootstrapChange
			for _, a := range selected {
				path, err := a.path()
				if err != nil {
					return fmt.Errorf("%s: %w", a.label, err)
				}
				if path == "" {
					changes = append(changes, bootstrapChange{target: a.label, path: "(unsupported on this OS)", action: "skipped"})
					continue
				}
				action, backup, err := a.remove(path, serverName)
				if err != nil {
					return fmt.Errorf("%s: %w", a.label, err)
				}
				changes = append(changes, bootstrapChange{target: a.label, path: path, action: action, backup: backup})
			}

			printBootstrapChanges(out, "Uninstall complete. Changes:", changes)
			return nil
		},
	}

	cmd.Flags().StringVar(&agentsCSV, "agents", "", "Comma-separated agents to clean (codex,gemini,cursor,claude-cli,claude-desktop); skips the interactive picker")
	cmd.Flags().BoolVar(&allAgents, "all", false, "Remove from all supported agents (skips the interactive picker)")
	return cmd
}

// resolveMCPServerName returns the MCP server name from --mcp, defaulting to
// DefaultMCPToolName.
func resolveMCPServerName(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup("mcp"); f != nil {
		if v := strings.TrimSpace(f.Value.String()); v != "" {
			return v
		}
	}
	return DefaultMCPToolName
}

// resolveConfigPath returns the vibeflow-cli config path from --config,
// defaulting to ConfigPath() (which honors --root).
func resolveConfigPath(cmd *cobra.Command) string {
	if f := cmd.Flags().Lookup("config"); f != nil {
		if v := strings.TrimSpace(f.Value.String()); v != "" {
			return v
		}
	}
	return ConfigPath()
}
