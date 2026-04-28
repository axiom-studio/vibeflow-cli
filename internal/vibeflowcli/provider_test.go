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

func TestNewProviderRegistry(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	keys := reg.Keys()
	if len(keys) != 5 {
		t.Fatalf("expected 5 providers, got %d", len(keys))
	}
	for _, k := range []string{"claude", "codex", "cursor", "gemini", "qwen"} {
		if _, ok := reg.Get(k); !ok {
			t.Errorf("missing provider %q", k)
		}
	}
}

func TestProviderRegistry_List(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	list := reg.List()
	if len(list) != 5 {
		t.Fatalf("expected 5 providers, got %d", len(list))
	}
	// Should be sorted alphabetically by key: claude, codex, cursor, gemini, qwen.
	names := []string{"Claude Code", "OpenAI Codex CLI", "Cursor Agent", "Google Gemini CLI", "Qwen Code"}
	for i, p := range list {
		if p.Name != names[i] {
			t.Errorf("list[%d].Name = %q, want %q", i, p.Name, names[i])
		}
	}
}

func TestProviderRegistry_Get(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	t.Run("exists", func(t *testing.T) {
		p, ok := reg.Get("claude")
		if !ok {
			t.Fatal("expected claude to exist")
		}
		if p.Name != "Claude Code" {
			t.Errorf("Name = %q, want Claude Code", p.Name)
		}
	})

	t.Run("not found", func(t *testing.T) {
		_, ok := reg.Get("nonexistent")
		if ok {
			t.Error("expected false for nonexistent provider")
		}
	})
}

func TestProviderRegistry_Default(t *testing.T) {
	t.Run("returns marked default", func(t *testing.T) {
		cfg := DefaultConfig()
		reg := NewProviderRegistry(cfg)
		d := reg.Default()
		if d.Name != "Claude Code" {
			t.Errorf("Default().Name = %q, want Claude Code", d.Name)
		}
	})

	t.Run("falls back to claude", func(t *testing.T) {
		cfg := DefaultConfig()
		// Remove Default flag from all providers.
		for k, p := range cfg.Providers {
			p.Default = false
			cfg.Providers[k] = p
		}
		reg := NewProviderRegistry(cfg)
		d := reg.Default()
		if d.Binary != "claude" {
			t.Errorf("Default().Binary = %q, want claude", d.Binary)
		}
	})

	t.Run("falls back to first alphabetically", func(t *testing.T) {
		cfg := &Config{
			Providers: map[string]Provider{
				"zebra": {Name: "Zebra Agent", Binary: "zebra"},
				"alpha": {Name: "Alpha Agent", Binary: "alpha"},
			},
		}
		reg := NewProviderRegistry(cfg)
		d := reg.Default()
		if d.Name != "Alpha Agent" {
			t.Errorf("Default().Name = %q, want Alpha Agent", d.Name)
		}
	})

	t.Run("empty registry", func(t *testing.T) {
		cfg := &Config{Providers: map[string]Provider{}}
		reg := NewProviderRegistry(cfg)
		d := reg.Default()
		if d.Binary != "claude" {
			t.Errorf("Default().Binary = %q, want claude (fallback)", d.Binary)
		}
	})
}

func TestProviderRegistry_Keys(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	keys := reg.Keys()
	expected := []string{"claude", "codex", "cursor", "gemini", "qwen"}
	if len(keys) != len(expected) {
		t.Fatalf("expected %d keys, got %d", len(expected), len(keys))
	}
	for i, k := range keys {
		if k != expected[i] {
			t.Errorf("keys[%d] = %q, want %q", i, k, expected[i])
		}
	}
}

func TestProviderRegistry_SetBinary(t *testing.T) {
	t.Run("updates existing provider", func(t *testing.T) {
		cfg := DefaultConfig()
		reg := NewProviderRegistry(cfg)

		// Set to a known binary that exists.
		ok := reg.SetBinary("claude", "sh")
		if !ok {
			t.Error("expected true when setting to an available binary")
		}
		p, _ := reg.Get("claude")
		if p.Binary != "sh" {
			t.Errorf("Binary = %q, want sh", p.Binary)
		}
	})

	t.Run("nonexistent provider", func(t *testing.T) {
		cfg := DefaultConfig()
		reg := NewProviderRegistry(cfg)

		ok := reg.SetBinary("nonexistent", "sh")
		if ok {
			t.Error("expected false for nonexistent provider")
		}
	})

	t.Run("unavailable binary", func(t *testing.T) {
		cfg := DefaultConfig()
		reg := NewProviderRegistry(cfg)

		ok := reg.SetBinary("claude", "this-binary-does-not-exist-xyz-123")
		if ok {
			t.Error("expected false for unavailable binary")
		}
		if reg.IsAvailable("claude") {
			t.Error("provider should be unavailable after setting bad binary")
		}
	})
}

func TestProviderRegistry_IsAvailable(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"exists":  {Name: "Exists", Binary: "sh"},
			"missing": {Name: "Missing", Binary: "this-binary-does-not-exist-xyz-123"},
		},
	}
	reg := NewProviderRegistry(cfg)

	if !reg.IsAvailable("exists") {
		t.Error("sh should be available")
	}
	if reg.IsAvailable("missing") {
		t.Error("nonexistent binary should not be available")
	}
	if reg.IsAvailable("unknown") {
		t.Error("unknown key should not be available")
	}
}

func TestProviderRegistry_Refresh(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"test": {Name: "Test", Binary: "this-binary-does-not-exist-xyz-123"},
		},
	}
	reg := NewProviderRegistry(cfg)

	if reg.IsAvailable("test") {
		t.Error("should not be available initially")
	}

	// Change binary to something that exists, then refresh.
	reg.SetBinary("test", "sh")
	reg.Refresh()
	if !reg.IsAvailable("test") {
		t.Error("should be available after refresh with valid binary")
	}
}

func TestProviderRegistry_Available(t *testing.T) {
	cfg := &Config{
		Providers: map[string]Provider{
			"exists":  {Name: "Exists", Binary: "sh"},
			"missing": {Name: "Missing", Binary: "this-binary-does-not-exist-xyz-123"},
		},
	}
	reg := NewProviderRegistry(cfg)

	avail := reg.Available()
	if len(avail) != 1 {
		t.Fatalf("expected 1 available, got %d", len(avail))
	}
	if avail[0].Name != "Exists" {
		t.Errorf("expected Exists, got %q", avail[0].Name)
	}
}

func TestCheckBinaryAvailable(t *testing.T) {
	t.Run("PATH binary", func(t *testing.T) {
		if !checkBinaryAvailable("sh") {
			t.Error("sh should be available on PATH")
		}
	})

	t.Run("nonexistent PATH binary", func(t *testing.T) {
		if checkBinaryAvailable("this-binary-does-not-exist-xyz-123") {
			t.Error("nonexistent binary should not be available")
		}
	})

	t.Run("absolute path executable", func(t *testing.T) {
		dir := t.TempDir()
		binPath := filepath.Join(dir, "my-binary")
		if err := os.WriteFile(binPath, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if !checkBinaryAvailable(binPath) {
			t.Error("absolute path to executable should be available")
		}
	})

	t.Run("absolute path non-executable", func(t *testing.T) {
		dir := t.TempDir()
		binPath := filepath.Join(dir, "not-exec")
		if err := os.WriteFile(binPath, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		if checkBinaryAvailable(binPath) {
			t.Error("non-executable file should not be available")
		}
	})

	t.Run("absolute path missing", func(t *testing.T) {
		if checkBinaryAvailable("/tmp/nonexistent-binary-xyz-123") {
			t.Error("missing absolute path should not be available")
		}
	})
}

func TestDefaultConfig_QwenProviderFields(t *testing.T) {
	cfg := DefaultConfig()
	p, ok := cfg.Providers["qwen"]
	if !ok {
		t.Fatal("expected qwen provider in defaults")
	}
	if p.Name != "Qwen Code" {
		t.Errorf("Name = %q, want Qwen Code", p.Name)
	}
	if p.Binary != "qwen" {
		t.Errorf("Binary = %q, want qwen", p.Binary)
	}
	if p.LaunchTemplate != "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}" {
		t.Errorf("LaunchTemplate = %q, want --yolo template", p.LaunchTemplate)
	}
	if p.PromptTemplate != "" {
		t.Errorf("PromptTemplate = %q, want empty", p.PromptTemplate)
	}
	if p.VibeFlowIntegrated {
		t.Error("VibeFlowIntegrated should be false in v1")
	}
	if p.SessionFile != "" {
		t.Errorf("SessionFile = %q, want empty", p.SessionFile)
	}
	if p.Default {
		t.Error("Default should be false (claude is the default)")
	}
}

func TestIsExecutable(t *testing.T) {
	dir := t.TempDir()

	t.Run("executable file", func(t *testing.T) {
		path := filepath.Join(dir, "exec")
		if err := os.WriteFile(path, []byte("#!/bin/sh\n"), 0755); err != nil {
			t.Fatal(err)
		}
		if !isExecutable(path) {
			t.Error("0755 file should be executable")
		}
	})

	t.Run("non-executable file", func(t *testing.T) {
		path := filepath.Join(dir, "noexec")
		if err := os.WriteFile(path, []byte("data"), 0644); err != nil {
			t.Fatal(err)
		}
		if isExecutable(path) {
			t.Error("0644 file should not be executable")
		}
	})

	t.Run("missing file", func(t *testing.T) {
		if isExecutable(filepath.Join(dir, "nope")) {
			t.Error("missing file should not be executable")
		}
	})
}

func TestResolvePersonaProvider(t *testing.T) {
	// Real registry with two installed providers (sh always exists on POSIX
	// and is what the existing TestProviderRegistry_IsAvailable suite uses)
	// and one configured-but-uninstalled provider.
	cfg := &Config{
		Providers: map[string]Provider{
			"claude": {Name: "Claude", Binary: "sh"},
			"qwen":   {Name: "Qwen", Binary: "sh"},
			"cursor": {Name: "Cursor", Binary: "this-binary-does-not-exist-xyz-123"},
		},
	}
	reg := NewProviderRegistry(cfg)
	defaultProvider, _ := reg.Get("claude")

	t.Run("inherit when no overrides set", func(t *testing.T) {
		p, key, err := ResolvePersonaProvider("developer", nil, "claude", defaultProvider, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "claude" || p.Name != "Claude" {
			t.Errorf("got (%s, %s), want (claude, Claude)", key, p.Name)
		}
	})

	t.Run("inherit when persona not in overrides map", func(t *testing.T) {
		overrides := map[string]string{"qa_lead": "qwen"}
		p, key, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "claude" || p.Name != "Claude" {
			t.Errorf("got (%s, %s), want (claude, Claude)", key, p.Name)
		}
	})

	t.Run("inherit when persona override is empty string", func(t *testing.T) {
		overrides := map[string]string{"developer": ""}
		p, key, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "claude" || p.Name != "Claude" {
			t.Errorf("got (%s, %s), want (claude, Claude)", key, p.Name)
		}
	})

	t.Run("explicit override to a different installed provider", func(t *testing.T) {
		overrides := map[string]string{"developer": "qwen"}
		p, key, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "qwen" || p.Name != "Qwen" {
			t.Errorf("got (%s, %s), want (qwen, Qwen)", key, p.Name)
		}
	})

	t.Run("override matching default key returns default without registry lookup", func(t *testing.T) {
		// Even if the registry lookup would succeed, this short-circuit avoids
		// a needless IsAvailable check and is the common no-op case when the
		// wizard writes the team default into PersonaProviders.
		overrides := map[string]string{"developer": "claude"}
		p, key, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, reg)
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if key != "claude" || p.Name != "Claude" {
			t.Errorf("got (%s, %s), want (claude, Claude)", key, p.Name)
		}
	})

	t.Run("override referencing unknown provider returns error", func(t *testing.T) {
		overrides := map[string]string{"developer": "ghost"}
		_, _, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, reg)
		if err == nil {
			t.Fatal("expected error for unknown provider, got nil")
		}
		if !strings.Contains(err.Error(), "ghost") || !strings.Contains(err.Error(), "developer") {
			t.Errorf("error %q should name both provider and persona", err.Error())
		}
	})

	t.Run("override referencing uninstalled provider returns error", func(t *testing.T) {
		overrides := map[string]string{"qa_lead": "cursor"}
		_, _, err := ResolvePersonaProvider("qa_lead", overrides, "claude", defaultProvider, reg)
		if err == nil {
			t.Fatal("expected error for uninstalled provider, got nil")
		}
		if !strings.Contains(err.Error(), "not installed") {
			t.Errorf("error %q should mention 'not installed'", err.Error())
		}
	})

	t.Run("nil registry with non-default override returns error", func(t *testing.T) {
		overrides := map[string]string{"developer": "qwen"}
		_, _, err := ResolvePersonaProvider("developer", overrides, "claude", defaultProvider, nil)
		if err == nil {
			t.Fatal("expected error for nil registry, got nil")
		}
	})

	t.Run("multi-persona team scenario — full resolution sweep", func(t *testing.T) {
		// AC subitems (a) all-inherit, (b) one-override-rest-inherit, (c) all-override
		// exercised together in the realistic shape that launchFromWizard sees.
		personas := []string{"developer", "qa_lead", "architect"}
		overrides := map[string]string{
			"developer": "qwen",   // explicit override
			"qa_lead":   "",       // empty → inherit
			// architect: missing key → inherit
		}
		want := map[string]string{
			"developer": "qwen",
			"qa_lead":   "claude",
			"architect": "claude",
		}
		for _, persona := range personas {
			_, key, err := ResolvePersonaProvider(persona, overrides, "claude", defaultProvider, reg)
			if err != nil {
				t.Fatalf("persona %s: unexpected error: %v", persona, err)
			}
			if key != want[persona] {
				t.Errorf("persona %s: got %q, want %q", persona, key, want[persona])
			}
		}
	})
}
