package vibeflowcli

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"gopkg.in/yaml.v3"
)

// WorktreeConfig holds settings for git worktree management.
type WorktreeConfig struct {
	BaseDir       string `yaml:"base_dir"`
	AutoCreate    bool   `yaml:"auto_create"`
	CleanupOnKill string `yaml:"cleanup_on_kill"` // "ask", "always", "never"
	LastCustomDir string `yaml:"last_custom_dir,omitempty"`
}

// ErrorRecoveryConfig holds settings for automatic error detection and recovery.
type ErrorRecoveryConfig struct {
	Enabled           bool   `yaml:"enabled"`
	MaxRetries        int    `yaml:"max_retries"`
	DebounceSeconds   int    `yaml:"debounce_seconds"`
	BackoffMultiplier int    `yaml:"backoff_multiplier"`
}

// Config holds all vibeflow-cli configuration.
type Config struct {
	ServerURL        string                `yaml:"server_url"`
	APIToken         string                `yaml:"api_token"`
	DefaultProject   string                `yaml:"default_project"`
	DefaultWorkDir   string                `yaml:"default_work_dir"`
	TmuxSocket       string                `yaml:"tmux_socket"`
	PollInterval     int                   `yaml:"poll_interval_seconds"`
	ClaudeBinary     string                `yaml:"claude_binary"`
	Providers        map[string]Provider   `yaml:"providers"`
	Worktree         WorktreeConfig        `yaml:"worktree"`
	DefaultProvider  string                `yaml:"default_provider"`
	ViewMode         string                `yaml:"view_mode"` // "flat" or "grouped" (default: flat)
	ErrorRecovery    ErrorRecoveryConfig   `yaml:"error_recovery"`
	DirectoryHistory []string              `yaml:"directory_history,omitempty"`
}

// AddDirectoryToHistory adds a directory to the front of the history list,
// removing any duplicate and capping at 10 entries.
func (c *Config) AddDirectoryToHistory(dir string) {
	for i, d := range c.DirectoryHistory {
		if d == dir {
			c.DirectoryHistory = append(c.DirectoryHistory[:i], c.DirectoryHistory[i+1:]...)
			break
		}
	}
	c.DirectoryHistory = append([]string{dir}, c.DirectoryHistory...)
	if len(c.DirectoryHistory) > 10 {
		c.DirectoryHistory = c.DirectoryHistory[:10]
	}
}

// ResolveWorkDir returns the working directory to use. Priority:
// explicit > Config.DefaultWorkDir > current directory.
func (c *Config) ResolveWorkDir(explicit string) string {
	if explicit != "" {
		return explicit
	}
	if c.DefaultWorkDir != "" {
		return c.DefaultWorkDir
	}
	return "."
}

// DefaultConfig returns a Config with sensible defaults.
// Three built-in providers are included; user config merges on top.
func DefaultConfig() *Config {
	return &Config{
		ServerURL:       "http://localhost:7080",
		TmuxSocket:      "vibeflow",
		PollInterval:    5,
		ClaudeBinary:    "claude",
		DefaultProvider: "claude",
		Worktree: WorktreeConfig{
			BaseDir:       ".claude/worktrees",
			AutoCreate:    true,
			CleanupOnKill: "ask",
		},
		ErrorRecovery: ErrorRecoveryConfig{
			Enabled:           true,
			MaxRetries:        3,
			DebounceSeconds:   5,
			BackoffMultiplier: 2,
		},
		Providers: map[string]Provider{
			"claude": {
				Name:               "Claude Code",
				Binary:             "claude",
				LaunchTemplate:     "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}",
				PromptTemplate:     "",
				Env:                map[string]string{},
				VibeFlowIntegrated: true,
				SessionFile:        ".vibeflow-session",
				Default:            true,
			},
			"codex": {
				Name:               "OpenAI Codex CLI",
				Binary:             "codex",
				LaunchTemplate:     "{{.Binary}}{{ if .SkipPermissions }} --full-auto{{ end }}",
				PromptTemplate:     "",
				Env:                map[string]string{},
				VibeFlowIntegrated: false,
				SessionFile:        "",
			},
			"gemini": {
				Name:               "Google Gemini CLI",
				Binary:             "gemini",
				LaunchTemplate:     "{{.Binary}}{{ if .SkipPermissions }} -sandbox=none{{ end }}",
				PromptTemplate:     "",
				Env:                map[string]string{},
				VibeFlowIntegrated: false,
				SessionFile:        "",
			},
		},
	}
}

// ConfigPath returns the default config file path.
func ConfigPath() string {
	home, _ := os.UserHomeDir()
	return filepath.Join(home, ".vibeflow-cli", "config.yaml")
}

// LoadConfig reads config from file, falling back to defaults.
func LoadConfig(path string) (*Config, error) {
	cfg := DefaultConfig()

	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return cfg, nil
		}
		return nil, fmt.Errorf("read config: %w", err)
	}

	if err := yaml.Unmarshal(data, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	// Environment variable overrides
	if v := os.Getenv("VIBEFLOW_URL"); v != "" {
		cfg.ServerURL = v
	}
	if v := os.Getenv("VIBEFLOW_TOKEN"); v != "" {
		cfg.APIToken = v
	}

	return cfg, nil
}

// SaveConfig writes config to the given path.
func SaveConfig(cfg *Config, path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		return fmt.Errorf("create config dir: %w", err)
	}

	data, err := yaml.Marshal(cfg)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}

	return os.WriteFile(path, data, 0600)
}

// ConfigFileExists reports whether the config file exists at the given path.
func ConfigFileExists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

// CheckServerReachable tests if the vibeflow server is reachable with a
// short-timeout HEAD request. Returns nil if reachable, error otherwise.
func CheckServerReachable(serverURL string) error {
	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Head(serverURL + "/rest/v1/vibeflow/projects")
	if err != nil {
		return fmt.Errorf("server unreachable: %w", err)
	}
	resp.Body.Close()
	return nil
}
