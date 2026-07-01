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
	"strings"
	"testing"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
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

func TestRenderTerminalPane_SelectedSessionShowsCapturedOutput(t *testing.T) {
	m := Model{
		captureName:   "s1",
		captureOutput: "line one\nline two",
	}

	out := ansiRe.ReplaceAllString(m.renderTerminalPane(SessionRow{Name: "s1"}, 80, 20), "")

	if !strings.Contains(out, "Output") {
		t.Errorf("terminal pane missing output header:\n%s", out)
	}
	if !strings.Contains(out, "line one") || !strings.Contains(out, "line two") {
		t.Errorf("terminal pane missing captured output:\n%s", out)
	}
}

func TestRenderTerminalPane_IgnoresStaleCaptureForOtherSession(t *testing.T) {
	m := Model{
		captureName:   "s2",
		captureOutput: "stale output",
	}

	out := ansiRe.ReplaceAllString(m.renderTerminalPane(SessionRow{Name: "s1"}, 80, 20), "")

	if strings.Contains(out, "stale output") {
		t.Errorf("terminal pane rendered capture from another session:\n%s", out)
	}
	if !strings.Contains(out, "(no output yet)") {
		t.Errorf("terminal pane should show empty state for unmatched capture:\n%s", out)
	}
}

func TestUpdate_EnterAttachesSelectedSession(t *testing.T) {
	m := Model{
		config:   &Config{},
		tmux:     &TmuxManager{socketName: "test"},
		sessions: []SessionRow{{Name: "s1", Status: "running"}},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if cmd == nil {
		t.Fatal("enter should attach to the selected tmux session")
	}
	m = updated.(Model)
	if m.terminalFocus {
		t.Fatal("enter should not toggle terminal focus")
	}
}

func TestUpdate_MatrixToggle(t *testing.T) {
	m := Model{
		config:   &Config{},
		sessions: []SessionRow{{Name: "s1", Status: "running"}},
	}

	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if !m.matrixMode {
		t.Fatal("s should enable matrix mode")
	}
	if cmd == nil {
		t.Fatal("matrix toggle should request a capture refresh")
	}

	updated, _ = m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'s'}})
	m = updated.(Model)
	if m.matrixMode {
		t.Fatal("second s should disable matrix mode")
	}
}

// TestUpdate_InspectorKeyIsNoOp guards that the removed inspector toggle ('i')
// no longer mutates state or crashes now that the drawer is gone.
func TestUpdate_InspectorKeyIsNoOp(t *testing.T) {
	m := Model{
		config:   &Config{},
		sessions: []SessionRow{{Name: "s1", Status: "running"}},
	}
	updated, cmd := m.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'i'}})
	if cmd != nil {
		t.Fatal("removed inspector key should not run a command")
	}
	m = updated.(Model)
	if m.matrixMode {
		t.Fatal("'i' must not enable matrix mode")
	}
}

func TestRenderWorkbenchTerminal_FocusModeShowsSelectedOutput(t *testing.T) {
	m := Model{
		sessions:      []SessionRow{{Name: "s1", Status: "running", Branch: "main"}},
		captureName:   "s1",
		captureOutput: "line one\nline two",
	}

	out := ansiRe.ReplaceAllString(m.renderWorkbenchTerminal(100, 20), "")

	if !strings.Contains(out, "Terminal") {
		t.Errorf("workbench missing terminal header:\n%s", out)
	}
	if !strings.Contains(out, "s1") || !strings.Contains(out, "main") {
		t.Errorf("workbench missing selected session header:\n%s", out)
	}
	if !strings.Contains(out, "line two") {
		t.Errorf("workbench missing captured output:\n%s", out)
	}
}

func TestRenderMatrixPane_ShowsMultipleSessionOutputs(t *testing.T) {
	m := Model{
		matrixMode: true,
		sessions: []SessionRow{
			{Name: "s1", Status: "running", Branch: "main"},
			{Name: "s2", Status: "attached", Branch: "feature"},
		},
		captureOutputs: map[string]string{
			"s1": "one",
			"s2": "two",
		},
	}

	out := ansiRe.ReplaceAllString(m.renderWorkbenchTerminal(100, 20), "")

	if !strings.Contains(out, "Matrix") {
		t.Errorf("matrix mode missing header:\n%s", out)
	}
	if !strings.Contains(out, "s1") || !strings.Contains(out, "one") {
		t.Errorf("matrix mode missing first session output:\n%s", out)
	}
	if !strings.Contains(out, "s2") || !strings.Contains(out, "two") {
		t.Errorf("matrix mode missing second session output:\n%s", out)
	}
	// The 2x2 grid header reports the visible window over the total.
	if !strings.Contains(out, "of 2") {
		t.Errorf("matrix header should report the window position:\n%s", out)
	}
	// Each pane is drawn as its own bordered rectangle (rounded border chars).
	if !strings.Contains(out, "│") || !strings.Contains(out, "╮") {
		t.Errorf("matrix cells should render as bordered rectangles:\n%s", out)
	}
}

func sixSessions() []SessionRow {
	return []SessionRow{
		{Name: "s0", Status: "running"},
		{Name: "s1", Status: "running"},
		{Name: "s2", Status: "running"},
		{Name: "s3", Status: "running"},
		{Name: "s4", Status: "running"},
		{Name: "s5", Status: "running"},
	}
}

func TestRenderMatrixPane_ScrollWindowShowsLaterSessions(t *testing.T) {
	m := Model{matrixMode: true, width: 120, height: 40, sessions: sixSessions()}

	// Default window shows the first four sessions.
	out := ansiRe.ReplaceAllString(m.renderWorkbenchTerminal(80, 28), "")
	if !strings.Contains(out, "s0") || !strings.Contains(out, "s3") {
		t.Errorf("default matrix window should show s0..s3:\n%s", out)
	}
	if strings.Contains(out, "s5") {
		t.Errorf("default matrix window should not show s5:\n%s", out)
	}

	// Scrolling the window forward reveals the later sessions.
	m.matrixOffset = 2
	out = ansiRe.ReplaceAllString(m.renderWorkbenchTerminal(80, 28), "")
	if !strings.Contains(out, "s5") || !strings.Contains(out, "s2") {
		t.Errorf("scrolled matrix window should show s2..s5:\n%s", out)
	}
}

func TestListLineToCursor_FlatMixedSubtitles(t *testing.T) {
	m := Model{sessions: []SessionRow{
		{Name: "s0", Branch: "main"}, // has subtitle -> 2 lines
		{Name: "s1"},                 // no subtitle -> 1 line
		{Name: "s2", Project: "p"},   // has subtitle -> 2 lines
	}}
	got := m.listLineToCursor()
	want := []int{0, 0, 1, 2, 2}
	if len(got) != len(want) {
		t.Fatalf("line map length: got %v want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("line map[%d]: got %d want %d (full %v)", i, got[i], want[i], got)
		}
	}
}

func TestHitTestMatrix_MapsCellsToSessions(t *testing.T) {
	m := Model{matrixMode: true, width: 120, height: 40, sessions: sixSessions()}

	width, height := m.normalizedDims()
	leftWidth, workbench := m.columnWidths(width)
	tcw := workbench - 4
	contentH := m.workbenchContentDims(height)
	lcw, _, cellH := matrixGridDims(tcw, contentH)
	x0 := leftWidth + 2
	gridTop := m.headerOffset() + 2

	cases := []struct {
		name    string
		x, y    int
		wantIdx int
		wantOK  bool
	}{
		{"top-left", x0, gridTop, 0, true},
		{"top-left-inner", x0 + lcw - 1, gridTop + cellH - 1, 0, true},
		{"top-right", x0 + lcw + 1, gridTop, 1, true},
		{"bottom-left", x0, gridTop + cellH, 2, true},
		{"bottom-right", x0 + lcw + 1, gridTop + cellH, 3, true},
		{"gutter-column", x0 + lcw, gridTop, 0, false},
		{"below-grid", x0, gridTop + 2*cellH, 0, false},
		{"left-of-grid", x0 - 1, gridTop, 0, false},
	}
	for _, tc := range cases {
		idx, ok := m.hitTestMatrix(tc.x, tc.y)
		if ok != tc.wantOK || (ok && idx != tc.wantIdx) {
			t.Errorf("%s: hitTestMatrix(%d,%d) = (%d,%v), want (%d,%v)",
				tc.name, tc.x, tc.y, idx, ok, tc.wantIdx, tc.wantOK)
		}
	}

	// A scrolled window shifts the mapping by the offset.
	m.matrixOffset = 2
	if idx, ok := m.hitTestMatrix(x0, gridTop); !ok || idx != 2 {
		t.Errorf("scrolled top-left: got (%d,%v), want (2,true)", idx, ok)
	}
}

func TestUpdate_MouseClickFocusesListRow(t *testing.T) {
	m := Model{
		width:    120,
		height:   40,
		sessions: []SessionRow{{Name: "s0", Branch: "a"}, {Name: "s1", Branch: "b"}, {Name: "s2", Branch: "c"}},
	}
	// Each session renders name+subtitle (2 lines); s1's name line is the third.
	y := m.headerOffset() + 2 + 2
	updated, _ := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: 3, Y: y})
	m = updated.(Model)
	if m.cursor != 1 {
		t.Fatalf("click on s1 row should focus cursor 1, got %d", m.cursor)
	}
}

func TestUpdate_MouseClickFocusesMatrixPane(t *testing.T) {
	m := Model{matrixMode: true, width: 120, height: 40, sessions: sixSessions()}

	width, height := m.normalizedDims()
	leftWidth, workbench := m.columnWidths(width)
	tcw := workbench - 4
	contentH := m.workbenchContentDims(height)
	lcw, _, cellH := matrixGridDims(tcw, contentH)
	// Bottom-right cell → session index 3.
	x := leftWidth + 2 + lcw + 1
	y := m.headerOffset() + 2 + cellH

	updated, cmd := m.Update(tea.MouseMsg{Action: tea.MouseActionPress, Button: tea.MouseButtonLeft, X: x, Y: y})
	m = updated.(Model)
	if m.cursor != 3 {
		t.Fatalf("click on bottom-right matrix cell should focus session 3, got %d", m.cursor)
	}
	if cmd == nil {
		t.Fatal("focusing a matrix pane should request a capture refresh")
	}
}

// TestRenderMatrixPane_NoLineOverflow guards that the bordered 2x2 grid never
// renders a line wider than its content area (which would break the workbench
// column layout).
func TestRenderMatrixPane_NoLineOverflow(t *testing.T) {
	m := Model{matrixMode: true, width: 120, height: 40, sessions: sixSessions()}
	for _, d := range []struct{ w, h int }{{80, 28}, {60, 24}} {
		out := m.renderWorkbenchTerminal(d.w, d.h)
		for i, line := range strings.Split(out, "\n") {
			if w := lipgloss.Width(line); w > d.w {
				t.Errorf("matrix %dx%d line %d width %d exceeds %d: %q", d.w, d.h, i, w, d.w, line)
			}
		}
	}
}

func TestUpdate_MouseWheelScrollsMatrix(t *testing.T) {
	m := Model{matrixMode: true, width: 120, height: 40, sessions: sixSessions()}

	updated, _ := m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
	m = updated.(Model)
	if m.matrixOffset != 1 {
		t.Fatalf("wheel down should scroll matrix to offset 1, got %d", m.matrixOffset)
	}

	// Offset clamps at total-4 (6-4=2).
	for i := 0; i < 5; i++ {
		updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelDown})
		m = updated.(Model)
	}
	if m.matrixOffset != 2 {
		t.Fatalf("wheel down should clamp matrix offset at 2, got %d", m.matrixOffset)
	}

	updated, _ = m.Update(tea.MouseMsg{Button: tea.MouseButtonWheelUp})
	m = updated.(Model)
	if m.matrixOffset != 1 {
		t.Fatalf("wheel up should scroll matrix back to offset 1, got %d", m.matrixOffset)
	}
}
