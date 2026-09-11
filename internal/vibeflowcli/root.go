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

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

var (
	flagRootDir     string
	flagConfigPath  string
	flagServerURL   string
	flagProject     string
	flagMCPToolName string
	flagTmuxSocket  string

	buildVersion = "dev"
	buildCommit  = "none"
	buildDate    = "unknown"
)

// SetVersionInfo sets build metadata from ldflags.
func SetVersionInfo(version, commit, date string) {
	buildVersion = version
	buildCommit = commit
	buildDate = date
}

var rootCmd = &cobra.Command{
	Use:   "vibeflow-cli",
	Short: "Terminal UI for managing VibeFlow vibecoding sessions",
	Long: `vibeflow-cli is a terminal-based session manager for VibeFlow.
It provides a Bubble Tea TUI to launch, monitor, and manage multiple
Claude Code agent sessions via tmux.`,
	PersistentPreRun: func(cmd *cobra.Command, args []string) {
		if flagRootDir != "" {
			SetRootDir(flagRootDir)
		}
	},
	RunE: runTUI,
}

var versionCmd = &cobra.Command{
	Use:   "version",
	Short: "Print version information",
	Run: func(cmd *cobra.Command, args []string) {
		fmt.Printf("vibeflow %s\n  commit: %s\n  built:  %s\n", buildVersion, buildCommit, buildDate)
	},
}

func init() {
	rootCmd.PersistentFlags().StringVar(&flagRootDir, "root", "", "Root directory for config, sessions, and logs (default: ~/.vibeflow-cli)")
	rootCmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "Path to config file (default: <root>/config.yaml)")
	rootCmd.PersistentFlags().StringVar(&flagMCPToolName, "mcp", "", "MCP server tool name used in the agent init prompt (default: vibeflow)")
	rootCmd.PersistentFlags().StringVar(&flagTmuxSocket, "tmux-socket", "", "tmux socket name for sessions (default: 'vibeflow', or 'vibeflow-<hash>' for a custom --root)")
	rootCmd.Flags().StringVar(&flagServerURL, "server-url", "", "VibeFlow server URL (overrides config)")
	rootCmd.Flags().StringVar(&flagProject, "project", "", "Default project name")

	rootCmd.AddCommand(versionCmd)

	// Register headless subcommands.
	initSubcommands(rootCmd)
}

// Execute runs the root command.
func Execute() error {
	return rootCmd.Execute()
}

func runTUI(cmd *cobra.Command, args []string) error {
	// Enforce singleton — only one TUI instance at a time.
	if err := AcquirePIDLock(); err != nil {
		fmt.Fprintf(os.Stderr, "%v\n", err)
		return nil // Exit gracefully, not an error.
	}
	defer ReleasePIDLock()

	// Load config
	cfgPath := flagConfigPath
	if cfgPath == "" {
		cfgPath = ConfigPath()
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}

	// Resolve the tmux socket up front — it is independent of the setup wizard
	// (which never sets TmuxSocket): explicit --tmux-socket flag > config
	// tmux_socket > per-root derived. Creating the tmux manager and store here
	// (rather than lower down) lets the wizard gate below see existing state.
	cfg.TmuxSocket = ResolveTmuxSocket(flagTmuxSocket, cfg.TmuxSocket)
	tmux := NewTmuxManager(cfg.TmuxSocket)
	_ = tmux.EnsureServer() // Start tmux server on the vibeflow socket if not running.
	store := NewStore()

	// First-run setup wizard only when the root is genuinely uninitialized:
	// no config.yaml AND no existing session state. The headless spawn/dispatch
	// path never writes config.yaml, and a relocated/copied root likewise has a
	// sessions.json (and/or live tmux sessions) but no config — showing the
	// fresh-install wizard there would hide the user's running sessions behind a
	// setup screen instead of attaching. See issue #3484.
	if !ConfigFileExists(cfgPath) && !hasExistingSessionState(store, tmux) {
		setup := NewSetupModel(cfg, cfgPath)
		p := tea.NewProgram(setup)
		result, err := p.Run()
		if err != nil {
			return fmt.Errorf("setup wizard: %w", err)
		}
		if s, ok := result.(SetupModel); ok && s.Done() {
			cfg = s.Config()
		}
	}

	// CLI flag overrides
	if flagServerURL != "" {
		cfg.ServerURL = flagServerURL
	}
	if flagProject != "" {
		cfg.DefaultProject = flagProject
	}
	if flagMCPToolName != "" {
		cfg.MCPToolName = flagMCPToolName
	}

	// Initialize components
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	registry := NewProviderRegistry(cfg)

	// Initialize worktree manager (best-effort — non-fatal if not in a git repo).
	cwd, _ := os.Getwd()
	worktrees, _ := NewWorktreeManager(cwd, cfg.Worktree.BaseDir)
	cache := NewSessionCache()

	// Resolve project ID if project name is set
	var projectID int64
	if cfg.DefaultProject != "" {
		projects, err := client.ListProjects()
		if err == nil {
			for _, p := range projects {
				if p.Name == cfg.DefaultProject {
					projectID = p.ID
					break
				}
			}
		}
	}

	// Check server reachability (non-blocking — warn only).
	var serverWarning string
	if err := CheckServerReachable(cfg.ServerURL); err != nil {
		serverWarning = fmt.Sprintf("Server unreachable (%s)", cfg.ServerURL)
	}

	// Run TUI
	model := NewModel(cfg, client, tmux, worktrees, store, cache, registry, projectID)
	model.serverWarning = serverWarning

	// Detect dead sessions from cache and show restart popup if any.
	if tmuxNames, err := tmux.ListSessionNames(); err == nil {
		if deadSessions, err := cache.DeadSessions(tmuxNames); err == nil && len(deadSessions) > 0 {
			model.restartSelect = NewRestartSelectModel(deadSessions)
			model.activeView = ViewRestart
		}
	}
	defer model.logger.Close()
	// Alt-screen, focus reporting, and mouse mode are set on the View in
	// Bubble Tea v2 (see Model.View) rather than as program options here.
	p := tea.NewProgram(model)
	if _, err := p.Run(); err != nil {
		model.logger.Error("TUI fatal: %v", err)
		fmt.Fprintf(os.Stderr, "TUI error: %v\n", err)
		return err
	}

	// Remind user that sessions persist in the background.
	if sessions, err := tmux.ListSessions(); err == nil && len(sessions) > 0 {
		fmt.Fprintf(os.Stderr, "Sessions are running in the background. Run `vibeflow` to reconnect.\n")
	}

	return nil
}

// hasExistingSessionState reports whether the current root already holds
// vibeflow session state — either a sessions.json with at least one entry or a
// live tmux session on the resolved socket. It gates the first-run setup wizard
// so a root created by the headless spawn/dispatch path (which never writes
// config.yaml) or a relocated/copied state dir attaches to its sessions instead
// of being greeted by the fresh-install screen. Lookup errors are treated as
// "no state" so a genuinely uninitialized root still reaches the wizard.
func hasExistingSessionState(store *Store, tmux *TmuxManager) bool {
	if store != nil {
		if has, err := store.HasSessions(); err == nil && has {
			return true
		}
	}
	if tmux != nil {
		if names, err := tmux.ListSessionNames(); err == nil && len(names) > 0 {
			return true
		}
	}
	return false
}
