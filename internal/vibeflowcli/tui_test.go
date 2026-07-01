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
	"path/filepath"
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
)

// detailPanelModel builds a minimal Model with a single session row selected,
// sufficient to exercise renderDetailPanel.
func detailPanelModel(row SessionRow, cfg *Config) Model {
	return Model{
		config:   cfg,
		sessions: []SessionRow{row},
	}
}

func TestRenderDetailPanel_GatewayEnv_ClaudeMasksSecrets(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "claude",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(120, 40), "")

	if !strings.Contains(out, "Gateway") || !strings.Contains(out, "enabled") {
		t.Errorf("detail panel missing Gateway enabled row:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway") {
		t.Errorf("detail panel missing non-secret base URL value:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_CUSTOM_HEADERS=<redacted>") {
		t.Errorf("detail panel must mask ANTHROPIC_CUSTOM_HEADERS:\n%s", out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestRenderDetailPanel_GatewayEnv_QwenMasksSecrets(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "qwen",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(200, 40), "")

	if !strings.Contains(out, "OPENAI_API_KEY=<redacted>") {
		t.Errorf("detail panel must mask OPENAI_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "OPENAI_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway/v1") {
		t.Errorf("detail panel missing non-secret base URL value:\n%s", out)
	}
	qwenKey := QwenCustomAPIKeyEnvName("OPENAI", "https://cloud.example.com/rest/v1/llm-gateway/v1")
	if !strings.Contains(out, qwenKey+"=<redacted>") {
		t.Errorf("detail panel must mask %s:\n%s", qwenKey, out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestRenderDetailPanel_GatewayEnv_GeminiUsesGeminiVars(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "gemini",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(200, 40), "")

	if !strings.Contains(out, "GEMINI_API_KEY=<redacted>") {
		t.Errorf("detail panel must mask GEMINI_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "GOOGLE_GEMINI_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway") {
		t.Errorf("detail panel missing Gemini gateway base URL:\n%s", out)
	}
	if strings.Contains(out, "OPENAI_API_KEY=") || strings.Contains(out, "OPENAI_BASE_URL=") {
		t.Errorf("gemini gateway env must not display OPENAI_* vars:\n%s", out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestProjectGrouping(t *testing.T) {
	// Pre-populate the repo-root cache so getRepoRoot never shells out to git.
	m := Model{
		repoRootCache: map[string]string{
			"/work/alpha":     "/work/alpha",
			"/work/alpha/sub": "/work/alpha",
			"/work/beta":      "/work/beta",
		},
		sessions: []SessionRow{
			{Name: "vibeflow_claude-a1", WorkingDir: "/work/alpha"},
			{Name: "vibeflow_codex-b1", WorkingDir: "/work/beta"},
			{Name: "vibeflow_gemini-a2", WorkingDir: "/work/alpha/sub"},
		},
	}

	groups := m.projectGroups()
	if len(groups) != 2 {
		t.Fatalf("projectGroups len = %d, want 2 (%+v)", len(groups), groups)
	}
	if groups[0].Label != "alpha" || len(groups[0].Sessions) != 2 {
		t.Errorf("group[0] = %+v, want label alpha with 2 sessions", groups[0])
	}
	if groups[1].Label != "beta" || len(groups[1].Sessions) != 1 {
		t.Errorf("group[1] = %+v, want label beta with 1 session", groups[1])
	}

	m.cursor = 1 // the beta session
	label, names := m.selectedProjectSessions()
	if label != "beta" || len(names) != 1 || names[0] != "vibeflow_codex-b1" {
		t.Errorf("selectedProjectSessions@beta = %q %v, want beta [vibeflow_codex-b1]", label, names)
	}
	m.cursor = 0 // an alpha session
	label, names = m.selectedProjectSessions()
	if label != "alpha" || len(names) != 2 {
		t.Errorf("selectedProjectSessions@alpha = %q %v, want alpha with 2 sessions", label, names)
	}
}

func TestWorkbenchMetas(t *testing.T) {
	st := &Store{path: filepath.Join(t.TempDir(), "sessions.json")}
	_ = st.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_claude-a", Persona: "dev", Project: "p1"})
	_ = st.Add(SessionMeta{Name: "b", TmuxSession: "vibeflow_codex-b", Persona: "qa", Project: "p2"})
	m := Model{store: st}

	got := m.workbenchMetas([]string{"vibeflow_claude-a"})
	if len(got) != 1 || got[0].Persona != "dev" || got[0].Project != "p1" {
		t.Fatalf("workbenchMetas = %+v, want one meta a/dev/p1", got)
	}
}

// TestWorkbenchMetadataSurvivesPruneAndReapply validates the fix mechanism at
// the store level: a session's full metadata is captured, then pruned by Sync
// while the session is (transiently) absent, then re-applied by Add — restoring
// the persona/project that tmux alone cannot reconstruct (issue #3282).
func TestWorkbenchMetadataSurvivesPruneAndReapply(t *testing.T) {
	st := &Store{path: filepath.Join(t.TempDir(), "sessions.json")}
	full := SessionMeta{
		Name: "s", TmuxSession: "vibeflow_claude-s", Provider: "claude",
		Persona: "principal_engineer", Project: "vibeflow-cli", Branch: "main",
	}
	if err := st.Add(full); err != nil {
		t.Fatal(err)
	}
	m := Model{store: st}
	captured := m.workbenchMetas([]string{"vibeflow_claude-s"})

	// Session is absent from tmux (composed into the holder) → a refresh prunes it.
	if err := st.Sync([]string{}); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := st.Get("s"); ok {
		t.Fatal("expected Sync to prune the absent session's metadata")
	}

	// After Restore, the workbench re-applies the captured metadata.
	for _, meta := range captured {
		if err := st.Add(meta); err != nil {
			t.Fatal(err)
		}
	}
	got, ok, _ := st.Get("s")
	if !ok || got.Persona != "principal_engineer" || got.Project != "vibeflow-cli" {
		t.Fatalf("metadata not restored after reapply: %+v (ok=%v)", got, ok)
	}
}

func TestWorkbenchTitles(t *testing.T) {
	// SessionRow.Name is the SHORT name — refreshSessions strips the vibeflow_
	// prefix. The header map MUST be keyed by the FULL name because composeInto
	// looks it up via titles[ensurePrefix(name)]; keying by the short name made
	// every lookup miss so panes fell back to the session id (#3291).
	m := Model{sessions: []SessionRow{
		{Name: "claude-a", Persona: "principal_engineer", Project: "vibeflow-cli", Branch: "main"},
		{Name: "codex-b", Persona: "", Project: "demo", Branch: "feat"},
		{Name: "gemini-c"}, // no metadata → omitted (empty header)
	}}
	titles := m.workbenchTitles()
	if got, want := titles["vibeflow_claude-a"], "principal_engineer · vibeflow-cli · main"; got != want {
		t.Errorf("titles[a] = %q, want %q", got, want)
	}
	if got, want := titles["vibeflow_codex-b"], "demo · feat"; got != want {
		t.Errorf("titles[b] = %q, want %q", got, want)
	}
	if _, ok := titles["vibeflow_gemini-c"]; ok {
		t.Errorf("session with no persona/project/branch must be omitted, got %q", titles["vibeflow_gemini-c"])
	}
	// Regression guard for #3291: must NOT be keyed by the short name.
	if _, ok := titles["claude-a"]; ok {
		t.Errorf("titles must be keyed by the full vibeflow_ name, not the short name")
	}
}

func TestWorkbenchKey_MultiSession_SetsWorkbenchActive(t *testing.T) {
	m := Model{tmux: NewTmuxManager("vftest"), sessions: []SessionRow{
		{Name: "vibeflow_claude-a"},
		{Name: "vibeflow_codex-b"},
	}}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	if cmd == nil {
		t.Fatal("expected a compose command")
	}
	if !nm.(Model).workbenchActive {
		t.Fatal("composing a workbench must set workbenchActive to pause store prune")
	}
}

func TestWorkbenchKey_M_MultiProject_ReturnsCmd(t *testing.T) {
	m := Model{
		tmux:          NewTmuxManager("vftest"),
		repoRootCache: map[string]string{"/work/a": "/work/a", "/work/b": "/work/b"},
		sessions: []SessionRow{
			{Name: "vibeflow_claude-a", WorkingDir: "/work/a"},
			{Name: "vibeflow_codex-b", WorkingDir: "/work/b"},
		},
	}
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("M")})
	if cmd == nil {
		t.Fatalf("M with 2 sessions across projects must return a compose command")
	}
	if nm.(Model).activeView != ViewSessions {
		t.Errorf("workbench must not change the active view")
	}
}

// pressM sends the workbench key to a model and returns the resulting model and
// command WITHOUT executing the command (executing would issue real tmux calls;
// compose/restore behavior is covered by the real-tmux round-trip test).
func pressM(m Model) (Model, tea.Cmd) {
	nm, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("m")})
	return nm.(Model), cmd
}

func TestWorkbenchKey_ZeroSessions_Noop(t *testing.T) {
	m := Model{tmux: NewTmuxManager("vftest")}
	nm, cmd := pressM(m)
	if cmd != nil {
		t.Fatalf("workbench with 0 sessions must be a no-op (nil cmd)")
	}
	if nm.activeView != ViewSessions {
		t.Errorf("workbench must not change the active view, got %v", nm.activeView)
	}
}

func TestWorkbenchKey_SingleSession_ReturnsAttachCmd(t *testing.T) {
	m := Model{tmux: NewTmuxManager("vftest"), sessions: []SessionRow{{Name: "vibeflow_claude-a"}}}
	_, cmd := pressM(m)
	if cmd == nil {
		t.Fatalf("single-session workbench must attach directly (non-nil cmd)")
	}
}

func TestWorkbenchKey_MultiSession_ReturnsComposeCmd(t *testing.T) {
	m := Model{tmux: NewTmuxManager("vftest"), sessions: []SessionRow{
		{Name: "vibeflow_claude-a"},
		{Name: "vibeflow_codex-b"},
	}}
	nm, cmd := pressM(m)
	if cmd == nil {
		t.Fatalf("multi-session workbench must return a compose command")
	}
	if nm.activeView != ViewSessions {
		t.Errorf("workbench must not change the active view, got %v", nm.activeView)
	}
}

func TestUpdate_WorkbenchReadyMsg_Error_SurfacesError(t *testing.T) {
	m := Model{logger: NewLogger(), tmux: NewTmuxManager("vftest")}
	nm, cmd := m.Update(workbenchReadyMsg{err: fmt.Errorf("compose failed")})
	if nm.(Model).err == nil {
		t.Fatalf("expected compose error to be surfaced on the model")
	}
	if cmd == nil {
		t.Fatalf("expected an error-clear tick command after a compose failure")
	}
}

func TestRenderDetailPanel_GatewayDisabled_NoEnvSection(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:     "s1",
		Provider: "claude",
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(120, 40), "")

	if strings.Contains(out, "Gateway") {
		t.Errorf("detail panel must not show Gateway row when gateway is disabled:\n%s", out)
	}
	if strings.Contains(out, "ANTHROPIC_BASE_URL") {
		t.Errorf("detail panel must not show gateway env vars when gateway is disabled:\n%s", out)
	}
}
