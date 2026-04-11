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
	"testing"
)

func TestNewWizardModel_PreselectsDeveloper(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)

	if !wm.selectedPersonas[0] {
		t.Error("selectedPersonas[0] (developer) should be pre-selected")
	}
	for i := 1; i < len(wm.personas); i++ {
		if wm.selectedPersonas[i] {
			t.Errorf("selectedPersonas[%d] (%s) should not be pre-selected", i, wm.personas[i].key)
		}
	}
}

func TestSelectedPersonas_ToggleOnOff(t *testing.T) {
	sp := map[int]bool{0: true}

	// Toggle architect on.
	sp[1] = !sp[1]
	if !sp[1] {
		t.Error("after toggle on, selectedPersonas[1] should be true")
	}

	// Toggle architect off.
	sp[1] = !sp[1]
	if sp[1] {
		t.Error("after toggle off, selectedPersonas[1] should be false")
	}

	// Toggle developer off.
	sp[0] = !sp[0]
	if sp[0] {
		t.Error("after toggle off, selectedPersonas[0] should be false")
	}
}

func TestSelectedPersonas_CountSelected(t *testing.T) {
	sp := map[int]bool{0: true, 2: true, 4: true}
	count := 0
	for _, on := range sp {
		if on {
			count++
		}
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestSelectedPersonas_EmptyBlocksAdvance(t *testing.T) {
	sp := map[int]bool{}
	count := 0
	for _, on := range sp {
		if on {
			count++
		}
	}
	if count != 0 {
		t.Errorf("empty map should have count 0, got %d", count)
	}
}

func TestWizardResult_PersonasField(t *testing.T) {
	personas := defaultPersonas()
	selectedPersonas := map[int]bool{0: true, 2: true, 5: true}

	var result []string
	for i := 0; i < len(personas); i++ {
		if selectedPersonas[i] {
			result = append(result, personas[i].key)
		}
	}

	if len(result) != 3 {
		t.Fatalf("len(Personas) = %d, want 3", len(result))
	}
	expected := []string{"developer", "architect", "security_lead"}
	for i, want := range expected {
		if result[i] != want {
			t.Errorf("Personas[%d] = %q, want %q", i, result[i], want)
		}
	}
}

func TestWizardResult_PersonaBackwardCompat(t *testing.T) {
	// First selected persona should become the primary Persona field.
	personas := defaultPersonas()
	selectedPersonas := map[int]bool{4: true, 6: true} // qa_lead and product_manager

	selectedPersona := -1
	for i := 0; i < len(personas); i++ {
		if selectedPersonas[i] {
			if selectedPersona < 0 {
				selectedPersona = i
			}
		}
	}

	if selectedPersona != 4 {
		t.Errorf("selectedPersona = %d, want 4 (qa_lead)", selectedPersona)
	}
	if personas[selectedPersona].key != "qa_lead" {
		t.Errorf("primary persona = %q, want qa_lead", personas[selectedPersona].key)
	}
}

func TestDefaultPersonas_EightEntries(t *testing.T) {
	personas := defaultPersonas()
	if len(personas) != 9 {
		t.Errorf("len(defaultPersonas()) = %d, want 9", len(personas))
	}
}

func TestDefaultPersonas_UniqueKeys(t *testing.T) {
	personas := defaultPersonas()
	seen := make(map[string]bool)
	for _, p := range personas {
		if seen[p.key] {
			t.Errorf("duplicate persona key: %q", p.key)
		}
		seen[p.key] = true
	}
}

func TestDefaultPersonas_DeveloperFirst(t *testing.T) {
	personas := defaultPersonas()
	if personas[0].key != "developer" {
		t.Errorf("first persona key = %q, want developer", personas[0].key)
	}
}

func TestPersonaMutualExclusion_CodeAgents(t *testing.T) {
	// Selecting architect should deselect developer.
	selected := map[int]bool{0: true} // developer pre-selected
	personas := defaultPersonas()

	// Simulate selecting architect (index 2).
	cursor := 2
	key := personas[cursor].key
	if isCodeAgentPersona(key) {
		for i, p := range personas {
			if isCodeAgentPersona(p.key) {
				selected[i] = false
			}
		}
		selected[cursor] = true
	}

	if selected[0] {
		t.Error("developer should be deselected after selecting architect")
	}
	if !selected[2] {
		t.Error("architect should be selected")
	}
}

func TestPersonaMutualExclusion_ReviewUnaffected(t *testing.T) {
	// Selecting a code agent should not affect review personas.
	selected := map[int]bool{0: true, 4: true, 5: true} // developer + qa_lead + security_lead
	personas := defaultPersonas()

	// Simulate selecting principal_engineer (index 1).
	cursor := 1
	key := personas[cursor].key
	if isCodeAgentPersona(key) {
		for i, p := range personas {
			if isCodeAgentPersona(p.key) {
				selected[i] = false
			}
		}
		selected[cursor] = true
	}

	if selected[0] {
		t.Error("developer should be deselected")
	}
	if !selected[1] {
		t.Error("principal_engineer should be selected")
	}
	if !selected[4] {
		t.Error("qa_lead should remain selected")
	}
	if !selected[5] {
		t.Error("security_lead should remain selected")
	}
}

func TestPersonaMutualExclusion_DeselectCodeAgent(t *testing.T) {
	// Deselecting the only code agent is valid (review-only team).
	selected := map[int]bool{0: true, 4: true}
	personas := defaultPersonas()

	// Simulate deselecting developer (already selected).
	cursor := 0
	key := personas[cursor].key
	if isCodeAgentPersona(key) && selected[cursor] {
		selected[cursor] = false
	}

	if selected[0] {
		t.Error("developer should be deselected")
	}
	if !selected[4] {
		t.Error("qa_lead should remain selected")
	}
}

func TestCursorToCurrentBranch_Found(t *testing.T) {
	w := WizardModel{
		branches:         []string{"[+] Create new branch", "main", "develop", "feature-x"},
		filteredBranches: []int{0, 1, 2, 3},
		currentBranch:    "develop",
	}
	w.cursorToCurrentBranch()
	if w.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (develop)", w.cursor)
	}
}

func TestCursorToCurrentBranch_NotFound(t *testing.T) {
	w := WizardModel{
		branches:         []string{"[+] Create new branch", "main", "develop"},
		filteredBranches: []int{0, 1, 2},
		currentBranch:    "", // detached HEAD
	}
	w.cursor = 0
	w.cursorToCurrentBranch()
	if w.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (unchanged for empty currentBranch)", w.cursor)
	}
}

func TestCursorToCurrentBranch_SkipsCreateNew(t *testing.T) {
	// Index 0 ("[+] Create new branch") should never match even if currentBranch is set.
	w := WizardModel{
		branches:         []string{"[+] Create new branch", "main"},
		filteredBranches: []int{0, 1},
		currentBranch:    "[+] Create new branch",
	}
	w.cursorToCurrentBranch()
	if w.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (should not match index 0)", w.cursor)
	}
}

func TestQuickSwitchWizard_StartsAtBranch(t *testing.T) {
	meta := SessionMeta{
		Provider:    "claude",
		Persona:     "developer",
		SessionType: "vibeflow",
		WorkingDir:  ".",
	}
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	w := NewQuickSwitchWizard(meta, reg, ".", nil, cfg)

	if w.step != StepBranch {
		t.Errorf("step = %d, want StepBranch (%d)", w.step, StepBranch)
	}
	if !w.quickSwitch {
		t.Error("quickSwitch should be true")
	}
	if w.switchSource == nil {
		t.Error("switchSource should not be nil")
	}
}

func TestQuickSwitchWizard_BackFromBranchCancels(t *testing.T) {
	meta := SessionMeta{
		Provider:    "claude",
		Persona:     "developer",
		SessionType: "vibeflow",
		WorkingDir:  ".",
	}
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	w := NewQuickSwitchWizard(meta, reg, ".", nil, cfg)

	// Simulate pressing Esc.
	w, _ = w.goBack()
	if !w.cancelled {
		t.Error("goBack from StepBranch in quickSwitch should cancel")
	}
}

func TestBuildQuickSwitchResult_PreservesFields(t *testing.T) {
	meta := SessionMeta{
		Provider:          "claude",
		Persona:           "architect",
		Project:           "my-project",
		SessionType:       "vibeflow",
		SkipPermissions:   true,
		LLMGatewayEnabled: true,
		WorkingDir:        ".",
	}
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	w := NewQuickSwitchWizard(meta, reg, ".", nil, cfg)

	// Simulate selecting branch "main" (index 1) and "Current directory" worktree choice.
	if len(w.branches) > 1 {
		w.selectedBranch = 1
	}
	// worktreeOpts last entry is "Current directory".
	w.selectedWorktree = len(w.worktreeOpts) - 1

	w, _ = w.buildQuickSwitchResult()

	if !w.done {
		t.Fatal("wizard should be done after buildQuickSwitchResult")
	}
	r := w.result
	if r.Persona != "architect" {
		t.Errorf("Persona = %q, want architect", r.Persona)
	}
	if r.ProjectName != "my-project" {
		t.Errorf("ProjectName = %q, want my-project", r.ProjectName)
	}
	if r.SessionType != "vibeflow" {
		t.Errorf("SessionType = %q, want vibeflow", r.SessionType)
	}
	if !r.SkipPermissions {
		t.Error("SkipPermissions should be preserved as true")
	}
	if !r.LLMGatewayEnabled {
		t.Error("LLMGatewayEnabled should be preserved as true")
	}
	if r.WorktreeChoice != WorktreeCurrent {
		t.Errorf("WorktreeChoice = %d, want WorktreeCurrent (%d)", r.WorktreeChoice, WorktreeCurrent)
	}
}

func TestIsCodeAgentPersona(t *testing.T) {
	tests := []struct {
		key  string
		want bool
	}{
		{"developer", true},
		{"principal_engineer", true},
		{"architect", true},
		{"qa_lead", false},
		{"security_lead", false},
		{"product_manager", false},
		{"customer", false},
		{"", false},
	}
	for _, tt := range tests {
		if got := isCodeAgentPersona(tt.key); got != tt.want {
			t.Errorf("isCodeAgentPersona(%q) = %v, want %v", tt.key, got, tt.want)
		}
	}
}
