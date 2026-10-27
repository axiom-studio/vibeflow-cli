package vibeflowcli

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

const reviewUserTokenCanary = "users-own-vibeflow-token-canary"

// reviewMCPTestEnv gives every harness a private home whose Codex config
// already has the user's VibeFlow server, and puts the user's VibeFlow token
// in every place a review child could inherit it from. Nothing outside the
// test's temporary directories is read or written.
func reviewMCPTestEnv(t *testing.T) *Config {
	t.Helper()
	isolateReviewHarnessHome(t)
	home := os.Getenv("HOME")
	codexHome := filepath.Join(home, ".codex")
	t.Setenv("CODEX_HOME", codexHome)
	if err := os.MkdirAll(codexHome, 0700); err != nil {
		t.Fatal(err)
	}
	config := "model = \"gpt-5\"\n\n[mcp_servers.vibeflow]\nurl = \"https://vf.example/rest/v1/vibeflow/mcp\"\nbearer_token_env_var = \"MY_VIBEFLOW\"\n\n[mcp_servers.\"other-tools\"]\ncommand = \"true\"\n\n[mcp_servers.other-tools.env]\nX = \"1\"\n"
	if err := os.WriteFile(filepath.Join(codexHome, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("MCP_TOKEN", reviewUserTokenCanary)
	t.Setenv("VIBEFLOW_TOKEN", reviewUserTokenCanary)
	t.Setenv("MY_VIBEFLOW", reviewUserTokenCanary)
	cfg := DefaultConfig()
	cfg.ServerURL = "https://vf.example/"
	cfg.APIToken = reviewUserTokenCanary
	fakeReviewHarnesses(t, cfg, "#!/bin/sh\nexit 0\n")
	return cfg
}

// reviewChildText is everything the agent can see of its launch: argv, env,
// the task and every file the runner wrote for it.
func reviewChildText(t *testing.T, spec *reviewChildSpec, root string) string {
	t.Helper()
	text := strings.Join(spec.Args, "\n") + "\n" + strings.Join(spec.Env, "\n")
	for _, name := range []string{"task.txt", "mcp.json", "schema.json"} {
		data, _ := os.ReadFile(filepath.Join(root, name))
		text += "\n" + string(data)
	}
	return text
}

func reviewEnvHasVibeflowToken(spec *reviewChildSpec) bool {
	return slices.ContainsFunc(spec.Env, func(kv string) bool {
		return strings.HasPrefix(kv, "MCP_TOKEN=") || strings.HasPrefix(kv, "VIBEFLOW_TOKEN=")
	})
}

func reviewMCPServers(t *testing.T, root string) map[string]map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var file struct {
		Servers map[string]map[string]any `json:"mcpServers"`
	}
	if err := json.Unmarshal(data, &file); err != nil {
		t.Fatal(err)
	}
	return file.Servers
}

// Without a credential (the repository shares no project context, the asker
// cannot see the project, or the server predates credentials) every harness
// starts with no VibeFlow MCP server, and the user's own VibeFlow token is
// in none of its argv, environment or files.
func TestReviewAgentStartsWithoutVibeflowMCPWithoutCredential(t *testing.T) {
	cfg := reviewMCPTestEnv(t)
	_, execution := reviewTestRepo(t)
	for _, provider := range reviewHarnessKeys {
		for _, question := range []bool{false, true} {
			t.Run(provider, func(t *testing.T) {
				exec := *execution
				if question {
					reviewWatchQuestionExecution(&exec)
				}
				root := t.TempDir()
				spec, err := prepareReviewProvider(context.Background(), cfg, provider, "", root, &exec, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", "")
				if err != nil {
					t.Fatal(err)
				}
				text := reviewChildText(t, spec, root)
				if strings.Contains(text, reviewUserTokenCanary) || reviewEnvHasVibeflowToken(spec) {
					t.Fatalf("the user's VibeFlow token reached the review agent:\n%s", text)
				}
				if !strings.Contains(text, "No VibeFlow MCP server is available in this session") {
					t.Fatal("the task does not say VibeFlow tools are unavailable")
				}
				switch provider {
				case "claude":
					if servers := reviewMCPServers(t, root); len(servers) != 0 || !slices.Contains(spec.Args, "--strict-mcp-config") {
						t.Fatalf("claude MCP servers %v args %q", servers, spec.Args)
					}
				case "codex":
					for _, want := range []string{"mcp_servers.vibeflow.enabled=false", "mcp_servers.other-tools.enabled=false"} {
						if !slices.Contains(spec.Args, want) {
							t.Fatalf("codex keeps the user's MCP server: %q", spec.Args)
						}
					}
					if strings.Contains(strings.Join(spec.Args, " "), reviewMCPServerName) {
						t.Fatalf("codex got a VibeFlow server without a credential: %q", spec.Args)
					}
				case "gemini", "qwen":
					if spec.Args[0] != "--allowed-mcp-server-names="+reviewNoMCPServer || slices.Contains(spec.Args, "--mcp-config") {
						t.Fatalf("%s MCP servers allowed: %q", provider, spec.Args)
					}
				case "copilot":
					if !slices.Equal(spec.Args[:2], []string{"--disable-mcp-server", "vibeflow"}) || slices.Contains(spec.Args, "--additional-mcp-config") {
						t.Fatalf("copilot args %q", spec.Args)
					}
				}
			})
		}
	}
}

// With a credential, each harness that can take an MCP server for one run is
// connected to VibeFlow through that credential alone; the others run without
// VibeFlow tools and their task says so. The user's token never appears.
func TestReviewAgentUsesOnlyTheScopedCredential(t *testing.T) {
	cfg := reviewMCPTestEnv(t)
	_, execution := reviewTestRepo(t)
	const scoped = "vfprc.scoped-review-credential"
	execution.VibeflowMCP = &reviewMCPCredential{Token: scoped, ProjectID: 66, ExpiresAt: 1, ReadOnly: true}
	url := "https://vf.example/rest/v1/vibeflow/mcp"
	for _, provider := range reviewHarnessKeys {
		t.Run(provider, func(t *testing.T) {
			root := t.TempDir()
			spec, err := prepareReviewProvider(context.Background(), cfg, provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", "")
			if err != nil {
				t.Fatal(err)
			}
			text := reviewChildText(t, spec, root)
			if strings.Contains(text, reviewUserTokenCanary) || reviewEnvHasVibeflowToken(spec) {
				t.Fatalf("the user's VibeFlow token reached the review agent:\n%s", text)
			}
			if got := strings.Contains(text, scoped); got != reviewMCPPerRun[provider] {
				t.Fatalf("scoped credential present=%v for %s:\n%s", got, provider, text)
			}
			task, _ := os.ReadFile(filepath.Join(root, "task.txt"))
			if reviewMCPPerRun[provider] != strings.Contains(string(task), `"vibeflow_review" is connected with a read-only credential for VibeFlow project 66`) {
				t.Fatalf("task does not describe the VibeFlow connection:\n%s", task)
			}
			if !reviewMCPPerRun[provider] && !strings.Contains(string(task), "cannot be given a VibeFlow connection for one run, so it runs without VibeFlow tools") {
				t.Fatalf("task does not say %s runs without VibeFlow tools:\n%s", provider, task)
			}
			switch provider {
			case "claude":
				servers := reviewMCPServers(t, root)
				server := servers[reviewMCPServerName]
				headers, _ := server["headers"].(map[string]any)
				if len(servers) != 1 || server["url"] != url || server["type"] != "http" || headers["Authorization"] != "Bearer "+scoped || !slices.Contains(spec.Args, "--strict-mcp-config") {
					t.Fatalf("claude MCP servers %v args %q", servers, spec.Args)
				}
			case "codex":
				for _, want := range []string{"mcp_servers.vibeflow.enabled=false", "mcp_servers.other-tools.enabled=false", `mcp_servers.vibeflow_review.url="` + url + `"`, `mcp_servers.vibeflow_review.bearer_token_env_var="` + reviewMCPTokenEnv + `"`} {
					if !slices.Contains(spec.Args, want) {
						t.Fatalf("codex args lack %s: %q", want, spec.Args)
					}
				}
				if !slices.Contains(spec.Env, reviewMCPTokenEnv+"="+scoped) || strings.Contains(strings.Join(spec.Args, " "), scoped) {
					t.Fatal("codex must read the credential from its environment, never argv")
				}
			case "qwen":
				server := reviewMCPServers(t, root)[reviewMCPServerName]
				if server["httpUrl"] != url || !slices.Contains(spec.Args, "--allowed-mcp-server-names="+reviewMCPServerName) || !slices.Contains(spec.Args, filepath.Join(root, "mcp.json")) {
					t.Fatalf("qwen server %v args %q", server, spec.Args)
				}
			case "copilot":
				server := reviewMCPServers(t, root)[reviewMCPServerName]
				if server["url"] != url || !slices.Contains(spec.Args, "@"+filepath.Join(root, "mcp.json")) || !slices.Contains(spec.Args, "--disable-mcp-server") {
					t.Fatalf("copilot server %v args %q", server, spec.Args)
				}
			case "gemini":
				if spec.Args[0] != "--allowed-mcp-server-names="+reviewNoMCPServer {
					t.Fatalf("gemini args %q", spec.Args)
				}
			}
			if strings.Contains(strings.Join(spec.Args, " "), scoped) {
				t.Fatalf("the credential is in argv, visible to other local users: %q", spec.Args)
			}
		})
	}
}

// The interactive launch keeps the same MCP servers: they go right after the
// harness binary in the rendered command.
func TestReviewInteractiveLaunchKeepsVeraMCPServers(t *testing.T) {
	cfg := reviewMCPTestEnv(t)
	_, execution := reviewTestRepo(t)
	execution.VibeflowMCP = &reviewMCPCredential{Token: "vfprc.interactive", ProjectID: 66, ReadOnly: true}
	for _, provider := range []string{"claude", "codex", "gemini"} {
		root := t.TempDir()
		spec, err := prepareReviewProvider(context.Background(), cfg, provider, "", root, execution, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", "")
		if err != nil {
			t.Fatal(err)
		}
		mcpArgs := slices.Clone(spec.MCPArgs)
		if err := makeReviewSpecInteractive(spec, cfg, provider, "", root, false); err != nil {
			t.Fatal(err)
		}
		command := spec.Args[len(spec.Args)-1]
		quoted := make([]string, len(mcpArgs))
		for i, arg := range mcpArgs {
			quoted[i] = shellQuote(arg)
		}
		if len(mcpArgs) == 0 || !strings.Contains(command, shellQuote(cfg.Providers[provider].Binary)+" "+strings.Join(quoted, " ")) && !strings.Contains(command, cfg.Providers[provider].Binary+" "+strings.Join(quoted, " ")) {
			t.Fatalf("%s interactive command lost Vera's MCP servers %q: %s", provider, mcpArgs, command)
		}
		if strings.Contains(command, reviewUserTokenCanary) || strings.Contains(strings.Join(spec.Env, "\n"), reviewUserTokenCanary) {
			t.Fatalf("%s interactive launch carries the user's token", provider)
		}
	}
}

// A Codex configuration whose MCP servers cannot be disabled for one run is
// refused rather than left connected.
func TestReviewCodexRefusesUndisableableMCPServers(t *testing.T) {
	cfg := reviewMCPTestEnv(t)
	_, execution := reviewTestRepo(t)
	path := filepath.Join(os.Getenv("CODEX_HOME"), "config.toml")
	for _, config := range []string{
		"mcp_servers = { vibeflow = { url = \"https://vf.example\" } }\n",
		"[mcp_servers.\"vibe flow\"]\nurl = \"https://vf.example\"\n",
		"[mcp_servers.vibeflow_review]\ncommand = \"true\"\n",
	} {
		if err := os.WriteFile(path, []byte(config), 0600); err != nil {
			t.Fatal(err)
		}
		if _, err := prepareReviewProvider(context.Background(), cfg, "codex", "", t.TempDir(), execution, &reviewBrief{Digest: strings.Repeat("a", 64)}, "", ""); err == nil {
			t.Fatalf("codex config accepted:\n%s", config)
		}
	}
}

// The claim's credential decodes from the server's payload, and a receipt
// kept after its attempt drops it.
func TestReviewExecutionCarriesAndDropsTheCredential(t *testing.T) {
	var execution reviewExecution
	if err := json.Unmarshal([]byte(`{"version":2,"vibeflow_mcp":{"token":"vfprc.x","project_id":66,"expires_at":5,"read_only":true}}`), &execution); err != nil {
		t.Fatal(err)
	}
	if c := execution.VibeflowMCP; c == nil || c.Token != "vfprc.x" || c.ProjectID != 66 || !c.ReadOnly {
		t.Fatalf("credential: %+v", c)
	}
	kept := withoutReviewMCPToken(&execution)
	if kept.VibeflowMCP != nil || execution.VibeflowMCP == nil {
		t.Fatal("the kept receipt still holds the credential, or the live one lost it")
	}
	var older reviewExecution
	if err := json.Unmarshal([]byte(`{"version":2}`), &older); err != nil || older.VibeflowMCP != nil {
		t.Fatal("an older server's claim must carry no credential")
	}
}
