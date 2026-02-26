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
	"os/exec"
	"path/filepath"
	"sort"
	"sync"
)

// Provider describes an AI coding agent that can be launched in a tmux session.
// Providers are data objects loaded from config YAML, not Go interfaces.
type Provider struct {
	Name               string            `yaml:"name"`
	Binary             string            `yaml:"binary"`
	LaunchTemplate     string            `yaml:"launch_template"`
	PromptTemplate     string            `yaml:"prompt_template"`
	Env                map[string]string  `yaml:"env"`
	VibeFlowIntegrated bool              `yaml:"vibeflow_integrated"`
	SessionFile        string            `yaml:"session_file"`
	Default            bool              `yaml:"default"`
}

// ProviderRegistry holds configured providers and caches binary availability.
type ProviderRegistry struct {
	providers map[string]Provider

	mu        sync.Mutex
	available map[string]bool // cached exec.LookPath results
}

// NewProviderRegistry creates a registry from the config's provider map.
// It merges user-defined providers on top of built-in defaults so that
// old configs without a providers section still work.
func NewProviderRegistry(cfg *Config) *ProviderRegistry {
	providers := make(map[string]Provider, len(cfg.Providers))
	for k, v := range cfg.Providers {
		providers[k] = v
	}

	r := &ProviderRegistry{
		providers: providers,
		available: make(map[string]bool, len(providers)),
	}
	r.refreshAvailability()
	return r
}

// List returns all configured providers sorted alphabetically by key.
func (r *ProviderRegistry) List() []Provider {
	keys := make([]string, 0, len(r.providers))
	for k := range r.providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)

	out := make([]Provider, 0, len(keys))
	for _, k := range keys {
		out = append(out, r.providers[k])
	}
	return out
}

// Available returns only the providers whose binary is found on PATH.
func (r *ProviderRegistry) Available() []Provider {
	r.mu.Lock()
	defer r.mu.Unlock()

	var out []Provider
	for key, p := range r.providers {
		if r.available[key] {
			out = append(out, p)
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// Get returns a provider by its config key (e.g. "claude", "codex").
func (r *ProviderRegistry) Get(name string) (Provider, bool) {
	p, ok := r.providers[name]
	return p, ok
}

// Default returns the provider marked as default. If none is marked, it
// falls back to "claude". If that doesn't exist either, it returns the
// first provider alphabetically.
func (r *ProviderRegistry) Default() Provider {
	for _, p := range r.providers {
		if p.Default {
			return p
		}
	}
	if p, ok := r.providers["claude"]; ok {
		return p
	}
	// Last resort: first alphabetically.
	list := r.List()
	if len(list) > 0 {
		return list[0]
	}
	return Provider{Name: "claude", Binary: "claude"}
}

// IsAvailable reports whether the named provider's binary is on PATH or is
// a valid absolute path.
func (r *ProviderRegistry) IsAvailable(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.available[name]
}

// Keys returns all provider keys sorted alphabetically.
func (r *ProviderRegistry) Keys() []string {
	keys := make([]string, 0, len(r.providers))
	for k := range r.providers {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// SetBinary updates a provider's binary path and re-checks availability.
// Returns true if the provider exists and the new binary is available.
func (r *ProviderRegistry) SetBinary(key, binary string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	p, ok := r.providers[key]
	if !ok {
		return false
	}
	p.Binary = binary
	r.providers[key] = p
	r.available[key] = checkBinaryAvailable(binary)
	return r.available[key]
}

// Refresh re-checks binary availability for all providers. Call this on
// TUI refresh so newly-installed binaries are detected.
func (r *ProviderRegistry) Refresh() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, p := range r.providers {
		r.available[key] = checkBinaryAvailable(p.Binary)
	}
}

// refreshAvailability is the unexported version called from the constructor.
func (r *ProviderRegistry) refreshAvailability() {
	r.mu.Lock()
	defer r.mu.Unlock()
	for key, p := range r.providers {
		r.available[key] = checkBinaryAvailable(p.Binary)
	}
}

// checkBinaryAvailable returns true if the binary exists as an absolute
// path (and is executable) or can be found on PATH via exec.LookPath.
func checkBinaryAvailable(binary string) bool {
	if filepath.IsAbs(binary) {
		return isExecutable(binary)
	}
	_, err := exec.LookPath(binary)
	return err == nil
}

// isExecutable reports whether the file at path exists and has at least one
// execute bit set.
func isExecutable(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.Mode()&0111 != 0
}
