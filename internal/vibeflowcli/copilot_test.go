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
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readCopilotTestConfig parses the config file written by the helper.
func readCopilotTestConfig(t *testing.T, home string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(filepath.Join(home, ".copilot", "config.json"))
	if err != nil {
		t.Fatalf("read copilot config: %v", err)
	}
	var root map[string]any
	if err := json.Unmarshal(data, &root); err != nil {
		t.Fatalf("parse copilot config: %v", err)
	}
	return root
}

func trustedFolderStrings(t *testing.T, root map[string]any) []string {
	t.Helper()
	raw, _ := root["trustedFolders"].([]any)
	out := make([]string, 0, len(raw))
	for _, f := range raw {
		s, ok := f.(string)
		if !ok {
			t.Fatalf("trustedFolders contains non-string entry: %#v", f)
		}
		out = append(out, s)
	}
	return out
}

func TestEnsureCopilotFirstRunConfig_CreatesFileWhenMissing(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "project")

	changed, err := EnsureCopilotFirstRunConfig(workDir)
	if err != nil {
		t.Fatalf("EnsureCopilotFirstRunConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true on first write")
	}

	root := readCopilotTestConfig(t, home)
	if got := trustedFolderStrings(t, root); len(got) != 1 || got[0] != workDir {
		t.Errorf("trustedFolders = %v, want [%s]", got, workDir)
	}
	if v, _ := root["appInstallNudgeResponded"].(bool); !v {
		t.Error("appInstallNudgeResponded should be true (desktop-app nudge would stall an unattended launch)")
	}
	if v, _ := root["appTipShown"].(bool); !v {
		t.Error("appTipShown should be true")
	}
	if _, ok := root["firstLaunchAt"].(string); !ok {
		t.Error("firstLaunchAt should be seeded")
	}
}

func TestEnsureCopilotFirstRunConfig_PreservesExistingKeysAndStripsComments(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "project")

	// Simulate a config.json as copilot v1.0.79 writes it: // comment
	// header, an already-trusted other folder, and an unrelated key that
	// must survive the merge untouched.
	dir := filepath.Join(home, ".copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	existing := `// User settings belong in settings.json.
// This file is managed automatically.
{
  "firstLaunchAt": "2026-03-11T00:00:00.000Z",
  "trustedFolders": ["/somewhere/else"],
  "reasoningSummariesCleanupDone": true
}
`
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte(existing), 0o644); err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureCopilotFirstRunConfig(workDir)
	if err != nil {
		t.Fatalf("EnsureCopilotFirstRunConfig: %v", err)
	}
	if !changed {
		t.Fatal("expected changed=true (workDir not yet trusted)")
	}

	root := readCopilotTestConfig(t, home)
	got := trustedFolderStrings(t, root)
	if len(got) != 2 || got[0] != "/somewhere/else" || got[1] != workDir {
		t.Errorf("trustedFolders = %v, want [/somewhere/else %s]", got, workDir)
	}
	if root["firstLaunchAt"] != "2026-03-11T00:00:00.000Z" {
		t.Errorf("firstLaunchAt = %v, want existing value preserved", root["firstLaunchAt"])
	}
	if v, _ := root["reasoningSummariesCleanupDone"].(bool); !v {
		t.Error("unrelated sibling key should be preserved verbatim")
	}
	if v, _ := root["appInstallNudgeResponded"].(bool); !v {
		t.Error("appInstallNudgeResponded should be forced true")
	}
}

func TestEnsureCopilotFirstRunConfig_IdempotentSecondCall(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	workDir := filepath.Join(home, "project")

	if _, err := EnsureCopilotFirstRunConfig(workDir); err != nil {
		t.Fatalf("first call: %v", err)
	}
	before, err := os.ReadFile(filepath.Join(home, ".copilot", "config.json"))
	if err != nil {
		t.Fatal(err)
	}

	changed, err := EnsureCopilotFirstRunConfig(workDir)
	if err != nil {
		t.Fatalf("second call: %v", err)
	}
	if changed {
		t.Error("expected changed=false when workDir is already trusted and markers set")
	}
	after, err := os.ReadFile(filepath.Join(home, ".copilot", "config.json"))
	if err != nil {
		t.Fatal(err)
	}
	if string(before) != string(after) {
		t.Error("second call must not rewrite the file")
	}
}

func TestEnsureCopilotFirstRunConfig_RelativeWorkDirStoredAbsolute(t *testing.T) {
	// Launch paths pass "." when launching from the project directory;
	// copilot matches trustedFolders against absolute paths, so a literal
	// "." entry never matches and the trust dialog still fires. Regression
	// caught live during feature #667 E2E.
	home := t.TempDir()
	t.Setenv("HOME", home)
	work := filepath.Join(home, "project")
	if err := os.MkdirAll(work, 0o755); err != nil {
		t.Fatal(err)
	}
	origWD, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chdir(origWD) })
	if err := os.Chdir(work); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureCopilotFirstRunConfig("."); err != nil {
		t.Fatalf("EnsureCopilotFirstRunConfig: %v", err)
	}

	root := readCopilotTestConfig(t, home)
	got := trustedFolderStrings(t, root)
	if len(got) != 1 || !filepath.IsAbs(got[0]) {
		t.Fatalf("trustedFolders = %v, want a single absolute path", got)
	}
	// macOS: /tmp symlinks to /private/tmp, so compare resolved paths.
	wantResolved, _ := filepath.EvalSymlinks(work)
	gotResolved, _ := filepath.EvalSymlinks(got[0])
	if gotResolved != wantResolved {
		t.Errorf("trustedFolders[0] = %q, want path resolving to %q", got[0], work)
	}
}

func TestEnsureCopilotFirstRunConfig_CorruptFileErrors(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir := filepath.Join(home, ".copilot")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "config.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}

	if _, err := EnsureCopilotFirstRunConfig(filepath.Join(home, "project")); err == nil {
		t.Fatal("expected error on corrupt config.json (must not silently clobber a file we cannot parse)")
	}
}
