package vibeflowcli

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
)

// reviewMCPCredential is the server's read-only VibeFlow credential for one
// review attempt or question claim (axiomcloud #5763): it reads one project
// only and stops working when the attempt ends. Older servers send none.
type reviewMCPCredential struct {
	Token     string `json:"token"`
	ProjectID int64  `json:"project_id"`
	ExpiresAt int64  `json:"expires_at"`
	ReadOnly  bool   `json:"read_only"`
}

const (
	// reviewMCPServerName is the only VibeFlow MCP server a review agent sees.
	reviewMCPServerName = "vibeflow_review"
	// reviewMCPTokenEnv carries the credential to harnesses that read a
	// bearer token from the environment (Codex).
	reviewMCPTokenEnv = "VIBEFLOW_REVIEW_MCP_TOKEN"
	// reviewNoMCPServer names no server: an allow-list of only this name
	// starts Gemini CLI and Qwen Code with no MCP server at all.
	reviewNoMCPServer = "vera_no_mcp_servers"
)

// reviewMCPPerRun lists the harnesses whose MCP servers Vera can set for one
// run. The others (gemini, cursor, kiro) always run without VibeFlow tools.
var reviewMCPPerRun = map[string]bool{"claude": true, "codex": true, "qwen": true, "copilot": true}

// reviewMCPSetup is how one review child reaches VibeFlow: Args go right
// after the harness binary, Env replaces the token variables, and Task tells
// the agent which VibeFlow tools it has.
type reviewMCPSetup struct {
	Args []string
	Env  map[string]string
	Task string
}

// reviewVibeflowMCP configures the agent's VibeFlow MCP connection from the
// server's per-attempt credential only. The runner owner's own VibeFlow token
// never reaches the agent: every variable that carries it is removed, and
// without a credential the agent starts with no VibeFlow MCP server.
func reviewVibeflowMCP(cfg *Config, provider, root string, env map[string]string, credential *reviewMCPCredential) (*reviewMCPSetup, error) {
	scrubReviewVibeflowToken(cfg, env)
	if credential != nil && (credential.Token == "" || credential.ProjectID <= 0 || strings.TrimSpace(cfg.ServerURL) == "") {
		credential = nil
	}
	setup := &reviewMCPSetup{Env: env, Task: "- No VibeFlow MCP server is available in this session; never call VibeFlow tools. Work from the pull request, its repository and the supplied files."}
	if credential != nil && !reviewMCPPerRun[provider] {
		setup.Task = fmt.Sprintf("- VibeFlow project %d was shared for this attempt, but the %s harness cannot be given a VibeFlow connection for one run, so it runs without VibeFlow tools; never call them. ../brief.json still carries the shared project context.", credential.ProjectID, provider)
		credential = nil
	}
	if credential != nil {
		setup.Task = fmt.Sprintf("- The VibeFlow MCP server %q is connected with a read-only credential for VibeFlow project %d, valid for this attempt only. Pass project_id %d to every list call; the server refuses writes, other projects and unscoped lists. No other VibeFlow server is available.", reviewMCPServerName, credential.ProjectID, credential.ProjectID)
	}
	url := strings.TrimRight(strings.TrimSpace(cfg.ServerURL), "/") + mcpEndpointPath
	writeServers := func(entry map[string]any) (string, error) {
		servers := map[string]any{}
		if entry != nil {
			servers[reviewMCPServerName] = entry
		}
		path := filepath.Join(root, "mcp.json")
		return path, saveReviewJSON(path, map[string]any{"mcpServers": servers})
	}
	bearer := map[string]any{}
	if credential != nil {
		bearer["Authorization"] = "Bearer " + credential.Token
	}
	switch provider {
	case "claude":
		// --strict-mcp-config: no user, plugin or checkout MCP server loads.
		var entry map[string]any
		if credential != nil {
			entry = map[string]any{"type": "http", "url": url, "headers": bearer}
		}
		path, err := writeServers(entry)
		if err != nil {
			return nil, err
		}
		setup.Args = []string{"--mcp-config", path, "--strict-mcp-config"}
	case "codex":
		names, err := codexMCPServerNames(env)
		if err != nil {
			return nil, err
		}
		for _, name := range names {
			if name == reviewMCPServerName {
				return nil, reviewReportable("the Codex configuration already defines an MCP server named %s, which Vera reserves for her own VibeFlow connection; rename it", name)
			}
			setup.Args = append(setup.Args, "-c", "mcp_servers."+name+".enabled=false")
		}
		if credential != nil {
			env[reviewMCPTokenEnv] = credential.Token
			setup.Args = append(setup.Args, "-c", fmt.Sprintf("mcp_servers.%s.url=%q", reviewMCPServerName, url), "-c", fmt.Sprintf("mcp_servers.%s.bearer_token_env_var=%q", reviewMCPServerName, reviewMCPTokenEnv))
		}
	case "qwen":
		if credential == nil {
			setup.Args = []string{"--allowed-mcp-server-names=" + reviewNoMCPServer}
			break
		}
		path, err := writeServers(map[string]any{"httpUrl": url, "headers": bearer})
		if err != nil {
			return nil, err
		}
		setup.Args = []string{"--mcp-config", path, "--allowed-mcp-server-names=" + reviewMCPServerName}
	case "gemini":
		setup.Args = []string{"--allowed-mcp-server-names=" + reviewNoMCPServer}
	case "copilot":
		// Copilot can add servers for one run but not drop the user's, so the
		// VibeFlow server bootstrap installs is disabled by name; it would fail
		// to authenticate anyway once MCP_TOKEN is gone.
		name := cfg.MCPToolName
		if name == "" {
			name = DefaultMCPToolName
		}
		setup.Args = []string{"--disable-mcp-server", name}
		if credential != nil {
			path, err := writeServers(map[string]any{"type": "http", "url": url, "headers": bearer, "tools": []any{"*"}})
			if err != nil {
				return nil, err
			}
			setup.Args = append(setup.Args, "--additional-mcp-config", "@"+path)
		}
	}
	return setup, nil
}

// scrubReviewVibeflowToken removes the runner owner's VibeFlow token from a
// review child's environment: MCP_TOKEN (which every bootstrapped harness
// config references), VIBEFLOW_TOKEN, and any variable holding the token's
// value, such as Codex's configured bearer_token_env_var.
func scrubReviewVibeflowToken(cfg *Config, env map[string]string) {
	token := cleanEnvToken(cfg.APIToken)
	for k, v := range env {
		if k == mcpTokenEnvVar || k == "VIBEFLOW_TOKEN" || (token != "" && cleanEnvToken(v) == token) {
			delete(env, k)
		}
	}
}

var (
	codexMCPServerHeader = regexp.MustCompile(`^\[\s*mcp_servers\s*\.\s*("[^"]*"|'[^']*'|[A-Za-z0-9_-]+)`)
	codexBareKey         = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)
)

// codexMCPServerNames lists the MCP server names in $CODEX_HOME/config.toml
// by their section headers; it reads no values. A name Codex's -c override
// cannot address is refused rather than left enabled.
func codexMCPServerNames(env map[string]string) ([]string, error) {
	path, err := codexConfigFile(env)
	if err != nil {
		return nil, err
	}
	f, err := os.Open(path)
	if os.IsNotExist(err) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	defer f.Close()
	seen := map[string]bool{}
	var names []string
	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 64<<10), 1<<20)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if strings.HasPrefix(line, "mcp_servers") && strings.Contains(line, "=") && !strings.HasPrefix(line, "[") {
			return nil, reviewReportable("the Codex configuration sets mcp_servers inline, which Vera cannot disable for one run; declare each server as an [mcp_servers.<name>] table")
		}
		m := codexMCPServerHeader.FindStringSubmatch(line)
		if m == nil {
			continue
		}
		name := m[1]
		if strings.ContainsAny(name, `"'`) {
			unquoted := strings.Trim(name, `"'`)
			if !codexBareKey.MatchString(unquoted) {
				return nil, reviewReportable("the Codex configuration has an MCP server named %s, which Vera cannot disable for one run; rename it with letters, digits, - or _", name)
			}
			name = unquoted
		}
		if !seen[name] {
			seen[name] = true
			names = append(names, name)
		}
	}
	return names, scanner.Err()
}

// withoutReviewMCPToken drops the credential from a receipt kept after its
// attempt ended; the server has revoked it by then.
func withoutReviewMCPToken(execution *reviewExecution) *reviewExecution {
	if execution == nil || execution.VibeflowMCP == nil {
		return execution
	}
	copied := *execution
	copied.VibeflowMCP = nil
	return &copied
}
