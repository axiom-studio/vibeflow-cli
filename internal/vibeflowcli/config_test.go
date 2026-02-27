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
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDefaultConfig(t *testing.T) {
	cfg := DefaultConfig()

	if cfg.ServerURL != "http://localhost:7080" {
		t.Errorf("ServerURL = %q, want http://localhost:7080", cfg.ServerURL)
	}
	if cfg.TmuxSocket != "vibeflow" {
		t.Errorf("TmuxSocket = %q, want vibeflow", cfg.TmuxSocket)
	}
	if cfg.DefaultProvider != "claude" {
		t.Errorf("DefaultProvider = %q, want claude", cfg.DefaultProvider)
	}
	if cfg.PollInterval != 5 {
		t.Errorf("PollInterval = %d, want 5", cfg.PollInterval)
	}
	if cfg.ClaudeBinary != "claude" {
		t.Errorf("ClaudeBinary = %q, want claude", cfg.ClaudeBinary)
	}

	// Three built-in providers.
	if len(cfg.Providers) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(cfg.Providers))
	}
	for _, key := range []string{"claude", "codex", "gemini"} {
		if _, ok := cfg.Providers[key]; !ok {
			t.Errorf("missing provider %q", key)
		}
	}

	// Worktree defaults.
	if cfg.Worktree.BaseDir != ".claude/worktrees" {
		t.Errorf("Worktree.BaseDir = %q, want .claude/worktrees", cfg.Worktree.BaseDir)
	}
	if !cfg.Worktree.AutoCreate {
		t.Error("Worktree.AutoCreate should default to true")
	}
	if cfg.Worktree.CleanupOnKill != "ask" {
		t.Errorf("Worktree.CleanupOnKill = %q, want ask", cfg.Worktree.CleanupOnKill)
	}

	// Error recovery defaults.
	if !cfg.ErrorRecovery.Enabled {
		t.Error("ErrorRecovery.Enabled should default to true")
	}
	if cfg.ErrorRecovery.MaxRetries != 3 {
		t.Errorf("ErrorRecovery.MaxRetries = %d, want 3", cfg.ErrorRecovery.MaxRetries)
	}
}

func TestLoadConfig_MissingFile(t *testing.T) {
	cfg, err := LoadConfig(filepath.Join(t.TempDir(), "nonexistent.yaml"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// Should return defaults.
	if cfg.ServerURL != "http://localhost:7080" {
		t.Errorf("expected default ServerURL, got %q", cfg.ServerURL)
	}
	if len(cfg.Providers) != 3 {
		t.Errorf("expected 3 default providers, got %d", len(cfg.Providers))
	}
}

func TestLoadConfig_ValidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yaml := `server_url: https://my.server.com
api_token: tok123
tmux_socket: custom
poll_interval_seconds: 10
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ServerURL != "https://my.server.com" {
		t.Errorf("ServerURL = %q, want https://my.server.com", cfg.ServerURL)
	}
	if cfg.APIToken != "tok123" {
		t.Errorf("APIToken = %q, want tok123", cfg.APIToken)
	}
	if cfg.TmuxSocket != "custom" {
		t.Errorf("TmuxSocket = %q, want custom", cfg.TmuxSocket)
	}
	if cfg.PollInterval != 10 {
		t.Errorf("PollInterval = %d, want 10", cfg.PollInterval)
	}
}

func TestLoadConfig_InvalidYAML(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	if err := os.WriteFile(cfgPath, []byte(":::invalid yaml{{{"), 0644); err != nil {
		t.Fatal(err)
	}

	_, err := LoadConfig(cfgPath)
	if err == nil {
		t.Fatal("expected error for invalid YAML")
	}
	if !strings.Contains(err.Error(), "parse config") {
		t.Errorf("expected 'parse config' in error, got: %v", err)
	}
}

func TestLoadConfig_EnvOverrides(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	yaml := `server_url: https://original.com
api_token: original
`
	if err := os.WriteFile(cfgPath, []byte(yaml), 0644); err != nil {
		t.Fatal(err)
	}

	t.Setenv("VIBEFLOW_URL", "https://env.override.com")
	t.Setenv("VIBEFLOW_TOKEN", "env-token")

	cfg, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if cfg.ServerURL != "https://env.override.com" {
		t.Errorf("ServerURL = %q, want env override", cfg.ServerURL)
	}
	if cfg.APIToken != "env-token" {
		t.Errorf("APIToken = %q, want env-token", cfg.APIToken)
	}
}

func TestSaveConfig_RoundTrip(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "subdir", "config.yaml")

	cfg := DefaultConfig()
	cfg.ServerURL = "https://saved.com"
	cfg.APIToken = "saved-token"

	if err := SaveConfig(cfg, cfgPath); err != nil {
		t.Fatalf("SaveConfig failed: %v", err)
	}

	// Verify file exists.
	if !ConfigFileExists(cfgPath) {
		t.Fatal("config file should exist after save")
	}

	// Re-read and verify.
	loaded, err := LoadConfig(cfgPath)
	if err != nil {
		t.Fatalf("LoadConfig after save failed: %v", err)
	}
	if loaded.ServerURL != "https://saved.com" {
		t.Errorf("round-trip ServerURL = %q, want https://saved.com", loaded.ServerURL)
	}
	if loaded.APIToken != "saved-token" {
		t.Errorf("round-trip APIToken = %q, want saved-token", loaded.APIToken)
	}
}

func TestConfigFileExists(t *testing.T) {
	dir := t.TempDir()

	if ConfigFileExists(filepath.Join(dir, "nope.yaml")) {
		t.Error("should return false for missing file")
	}

	path := filepath.Join(dir, "exists.yaml")
	if err := os.WriteFile(path, []byte("x: 1"), 0644); err != nil {
		t.Fatal(err)
	}
	if !ConfigFileExists(path) {
		t.Error("should return true for existing file")
	}
}

func TestAddDirectoryToHistory(t *testing.T) {
	t.Run("adds to front", func(t *testing.T) {
		cfg := &Config{DirectoryHistory: []string{"/a", "/b"}}
		cfg.AddDirectoryToHistory("/c")
		if cfg.DirectoryHistory[0] != "/c" {
			t.Errorf("expected /c at front, got %q", cfg.DirectoryHistory[0])
		}
		if len(cfg.DirectoryHistory) != 3 {
			t.Errorf("expected 3 entries, got %d", len(cfg.DirectoryHistory))
		}
	})

	t.Run("deduplicates", func(t *testing.T) {
		cfg := &Config{DirectoryHistory: []string{"/a", "/b", "/c"}}
		cfg.AddDirectoryToHistory("/b")
		if cfg.DirectoryHistory[0] != "/b" {
			t.Errorf("expected /b at front, got %q", cfg.DirectoryHistory[0])
		}
		if len(cfg.DirectoryHistory) != 3 {
			t.Errorf("expected 3 entries (deduplicated), got %d", len(cfg.DirectoryHistory))
		}
		// /b should not appear twice.
		count := 0
		for _, d := range cfg.DirectoryHistory {
			if d == "/b" {
				count++
			}
		}
		if count != 1 {
			t.Errorf("expected /b to appear once, appeared %d times", count)
		}
	})

	t.Run("caps at 10", func(t *testing.T) {
		cfg := &Config{}
		for i := 0; i < 15; i++ {
			cfg.AddDirectoryToHistory(filepath.Join("/dir", string(rune('a'+i))))
		}
		if len(cfg.DirectoryHistory) != 10 {
			t.Errorf("expected 10 entries (capped), got %d", len(cfg.DirectoryHistory))
		}
	})

	t.Run("empty history", func(t *testing.T) {
		cfg := &Config{}
		cfg.AddDirectoryToHistory("/new")
		if len(cfg.DirectoryHistory) != 1 || cfg.DirectoryHistory[0] != "/new" {
			t.Errorf("expected [/new], got %v", cfg.DirectoryHistory)
		}
	})
}

func TestResolveWorkDir(t *testing.T) {
	t.Run("explicit wins", func(t *testing.T) {
		cfg := &Config{DefaultWorkDir: "/default"}
		got := cfg.ResolveWorkDir("/explicit")
		if got != "/explicit" {
			t.Errorf("expected /explicit, got %q", got)
		}
	})

	t.Run("default when no explicit", func(t *testing.T) {
		cfg := &Config{DefaultWorkDir: "/default"}
		got := cfg.ResolveWorkDir("")
		if got != "/default" {
			t.Errorf("expected /default, got %q", got)
		}
	})

	t.Run("dot when nothing set", func(t *testing.T) {
		cfg := &Config{}
		got := cfg.ResolveWorkDir("")
		if got != "." {
			t.Errorf("expected '.', got %q", got)
		}
	})
}

func TestParseCodexBearerTokenEnvVar(t *testing.T) {
	t.Run("extracts from vibeflow section", func(t *testing.T) {
		toml := `[other]
key = "value"

[mcp_servers.vibeflow]
bearer_token_env_var = "MY_TOKEN"
url = "http://localhost"
`
		got := parseCodexBearerTokenEnvVar(toml)
		if got != "MY_TOKEN" {
			t.Errorf("expected MY_TOKEN, got %q", got)
		}
	})

	t.Run("ignores other sections", func(t *testing.T) {
		toml := `[mcp_servers.other]
bearer_token_env_var = "WRONG"
`
		got := parseCodexBearerTokenEnvVar(toml)
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("handles single quotes", func(t *testing.T) {
		toml := `[mcp_servers.vibeflow]
bearer_token_env_var = 'SINGLE_QUOTED'
`
		got := parseCodexBearerTokenEnvVar(toml)
		if got != "SINGLE_QUOTED" {
			t.Errorf("expected SINGLE_QUOTED, got %q", got)
		}
	})

	t.Run("handles no vibeflow section", func(t *testing.T) {
		got := parseCodexBearerTokenEnvVar("[other]\nkey = \"val\"\n")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("handles empty content", func(t *testing.T) {
		got := parseCodexBearerTokenEnvVar("")
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("skips comments", func(t *testing.T) {
		toml := `[mcp_servers.vibeflow]
# bearer_token_env_var = "COMMENTED"
bearer_token_env_var = "ACTUAL"
`
		got := parseCodexBearerTokenEnvVar(toml)
		if got != "ACTUAL" {
			t.Errorf("expected ACTUAL, got %q", got)
		}
	})
}

func TestCleanEnvToken(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"plain", "plain"},
		{"[bracketed]", "bracketed"},
		{"\"double-quoted\"", "double-quoted"},
		{"'single-quoted'", "single-quoted"},
		{"  spaces  ", "spaces"},
		{"\ttabs\t", "tabs"},
		{"\nnewlines\n", "newlines"},
		{"[\"both\"]", "both"},
		{"", ""},
	}

	for _, tc := range tests {
		got := cleanEnvToken(tc.input)
		if got != tc.want {
			t.Errorf("cleanEnvToken(%q) = %q, want %q", tc.input, got, tc.want)
		}
	}
}

func TestResolveProviderEnvVars_Claude(t *testing.T) {
	cfg := DefaultConfig()
	env, missing := ResolveProviderEnvVars(cfg, "claude")
	if missing != "" {
		t.Errorf("claude should have no missing env var, got %q", missing)
	}
	if len(env) != 0 {
		t.Errorf("claude should return empty env, got %v", env)
	}
}

func TestResolveProviderEnvVars_UnknownProvider(t *testing.T) {
	cfg := DefaultConfig()
	env, missing := ResolveProviderEnvVars(cfg, "unknown")
	if missing != "" {
		t.Errorf("unknown provider should have no missing env var, got %q", missing)
	}
	if len(env) != 0 {
		t.Errorf("unknown provider should return empty env, got %v", env)
	}
}

func TestResolveProviderEnvVars_GeminiFromSavedConfig(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SavedEnvVars = map[string]string{
		"GEMINI_API_KEY": "saved-key-123",
	}

	env, missing := ResolveProviderEnvVars(cfg, "gemini")
	if missing != "" {
		t.Errorf("expected no missing var, got %q", missing)
	}
	if env["GEMINI_API_KEY"] != "saved-key-123" {
		t.Errorf("expected saved key, got %q", env["GEMINI_API_KEY"])
	}
}

func TestResolveProviderEnvVars_GeminiFromEnv(t *testing.T) {
	cfg := DefaultConfig()
	t.Setenv("GEMINI_API_KEY", "env-key-456")

	env, missing := ResolveProviderEnvVars(cfg, "gemini")
	if missing != "" {
		t.Errorf("expected no missing var, got %q", missing)
	}
	if env["GEMINI_API_KEY"] != "env-key-456" {
		t.Errorf("expected env key, got %q", env["GEMINI_API_KEY"])
	}
}

func TestResolveProviderEnvVars_GeminiMissing(t *testing.T) {
	cfg := DefaultConfig()
	// Ensure GEMINI_API_KEY is not in environment.
	t.Setenv("GEMINI_API_KEY", "")
	os.Unsetenv("GEMINI_API_KEY")

	_, missing := ResolveProviderEnvVars(cfg, "gemini")
	if missing != "GEMINI_API_KEY" {
		t.Errorf("expected GEMINI_API_KEY missing, got %q", missing)
	}
}

func TestResolveProviderEnvVars_GeminiCleansToken(t *testing.T) {
	cfg := DefaultConfig()
	cfg.SavedEnvVars = map[string]string{
		"GEMINI_API_KEY": "[\"wrapped-key\"]",
	}

	env, _ := ResolveProviderEnvVars(cfg, "gemini")
	if env["GEMINI_API_KEY"] != "wrapped-key" {
		t.Errorf("expected cleaned key, got %q", env["GEMINI_API_KEY"])
	}
}

func TestMigrateProviders_SyncsLaunchTemplate(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	// Simulate a stale launch template.
	p := cfg.Providers["claude"]
	p.LaunchTemplate = "old-template {{.Binary}}"
	cfg.Providers["claude"] = p

	migrateProviders(cfg, cfgPath)

	// Should have been synced to default.
	defaults := DefaultConfig()
	if cfg.Providers["claude"].LaunchTemplate != defaults.Providers["claude"].LaunchTemplate {
		t.Errorf("expected launch template to be synced, got %q", cfg.Providers["claude"].LaunchTemplate)
	}
}

func TestMigrateProviders_RemovesVibeflowEnvVars(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	p := cfg.Providers["codex"]
	p.Env = map[string]string{
		"VIBEFLOW_URL":   "http://old",
		"VIBEFLOW_TOKEN": "old-token",
		"CUSTOM_VAR":     "keep-me",
	}
	cfg.Providers["codex"] = p

	migrateProviders(cfg, cfgPath)

	env := cfg.Providers["codex"].Env
	if _, ok := env["VIBEFLOW_URL"]; ok {
		t.Error("VIBEFLOW_URL should have been removed")
	}
	if _, ok := env["VIBEFLOW_TOKEN"]; ok {
		t.Error("VIBEFLOW_TOKEN should have been removed")
	}
	if env["CUSTOM_VAR"] != "keep-me" {
		t.Error("non-VIBEFLOW_ env vars should be preserved")
	}
}

func TestMigrateProviders_IgnoresCustomProviders(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	cfg.Providers["custom"] = Provider{
		Name:           "Custom Agent",
		Binary:         "my-agent",
		LaunchTemplate: "custom-template",
		Env: map[string]string{
			"VIBEFLOW_CUSTOM": "should-stay",
		},
	}

	migrateProviders(cfg, cfgPath)

	// Custom provider should not be modified.
	if cfg.Providers["custom"].LaunchTemplate != "custom-template" {
		t.Error("custom provider template should not be changed")
	}
	if cfg.Providers["custom"].Env["VIBEFLOW_CUSTOM"] != "should-stay" {
		t.Error("custom provider env vars should not be removed")
	}
}

func TestMigrateProviders_NoDirtyNoSave(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")

	cfg := DefaultConfig()
	// No changes needed — config file should NOT be written.
	migrateProviders(cfg, cfgPath)

	if ConfigFileExists(cfgPath) {
		t.Error("config file should not be written when no migration is needed")
	}
}
