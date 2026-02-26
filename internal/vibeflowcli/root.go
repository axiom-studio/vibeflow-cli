package vibeflowcli

import (
	"fmt"
	"os"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"
)

var (
	flagConfigPath string
	flagServerURL  string
	flagProject    string

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
	rootCmd.PersistentFlags().StringVar(&flagConfigPath, "config", "", "Path to config file (default: ~/.vibeflow-cli/config.yaml)")
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

	// First-run setup wizard if config file doesn't exist yet.
	if !ConfigFileExists(cfgPath) {
		setup := NewSetupModel(cfg, cfgPath)
		p := tea.NewProgram(setup, tea.WithAltScreen())
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

	// Initialize components
	client := NewClient(cfg.ServerURL, cfg.APIToken)
	tmux := NewTmuxManager(cfg.TmuxSocket)
	_ = tmux.EnsureServer() // Start tmux server on the vibeflow socket if not running.
	store := NewStore()
	registry := NewProviderRegistry(cfg)

	// Initialize worktree manager (best-effort — non-fatal if not in a git repo).
	cwd, _ := os.Getwd()
	worktrees, _ := NewWorktreeManager(cwd, cfg.Worktree.BaseDir)

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
	model := NewModel(cfg, client, tmux, worktrees, store, registry, projectID)
	model.serverWarning = serverWarning
	defer model.logger.Close()
	p := tea.NewProgram(model, tea.WithAltScreen())
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
