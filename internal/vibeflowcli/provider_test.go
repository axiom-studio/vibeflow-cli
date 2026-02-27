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
	"testing"
)

func TestNewProviderRegistry(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	keys := reg.Keys()
	if len(keys) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(keys))
	}
	for _, k := range []string{"claude", "codex", "gemini"} {
		if _, ok := reg.Get(k); !ok {
			t.Errorf("missing provider %q", k)
		}
	}
}

func TestProviderRegistry_List(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	list := reg.List()
	if len(list) != 3 {
		t.Fatalf("expected 3 providers, got %d", len(list))
	}
	// Should be sorted alphabetically by key: claude, codex, gemini.
	names := []string{"Claude Code", "OpenAI Codex CLI", "Google Gemini CLI"}
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
	expected := []string{"claude", "codex", "gemini"}
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
