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

// teamModeFixture builds a WizardModel pre-configured for StepProvider in
// team mode with two installed providers (claude + qwen) and one uninstalled
// (cursor). Three personas selected: developer, qa_lead, security_lead.
func teamModeFixture(t *testing.T) WizardModel {
	t.Helper()
	cfg := &Config{
		Providers: map[string]Provider{
			"claude": {Name: "Claude", Binary: "sh"},
			"qwen":   {Name: "Qwen", Binary: "sh"},
			"cursor": {Name: "Cursor", Binary: "this-binary-does-not-exist-xyz-123"},
		},
	}
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 1 // vibeflow
	// Toggle on three non-conflicting personas (developer index 0 already on).
	for i, p := range wm.personas {
		switch p.key {
		case "qa_lead", "security_lead":
			wm.selectedPersonas[i] = true
		case "developer":
			wm.selectedPersonas[i] = true
		}
	}
	wm.step = StepProvider
	wm.cursor = 0 // team-default row
	// Find provider indices in the wizard's sorted list.
	for i, pe := range wm.providers {
		if pe.key == "claude" {
			wm.selectedProvider = i
		}
	}
	return wm
}

func providerIdxByKey(t *testing.T, w WizardModel, key string) int {
	t.Helper()
	for i, pe := range w.providers {
		if pe.key == key {
			return i
		}
	}
	t.Fatalf("provider %q not in wizard", key)
	return -1
}

func TestTeamModeProvider_DetectsMultiSelect(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 1

	t.Run("single persona is solo", func(t *testing.T) {
		w := wm
		w.selectedPersonas = map[int]bool{0: true}
		if w.teamModeProvider() {
			t.Error("single persona should be solo (teamModeProvider=false)")
		}
	})

	t.Run("two personas is team", func(t *testing.T) {
		w := wm
		w.selectedPersonas = map[int]bool{0: true, 3: true}
		if !w.teamModeProvider() {
			t.Error("two personas should be team (teamModeProvider=true)")
		}
	})

	t.Run("vanilla session never team mode", func(t *testing.T) {
		w := wm
		w.selectedSessionType = 0
		w.selectedPersonas = map[int]bool{0: true, 3: true, 5: true}
		if w.teamModeProvider() {
			t.Error("vanilla session should never be team mode")
		}
	})
}

func TestSelectedPersonaIndices_DisplayOrder(t *testing.T) {
	wm := teamModeFixture(t)
	got := wm.selectedPersonaIndices()
	// Check ascending order matches w.personas indexing.
	for i := 1; i < len(got); i++ {
		if got[i] <= got[i-1] {
			t.Errorf("selectedPersonaIndices not in ascending order: %v", got)
		}
	}
	// All indices should be selected.
	for _, idx := range got {
		if !wm.selectedPersonas[idx] {
			t.Errorf("index %d returned but not selected", idx)
		}
	}
}

func TestTeamProviderRowCount_OneTeamDefaultPlusPersonas(t *testing.T) {
	wm := teamModeFixture(t)
	count := len(wm.selectedPersonaIndices())
	if got := wm.teamProviderRowCount(); got != count+1 {
		t.Errorf("teamProviderRowCount = %d, want %d (1 + %d personas)", got, count+1, count)
	}
}

func TestListLen_StepProviderTeamMode(t *testing.T) {
	wm := teamModeFixture(t)
	want := wm.teamProviderRowCount()
	if got := wm.listLen(); got != want {
		t.Errorf("listLen() in team mode = %d, want %d", got, want)
	}
}

func TestListLen_StepProviderSoloMode(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 1
	wm.selectedPersonas = map[int]bool{0: true} // single persona
	wm.step = StepProvider
	want := len(wm.providers)
	if got := wm.listLen(); got != want {
		t.Errorf("listLen() in solo mode = %d, want %d (provider count)", got, want)
	}
}

func TestNextAvailableProviderIdx_SkipsUninstalled(t *testing.T) {
	wm := teamModeFixture(t)
	cursorIdx := providerIdxByKey(t, wm, "cursor")
	claudeIdx := providerIdxByKey(t, wm, "claude")
	qwenIdx := providerIdxByKey(t, wm, "qwen")

	// Forward from claude should skip cursor (uninstalled) and land on qwen.
	got := wm.nextAvailableProviderIdx(claudeIdx, +1)
	if got == cursorIdx {
		t.Errorf("forward cycle from claude landed on cursor (uninstalled, idx=%d)", cursorIdx)
	}
	if got != qwenIdx {
		t.Errorf("forward cycle from claude = %d, want qwen (%d)", got, qwenIdx)
	}

	// Backward from claude should also skip cursor and land on qwen (the only
	// other installed provider — wraps around).
	if got := wm.nextAvailableProviderIdx(claudeIdx, -1); got != qwenIdx {
		t.Errorf("backward cycle from claude = %d, want qwen (%d)", got, qwenIdx)
	}
}

func TestNextAvailableProviderIdx_AllUninstalled(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"a": {Name: "A", Binary: "this-binary-does-not-exist-xyz-123"},
		"b": {Name: "B", Binary: "this-binary-does-not-exist-xyz-456"},
	}}
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	if got := wm.nextAvailableProviderIdx(0, +1); got != 0 {
		t.Errorf("all-uninstalled cycle should return same idx, got %d", got)
	}
}

func TestResolvedProviderForPersona_InheritsTeamDefault(t *testing.T) {
	wm := teamModeFixture(t)
	for _, personaIdx := range wm.selectedPersonaIndices() {
		// All personas init with -1 (inherit), so should resolve to selectedProvider.
		if got := wm.resolvedProviderForPersona(personaIdx); got != wm.selectedProvider {
			t.Errorf("persona %d resolved to %d, want team default %d",
				personaIdx, got, wm.selectedProvider)
		}
	}
}

func TestResolvedProviderForPersona_ExplicitOverride(t *testing.T) {
	wm := teamModeFixture(t)
	qwenIdx := providerIdxByKey(t, wm, "qwen")
	// Override the first selected persona to qwen.
	first := wm.selectedPersonaIndices()[0]
	wm.personaProviderIdx[first] = qwenIdx

	if got := wm.resolvedProviderForPersona(first); got != qwenIdx {
		t.Errorf("override persona %d = %d, want qwen %d", first, got, qwenIdx)
	}
	// Other personas still inherit.
	for _, idx := range wm.selectedPersonaIndices()[1:] {
		if got := wm.resolvedProviderForPersona(idx); got != wm.selectedProvider {
			t.Errorf("non-overridden persona %d = %d, want team default %d",
				idx, got, wm.selectedProvider)
		}
	}
}

func TestWizardResult_PersonaProvidersFromConfirm(t *testing.T) {
	wm := teamModeFixture(t)
	// Drive to the state right before StepConfirm advance.
	qwenIdx := providerIdxByKey(t, wm, "qwen")
	personas := wm.selectedPersonaIndices()
	wm.personaProviderIdx[personas[0]] = qwenIdx           // explicit override
	wm.personaProviderIdx[personas[1]] = wm.selectedProvider // override matching default — should be elided
	// personas[2] left at -1 (inherit)

	wm.selectedPersona = personas[0]
	wm.step = StepConfirm
	wm.cursor = 0
	wm.selectedBranch = 1 // skip "create new"
	if len(wm.branches) < 2 {
		wm.branches = []string{"[+] Create new branch", "main"}
	}
	wm.selectedWorktree = 0 // "New worktree" → set worktree name first
	wm.worktreeName = "test-wt"
	wm.selectedPermission = 0

	// Drive advance from StepConfirm.
	w2, _ := wm.advance()
	r := w2.Result()

	if r.PersonaProviders == nil {
		t.Fatal("PersonaProviders should be set when one persona has a real override")
	}
	first := wm.personas[personas[0]].key
	if got := r.PersonaProviders[first]; got != "qwen" {
		t.Errorf("PersonaProviders[%q] = %q, want qwen", first, got)
	}
	// personas[1] should NOT be in the map (override matched default).
	second := wm.personas[personas[1]].key
	if _, ok := r.PersonaProviders[second]; ok {
		t.Errorf("PersonaProviders[%q] should not be set (override matched default)", second)
	}
	// personas[2] should NOT be in the map (inherit).
	third := wm.personas[personas[2]].key
	if _, ok := r.PersonaProviders[third]; ok {
		t.Errorf("PersonaProviders[%q] should not be set (inherits)", third)
	}
}

func TestWizardResult_PersonaProvidersNilWhenAllInherit(t *testing.T) {
	wm := teamModeFixture(t)
	personas := wm.selectedPersonaIndices()
	// All personaProviderIdx entries left at -1 (inherit).

	wm.selectedPersona = personas[0]
	wm.step = StepConfirm
	wm.cursor = 0
	wm.selectedBranch = 1
	if len(wm.branches) < 2 {
		wm.branches = []string{"[+] Create new branch", "main"}
	}
	wm.selectedWorktree = 0
	wm.worktreeName = "test-wt"
	wm.selectedPermission = 0

	w2, _ := wm.advance()
	r := w2.Result()

	if r.PersonaProviders != nil {
		t.Errorf("PersonaProviders should be nil when no overrides taken, got %v", r.PersonaProviders)
	}
	// And ProviderKey (team default) should still be set.
	if r.ProviderKey == "" {
		t.Error("team default ProviderKey should be set")
	}
}

func TestWizardResult_SoloFlowOmitsPersonaProviders(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{
		"claude": {Name: "Claude", Binary: "sh"},
	}}
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 1
	wm.selectedPersonas = map[int]bool{0: true} // solo: single persona
	wm.selectedPersona = 0

	wm.step = StepConfirm
	wm.cursor = 0
	wm.selectedBranch = 1
	if len(wm.branches) < 2 {
		wm.branches = []string{"[+] Create new branch", "main"}
	}
	wm.selectedWorktree = 0
	wm.worktreeName = "test-wt"
	wm.selectedPermission = 0

	w2, _ := wm.advance()
	r := w2.Result()

	if r.PersonaProviders != nil {
		t.Errorf("solo flow should omit PersonaProviders, got %v", r.PersonaProviders)
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
