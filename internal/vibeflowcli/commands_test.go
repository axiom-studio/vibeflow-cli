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
	"bytes"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestRestartCmd_SkipPermissionsFlag verifies that `vibeflow restart`
// distinguishes an unset --skip-permissions flag (preserve stored value) from
// an explicitly set flag (override stored value with the passed value).
// This is the QA fix for Issue #517: before this change, passing
// --skip-permissions=false on the CLI did not downgrade a stored-autonomous
// session because the RunE only branched on `if skipPermissions`.
func TestRestartCmd_SkipPermissionsFlag(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantChanged  bool
		wantFlagVal  bool
		wantResolved bool // resolved SkipPermissions when stored value is true
	}{
		{
			name:         "flag omitted preserves stored true",
			args:         []string{"some-session"},
			wantChanged:  false,
			wantFlagVal:  false,
			wantResolved: true,
		},
		{
			name:         "flag explicit true keeps stored true",
			args:         []string{"--skip-permissions=true", "some-session"},
			wantChanged:  true,
			wantFlagVal:  true,
			wantResolved: true,
		},
		{
			name:         "flag explicit false overrides stored true",
			args:         []string{"--skip-permissions=false", "some-session"},
			wantChanged:  true,
			wantFlagVal:  false,
			wantResolved: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := restartCmd()
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("ParseFlags failed: %v", err)
			}

			changed := cmd.Flags().Changed("skip-permissions")
			if changed != tc.wantChanged {
				t.Errorf("Changed() = %v, want %v", changed, tc.wantChanged)
			}

			flagVal, err := cmd.Flags().GetBool("skip-permissions")
			if err != nil {
				t.Fatalf("GetBool failed: %v", err)
			}
			if flagVal != tc.wantFlagVal {
				t.Errorf("flag value = %v, want %v", flagVal, tc.wantFlagVal)
			}

			// Mirror the RunE resolution step: start from stored=true, apply
			// override only when the flag was explicitly set.
			resolved := true
			if changed {
				resolved = flagVal
			}
			if resolved != tc.wantResolved {
				t.Errorf("resolved SkipPermissions = %v, want %v", resolved, tc.wantResolved)
			}
		})
	}
}

func TestParsePersonaModels(t *testing.T) {
	got, err := parsePersonaModels("developer=gpt-5.1-codex, architect=opus")
	if err != nil {
		t.Fatalf("parsePersonaModels failed: %v", err)
	}
	if got["developer"] != "gpt-5.1-codex" || got["architect"] != "opus" {
		t.Fatalf("parsePersonaModels = %#v", got)
	}

	if _, err := parsePersonaModels("developer"); err == nil {
		t.Fatal("expected malformed --models entry to fail")
	}
}

func TestValidatePersonaModels(t *testing.T) {
	models := map[string]string{"developer": "gpt-5.1-codex"}
	if err := validatePersonaModels(models, []string{"developer", "architect"}); err != nil {
		t.Fatalf("validatePersonaModels failed: %v", err)
	}

	if err := validatePersonaModels(map[string]string{"qa_lead": "sonnet"}, []string{"developer"}); err == nil {
		t.Fatal("expected unknown persona model override to fail")
	}

	if err := validatePersonaModels(models, []string{""}); err == nil {
		t.Fatal("expected --models without personas to fail")
	}
}

func TestMatchesPersonaReplacement(t *testing.T) {
	workDir := t.TempDir()
	selected := map[string]struct{}{"developer": {}, "architect": {}}
	base := SessionMeta{
		SessionType: "vibeflow",
		Project:     "nimbus",
		Persona:     "developer",
		WorkingDir:  workDir,
	}

	if !matchesPersonaReplacement(base, filepath.Join(workDir, "."), "nimbus", selected) {
		t.Fatal("expected matching persona session to be reconciled")
	}

	tests := []struct {
		name string
		edit func(*SessionMeta)
	}{
		{"different project", func(m *SessionMeta) { m.Project = "other" }},
		{"unselected persona", func(m *SessionMeta) { m.Persona = "qa_lead" }},
		{"different directory", func(m *SessionMeta) { m.WorkingDir = t.TempDir() }},
		{"vanilla session", func(m *SessionMeta) { m.SessionType = "vanilla" }},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			meta := base
			tc.edit(&meta)
			if matchesPersonaReplacement(meta, workDir, "nimbus", selected) {
				t.Fatal("session outside reconciliation scope matched")
			}
		})
	}
}

func TestPreparePersonaSessionsReuseKeepsNewestAndRemovesDuplicates(t *testing.T) {
	dir := t.TempDir()
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))
	cache := NewSessionCacheWithPath(filepath.Join(dir, "cache.json"))
	tmux := NewTmuxManager("reuse-test-" + filepath.Base(dir))
	old := SessionMeta{Name: "session-old", VibeFlowSessionID: "session-old", TmuxSession: "missing-old", SessionType: "vibeflow", Project: "nimbus", Persona: "developer", WorkingDir: dir, CreatedAt: time.Unix(1, 0)}
	newest := SessionMeta{Name: "session-newest", VibeFlowSessionID: "session-newest", TmuxSession: "missing-newest", SessionType: "vibeflow", Project: "nimbus", Persona: "developer", WorkingDir: dir, CreatedAt: time.Unix(2, 0)}
	for _, meta := range []SessionMeta{old, newest} {
		if err := store.Add(meta); err != nil {
			t.Fatal(err)
		}
		if err := cache.Add(meta); err != nil {
			t.Fatal(err)
		}
	}

	ids, err := preparePersonaSessions(tmux, store, cache, dir, "nimbus", []string{"developer"}, true)
	if err != nil {
		t.Fatal(err)
	}
	if ids["developer"] != "session-newest" {
		t.Fatalf("reused ID = %q, want session-newest", ids["developer"])
	}
	if _, ok, err := store.Get(old.Name); err != nil || ok {
		t.Fatalf("duplicate session still stored: ok=%v err=%v", ok, err)
	}
	if _, ok, err := store.Get(newest.Name); err != nil || !ok {
		t.Fatalf("newest session was not retained: ok=%v err=%v", ok, err)
	}
}

func TestModelForPersona(t *testing.T) {
	models := map[string]string{"developer": "gpt-5.1-codex"}
	if got := modelForPersona("sonnet", models, "developer"); got != "gpt-5.1-codex" {
		t.Errorf("developer model = %q", got)
	}
	if got := modelForPersona("sonnet", models, "architect"); got != "sonnet" {
		t.Errorf("architect model = %q", got)
	}
}

// TestLaunchHelp_ListsEveryBuiltInProviderKey guards the --provider flag help
// against the #4334 defect class: the hardcoded key list omitted kiro and
// copilot, leaving new providers undiscoverable from `launch --help`. The
// usage string is now derived from the built-in provider map; this test keeps
// it that way.
func TestLaunchHelp_ListsEveryBuiltInProviderKey(t *testing.T) {
	var buf bytes.Buffer
	cmd := launchCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	if err := cmd.Help(); err != nil {
		t.Fatalf("render help: %v", err)
	}
	help := buf.String()
	for _, key := range NewProviderRegistry(DefaultConfig()).Keys() {
		if !strings.Contains(help, key) {
			t.Errorf("launch --help never mentions provider key %q", key)
		}
	}
}

// Regression test for issue #4541, and the sibling of
// TestLaunchHelp_ListsEveryBuiltInProviderKey. `vibeflow models` iterated a
// hardcoded provider list that went stale on every provider addition: it had
// been updated for copilot but not kiro, so kiro was advertised by
// `launch --help` and silently missing from `models`.
//
// Asserting only "today's output matches today's registry" would NOT catch
// that - the stale literal happened to match the set of providers that have
// catalogs. The defect is a coupling, so the test provokes it: give a registry
// provider that currently has no catalog one temporarily, and require `models`
// to pick it up. A hardcoded list cannot, a registry-derived loop does.
func TestModelsCmd_ProviderListTracksTheRegistry(t *testing.T) {
	keys := NewProviderRegistry(DefaultConfig()).Keys()

	var uncatalogued string
	for _, key := range keys {
		if len(ModelsForProvider(key)) == 0 {
			uncatalogued = key
			break
		}
	}
	if uncatalogued == "" {
		t.Skip("every registry provider has a catalog; nothing to distinguish a stale literal from a derived list")
	}

	t.Run("skips providers with no catalog instead of erroring", func(t *testing.T) {
		out, err := runModelsCmd(t, nil)
		if err != nil {
			t.Fatalf("`models` must not error just because %q has no catalog: %v", uncatalogued, err)
		}
		if strings.Contains(out, uncatalogued+":") {
			t.Errorf("`models` lists %q, which has no curated catalog\n---\n%s", uncatalogued, out)
		}
		for _, key := range keys {
			if len(ModelsForProvider(key)) > 0 && !strings.Contains(out, key+":") {
				t.Errorf("`models` never lists provider %q\n---\n%s", key, out)
			}
		}
	})

	t.Run("a newly catalogued provider appears without touching the command", func(t *testing.T) {
		// Simulates the next provider addition. This is the assertion a
		// hardcoded list fails.
		builtInProviderModels[uncatalogued] = []ModelOption{{ID: "test-model", Description: "injected by test"}}
		t.Cleanup(func() { delete(builtInProviderModels, uncatalogued) })

		out, err := runModelsCmd(t, nil)
		if err != nil {
			t.Fatalf("models: %v", err)
		}
		if !strings.Contains(out, uncatalogued+":") {
			t.Errorf("provider %q has a catalog but `models` does not list it - the provider list is not derived from the registry\n---\n%s", uncatalogued, out)
		}
	})

	t.Run("an explicit request for an uncatalogued provider still explains itself", func(t *testing.T) {
		_, err := runModelsCmd(t, []string{uncatalogued})
		if err == nil {
			t.Fatalf("`models %s` should error rather than print nothing", uncatalogued)
		}
		if !strings.Contains(err.Error(), "no curated model list") {
			t.Errorf("error = %q, want it to explain the missing catalog", err)
		}
	})
}

// runModelsCmd executes `models` with args and returns its captured output.
func runModelsCmd(t *testing.T, args []string) (string, error) {
	t.Helper()
	var buf bytes.Buffer
	cmd := modelsCmd()
	cmd.SetOut(&buf)
	cmd.SetErr(&buf)
	cmd.SilenceUsage = true
	cmd.SetArgs(args)
	err := cmd.Execute()
	return buf.String(), err
}
