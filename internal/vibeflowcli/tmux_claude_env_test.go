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
	"reflect"
	"strings"
	"testing"
)

func TestWithClaudeHardeningEnv(t *testing.T) {
	t.Run("claude with nil env gets all hardening vars", func(t *testing.T) {
		got := withClaudeHardeningEnv("claude", nil)
		if !reflect.DeepEqual(got, claudeHardeningEnv) {
			t.Errorf("got %v, want %v", got, claudeHardeningEnv)
		}
		if len(got) != 7 {
			t.Errorf("got %d vars, want 7", len(got))
		}
	})

	t.Run("claude preserves an explicit override and keeps unrelated keys", func(t *testing.T) {
		in := map[string]string{
			"DISABLE_TELEMETRY":  "0",        // explicit override — must win
			"ANTHROPIC_BASE_URL": "http://x", // unrelated — must survive
		}
		got := withClaudeHardeningEnv("claude", in)
		if got["DISABLE_TELEMETRY"] != "0" {
			t.Errorf("DISABLE_TELEMETRY = %q, want 0 (explicit override must win)", got["DISABLE_TELEMETRY"])
		}
		if got["ANTHROPIC_BASE_URL"] != "http://x" {
			t.Errorf("ANTHROPIC_BASE_URL = %q, want http://x (unrelated key must survive)", got["ANTHROPIC_BASE_URL"])
		}
		// The other six hardening vars are still applied as defaults.
		for k, v := range claudeHardeningEnv {
			if k == "DISABLE_TELEMETRY" {
				continue
			}
			if got[k] != v {
				t.Errorf("%s = %q, want %q", k, got[k], v)
			}
		}
	})

	t.Run("claude does not mutate the input map", func(t *testing.T) {
		in := map[string]string{"FOO": "bar"}
		_ = withClaudeHardeningEnv("claude", in)
		if !reflect.DeepEqual(in, map[string]string{"FOO": "bar"}) {
			t.Errorf("input map was mutated: %v", in)
		}
	})

	for _, provider := range []string{"codex", "gemini", "cursor", "qwen", ""} {
		t.Run("non-claude provider "+provider+" is unchanged", func(t *testing.T) {
			in := map[string]string{"OPENAI_API_KEY": "sk-test"}
			got := withClaudeHardeningEnv(provider, in)
			if !reflect.DeepEqual(got, in) {
				t.Errorf("provider %q: got %v, want %v (unchanged)", provider, got, in)
			}
			// A hardening key must not leak into a non-claude provider's env.
			if _, ok := got["DISABLE_TELEMETRY"]; ok {
				t.Errorf("provider %q leaked a claude hardening var", provider)
			}
		})
		t.Run("non-claude provider "+provider+" with nil env stays nil", func(t *testing.T) {
			if got := withClaudeHardeningEnv(provider, nil); got != nil {
				t.Errorf("provider %q: got %v, want nil", provider, got)
			}
		})
	}
}

// TestCreateSessionWithOpts_ClaudeHardeningEnv verifies end-to-end against a
// live tmux server that a claude session's environment carries all seven
// hardening vars (injected via tmux -e) while a codex session on the same
// server does not. No mocks — real tmux.
func TestCreateSessionWithOpts_ClaudeHardeningEnv(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	// Ensure the hardening vars are NOT inherited from the test process into the
	// tmux server's global environment, so the codex assertion isolates our own
	// injection from ambient inheritance.
	for k := range claudeHardeningEnv {
		if orig, ok := os.LookupEnv(k); ok {
			os.Unsetenv(k)
			t.Cleanup(func() { os.Setenv(k, orig) })
		}
	}

	tm := NewTmuxManager("vftest-claude-env")
	_, _ = tm.run("kill-server")
	defer func() { _, _ = tm.run("kill-server") }()

	workDir := t.TempDir()

	// claude session — every hardening var must be present.
	if err := tm.CreateSessionWithOpts(SessionOpts{
		Name:     "hardening",
		Provider: "claude",
		WorkDir:  workDir,
		Command:  "sleep 300",
	}); err != nil {
		t.Fatalf("CreateSessionWithOpts(claude) error = %v", err)
	}
	claudeEnv, err := tm.run("show-environment", "-t", tm.FullSessionName("claude", "hardening"))
	if err != nil {
		t.Fatalf("show-environment(claude) error = %v (%s)", err, claudeEnv)
	}
	for k := range claudeHardeningEnv {
		if want := k + "=1"; !strings.Contains(claudeEnv, want) {
			t.Errorf("claude session env missing %q\nshow-environment:\n%s", want, claudeEnv)
		}
	}

	// codex session on the same server — none of the hardening vars should appear.
	if err := tm.CreateSessionWithOpts(SessionOpts{
		Name:     "hardening",
		Provider: "codex",
		WorkDir:  workDir,
		Command:  "sleep 300",
	}); err != nil {
		t.Fatalf("CreateSessionWithOpts(codex) error = %v", err)
	}
	codexEnv, err := tm.run("show-environment", "-t", tm.FullSessionName("codex", "hardening"))
	if err != nil {
		t.Fatalf("show-environment(codex) error = %v (%s)", err, codexEnv)
	}
	for k := range claudeHardeningEnv {
		if strings.Contains(codexEnv, k+"=1") {
			t.Errorf("codex session unexpectedly has claude hardening var %q\nshow-environment:\n%s", k, codexEnv)
		}
	}
}
