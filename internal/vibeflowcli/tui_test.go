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

func TestLiveSessionNames_OrderPreserved(t *testing.T) {
	m := Model{sessions: []SessionRow{
		{Name: "vibeflow_claude-a"},
		{Name: "vibeflow_codex-b"},
		{Name: "vibeflow_gemini-c"},
	}}
	got := m.liveSessionNames()
	want := []string{"vibeflow_claude-a", "vibeflow_codex-b", "vibeflow_gemini-c"}
	if len(got) != len(want) {
		t.Fatalf("liveSessionNames len = %d, want %d (%v)", len(got), len(want), got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Errorf("liveSessionNames[%d] = %q, want %q", i, got[i], want[i])
		}
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
