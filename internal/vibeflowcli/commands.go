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
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/spf13/cobra"

	"vibeflow-cli/sessionid"
)

// initSubcommands registers all CLI subcommands on the root command.
func initSubcommands(root *cobra.Command) {
	root.AddCommand(launchCmd())
	root.AddCommand(listCmd())
	root.AddCommand(switchCmd())
	root.AddCommand(killCmd())
	root.AddCommand(deleteCmd())
	root.AddCommand(restartCmd())
	root.AddCommand(worktreesCmd())
	root.AddCommand(checkCmd())
	root.AddCommand(configCmd())
	root.AddCommand(agentDocCmd())
}

// --- helpers shared by subcommands ---

// loadComponents initialises the standard set of managers from config.
func loadComponents(cfgPath string) (*Config, *TmuxManager, *Store, *WorktreeManager, *ProviderRegistry, error) {
	if cfgPath == "" {
		cfgPath = ConfigPath()
	}
	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		return nil, nil, nil, nil, nil, fmt.Errorf("load config: %w", err)
	}
	tmux := NewTmuxManager(cfg.TmuxSocket)
	tmux.SetLogger(NewLogger())
	store := NewStore()
	registry := NewProviderRegistry(cfg)

	cwd, _ := os.Getwd()
	worktrees, _ := NewWorktreeManager(cwd, cfg.Worktree.BaseDir)

	return cfg, tmux, store, worktrees, registry, nil
}

// --- launch ---

func launchCmd() *cobra.Command {
	var provider, branch, worktreeName string
	var worktree, skipPermissions, newBranch bool

	cmd := &cobra.Command{
		Use:   "launch",
		Short: "Create and launch a new session",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			cfg, tmux, store, wm, registry, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			// Ensure tmux server is running before creating sessions.
			_ = tmux.EnsureServer()

			if provider == "" {
				provider = cfg.DefaultProvider
			}
			if provider == "" {
				provider = "claude"
			}
			if branch == "" {
				branch = "main"
			}

			prov, ok := registry.Get(provider)
			if !ok {
				return fmt.Errorf("unknown provider %q", provider)
			}
			if !registry.IsAvailable(provider) {
				return fmt.Errorf("provider %q binary %q not found on PATH", provider, prov.Binary)
			}

			workDir := "."
			name := sessionid.GenerateSessionID(workDir)

			if worktree && wm != nil {
				wtName := worktreeName
				if wtName == "" {
					wtName = fmt.Sprintf("%s-%s-%d", provider, branch, time.Now().Unix())
				}
				wtPath, err := wm.CreateBranch(wtName, branch, newBranch)
				if err == nil {
					workDir = wtPath
				}
			}

			command, err := RenderLaunchCommand(prov.LaunchTemplate, LaunchTemplateVars{
				WorkDir:         workDir,
				ServerURL:       cfg.ServerURL,
				SkipPermissions: skipPermissions,
				Binary:          prov.Binary,
			})
			if err != nil || command == "" {
				command = prov.Binary
			}

			// Resolve provider env vars (e.g. codex bearer token).
			envVars, missingVar := ResolveProviderEnvVars(cfg, provider)
			if missingVar != "" {
				return fmt.Errorf("provider %q requires env var %q — set it in the environment or use the TUI wizard", provider, missingVar)
			}
			sessionEnv := prov.Env
			if len(envVars) > 0 {
				if sessionEnv == nil {
					sessionEnv = make(map[string]string)
				}
				for k, v := range envVars {
					sessionEnv[k] = v
				}
			}

			// Ensure all agent-specific markdown docs exist in the working directory.
			EnsureAllAgentDocs(workDir)

			if err := tmux.CreateSessionWithOpts(SessionOpts{
				Name:     name,
				Provider: provider,
				WorkDir:  workDir,
				Command:  command,
				Env:      sessionEnv,
				Branch:   branch,
				Project:  cfg.DefaultProject,
			}); err != nil {
				return err
			}

			tmuxName := tmux.FullSessionName(provider, name)

			// Bind Ctrl+Q to open vibeflow TUI popup inside the session.
			_ = tmux.BindSessionKeys(tmuxName)

			if prov.SessionFile != "" {
				_ = WriteSessionFileIfNeeded(workDir, "", name)
			}

			_ = store.Add(SessionMeta{
				Name:        name,
				TmuxSession: tmuxName,
				Provider:    provider,
				Branch:      branch,
				WorkingDir:  workDir,
				CreatedAt:   time.Now(),
			})

			fmt.Printf("Session %q launched (provider: %s, branch: %s)\n", name, provider, branch)
			return nil
		},
	}
	cmd.Flags().StringVar(&provider, "provider", "", "Provider key (claude, codex, gemini)")
	cmd.Flags().StringVar(&branch, "branch", "", "Git branch (default: main)")
	cmd.Flags().BoolVar(&worktree, "worktree", false, "Create a new git worktree for the session")
	cmd.Flags().StringVar(&worktreeName, "worktree-name", "", "Custom worktree directory name (default: auto-generated)")
	cmd.Flags().BoolVar(&newBranch, "new-branch", false, "Create a new git branch (used with --worktree)")
	cmd.Flags().BoolVar(&skipPermissions, "skip-permissions", false, "Skip permission prompts (autonomous mode)")
	return cmd
}

// --- list ---

func listCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "list",
		Short: "List active sessions",
		Aliases: []string{"ls"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, tmux, store, _, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			sessions, err := tmux.ListSessions()
			if err != nil {
				return err
			}
			if len(sessions) == 0 {
				fmt.Println("No active sessions.")
				return nil
			}

			// Load store metadata.
			storeMeta := make(map[string]SessionMeta)
			if metas, err := store.List(); err == nil {
				for _, m := range metas {
					storeMeta[m.TmuxSession] = m
				}
			}

			// Print table.
			fmt.Printf("%-24s %-12s %-16s %-10s\n", "NAME", "PROVIDER", "BRANCH", "STATUS")
			fmt.Println(strings.Repeat("-", 66))
			for _, s := range sessions {
				shortName := strings.TrimPrefix(s.Name, sessionPrefix)
				prov := "-"
				branch := "-"
				if meta, ok := storeMeta[s.Name]; ok {
					prov = meta.Provider
					branch = meta.Branch
				}
				status := "idle"
				if s.Attached {
					status = "attached"
				}
				fmt.Printf("%-24s %-12s %-16s %-10s\n", shortName, prov, branch, status)
			}
			return nil
		},
	}
}

// --- switch ---

func switchCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "switch <session-name>",
		Short: "Attach to a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, tmux, _, _, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}
			return tmux.AttachSession(args[0])
		},
	}
}

// --- kill ---

func killCmd() *cobra.Command {
	var cleanupWorktree bool

	cmd := &cobra.Command{
		Use:   "kill <session-name>",
		Short: "Kill a session",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, tmux, store, wm, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			name := args[0]
			if err := tmux.KillSession(name); err != nil {
				return fmt.Errorf("kill session: %w", err)
			}

			if meta, found, _ := store.Get(name); found {
				RemoveSessionFile(meta.WorkingDir, meta.Persona)
				if cleanupWorktree && meta.WorktreePath != "" && wm != nil {
					if err := wm.Remove(meta.WorktreePath, true); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree: %v\n", err)
					}
				}
				_ = store.Remove(name)
			}

			fmt.Printf("Session %q killed.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&cleanupWorktree, "cleanup-worktree", false, "Also remove the git worktree")
	return cmd
}

// --- delete (alias for kill) ---

func deleteCmd() *cobra.Command {
	var cleanupWorktree bool

	cmd := &cobra.Command{
		Use:   "delete <session-name>",
		Short: "Delete (kill) a session",
		Long:  "Delete a session by name. This is an alias for the 'kill' command.",
		Args:  cobra.ExactArgs(1),
		Aliases: []string{"rm"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, tmux, store, wm, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			name := args[0]
			if err := tmux.KillSession(name); err != nil {
				return fmt.Errorf("delete session: %w", err)
			}

			if meta, found, _ := store.Get(name); found {
				RemoveSessionFile(meta.WorkingDir, meta.Persona)
				if cleanupWorktree && meta.WorktreePath != "" && wm != nil {
					if err := wm.Remove(meta.WorktreePath, true); err != nil {
						fmt.Fprintf(os.Stderr, "Warning: failed to remove worktree: %v\n", err)
					}
				}
				_ = store.Remove(name)
			}

			fmt.Printf("Session %q deleted.\n", name)
			return nil
		},
	}
	cmd.Flags().BoolVar(&cleanupWorktree, "cleanup-worktree", false, "Also remove the git worktree")
	return cmd
}

// --- restart ---

func restartCmd() *cobra.Command {
	var skipPermissions bool

	cmd := &cobra.Command{
		Use:   "restart <session-name>",
		Short: "Restart a session (kill and re-launch with same settings)",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			cfg, tmux, store, _, registry, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			_ = tmux.EnsureServer()

			name := args[0]
			meta, found, err := store.Get(name)
			if err != nil {
				return fmt.Errorf("read store: %w", err)
			}
			if !found {
				return fmt.Errorf("session %q not found in store", name)
			}

			// Kill the existing tmux session (ignore error if already dead).
			_ = tmux.KillSession(meta.TmuxSession)

			// Resolve provider from stored metadata.
			provider := meta.Provider
			if provider == "" {
				provider = cfg.DefaultProvider
			}
			if provider == "" {
				provider = "claude"
			}

			prov, ok := registry.Get(provider)
			if !ok {
				return fmt.Errorf("unknown provider %q", provider)
			}
			if !registry.IsAvailable(provider) {
				return fmt.Errorf("provider %q binary %q not found on PATH", provider, prov.Binary)
			}

			workDir := meta.WorkingDir
			if workDir == "" {
				workDir = "."
			}
			branch := meta.Branch
			if branch == "" {
				branch = "main"
			}

			command, err := RenderLaunchCommand(prov.LaunchTemplate, LaunchTemplateVars{
				WorkDir:         workDir,
				ServerURL:       cfg.ServerURL,
				SkipPermissions: skipPermissions,
				Binary:          prov.Binary,
			})
			if err != nil || command == "" {
				command = prov.Binary
			}

			// Resolve provider env vars.
			envVars, missingVar := ResolveProviderEnvVars(cfg, provider)
			if missingVar != "" {
				return fmt.Errorf("provider %q requires env var %q — set it in the environment or use the TUI wizard", provider, missingVar)
			}
			sessionEnv := prov.Env
			if len(envVars) > 0 {
				if sessionEnv == nil {
					sessionEnv = make(map[string]string)
				}
				for k, v := range envVars {
					sessionEnv[k] = v
				}
			}

			// Ensure agent docs exist in the working directory.
			EnsureAllAgentDocs(workDir)

			if err := tmux.CreateSessionWithOpts(SessionOpts{
				Name:     name,
				Provider: provider,
				WorkDir:  workDir,
				Command:  command,
				Env:      sessionEnv,
				Branch:   branch,
				Project:  cfg.DefaultProject,
			}); err != nil {
				return err
			}

			tmuxName := tmux.FullSessionName(provider, name)

			// Re-bind session keys.
			_ = tmux.BindSessionKeys(tmuxName)

			if prov.SessionFile != "" {
				_ = WriteSessionFileIfNeeded(workDir, meta.Persona, name)
			}

			// Update store entry with new timestamp.
			_ = store.Add(SessionMeta{
				Name:              name,
				TmuxSession:       tmuxName,
				Provider:          provider,
				Project:           meta.Project,
				Persona:           meta.Persona,
				Branch:            branch,
				WorktreePath:      meta.WorktreePath,
				WorkingDir:        workDir,
				VibeFlowSessionID: meta.VibeFlowSessionID,
				CreatedAt:         time.Now(),
			})

			fmt.Printf("Session %q restarted (provider: %s, branch: %s)\n", name, provider, branch)
			return nil
		},
	}
	cmd.Flags().BoolVar(&skipPermissions, "skip-permissions", false, "Skip permission prompts (autonomous mode)")
	return cmd
}

// --- worktrees ---

func worktreesCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "worktrees",
		Short: "List git worktrees",
		Aliases: []string{"wt"},
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, _, _, wm, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}
			if wm == nil {
				return fmt.Errorf("not in a git repository")
			}

			wts, err := wm.List()
			if err != nil {
				return err
			}
			if len(wts) == 0 {
				fmt.Println("No worktrees.")
				return nil
			}

			fmt.Printf("%-50s %-20s %-10s\n", "PATH", "BRANCH", "HEAD")
			fmt.Println(strings.Repeat("-", 82))
			for _, wt := range wts {
				head := wt.HEAD
				if len(head) > 8 {
					head = head[:8]
				}
				branch := wt.Branch
				if wt.Detached {
					branch = "(detached)"
				}
				if wt.Bare {
					branch = "(bare)"
				}
				fmt.Printf("%-50s %-20s %-10s\n", wt.Path, branch, head)
			}
			return nil
		},
	}
}

// --- check ---

func checkCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "check [directory]",
		Short: "Check for session conflicts",
		Args:  cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath, _ := cmd.Flags().GetString("config")
			_, tmux, _, _, _, err := loadComponents(cfgPath)
			if err != nil {
				return err
			}

			dir := "."
			if len(args) > 0 {
				dir = args[0]
			}

			result := CheckConflict(dir, "", tmux)
			switch result.Status {
			case NoConflict:
				fmt.Println("No conflicts detected.")
			case ActiveConflict:
				fmt.Printf("ACTIVE conflict: session %s (provider: %s)\n", result.SessionID, result.Provider)
				fmt.Printf("File: %s\n", result.FilePath)
				os.Exit(1)
			case StaleConflict:
				fmt.Printf("STALE conflict: session %s (provider: %s) — no longer running\n", result.SessionID, result.Provider)
				fmt.Printf("File: %s\n", result.FilePath)
				fmt.Println("Run with --cleanup to remove the stale session file.")
				os.Exit(1)
			}
			return nil
		},
	}
}

func configCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "config",
		Short: "Re-run interactive configuration setup",
		RunE: func(cmd *cobra.Command, args []string) error {
			cfgPath := flagConfigPath
			if cfgPath == "" {
				cfgPath = ConfigPath()
			}
			cfg, err := LoadConfig(cfgPath)
			if err != nil {
				return fmt.Errorf("load config: %w", err)
			}
			setup := NewSetupModel(cfg, cfgPath)
			p := tea.NewProgram(setup, tea.WithAltScreen())
			if _, err := p.Run(); err != nil {
				return fmt.Errorf("setup wizard: %w", err)
			}
			return nil
		},
	}
}

// --- agent-doc ---

func agentDocCmd() *cobra.Command {
	return &cobra.Command{
		Use:   "agent-doc <provider>",
		Short: "Print the embedded agent doc template to stdout",
		Long:  "Print the embedded agent instruction file (CLAUDE.md, AGENTS.md, or GEMINI.md) for the given provider to stdout.",
		Args:  cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			content, err := GetAgentDoc(args[0])
			if err != nil {
				return err
			}
			_, err = os.Stdout.Write(content)
			return err
		},
	}
}
