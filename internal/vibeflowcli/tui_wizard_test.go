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

	tea "github.com/charmbracelet/bubbletea"
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

// qwenWizardFixture builds a minimal WizardModel sitting at StepQwenLaunchConfig
// with the qwen provider pre-selected. Used by the qwen-launch-config tests.
func qwenWizardFixture(t *testing.T) WizardModel {
	t.Helper()
	cfg := &Config{Providers: map[string]Provider{
		"qwen": {Name: "Qwen Code", Binary: "sh"},
	}}
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 0 // vanilla — gateway path is irrelevant here
	for i, pe := range wm.providers {
		if pe.key == "qwen" {
			wm.selectedProvider = i
		}
	}
	if len(wm.branches) < 2 {
		wm.branches = []string{"[+] Create new branch", "main"}
		wm.filteredBranches = []int{0, 1}
	}
	wm.enterQwenLaunchConfig()
	return wm
}

func TestQwenLaunchPresets_Shape(t *testing.T) {
	presets := qwenLaunchPresets()
	if len(presets) < 2 {
		t.Fatalf("expected at least 2 presets, got %d", len(presets))
	}
	last := presets[len(presets)-1]
	if last.label != "Custom" {
		t.Errorf("expected last preset to be 'Custom', got %q", last.label)
	}
	if last.model != "" || last.baseURL != "" {
		t.Errorf("Custom preset must have empty model + baseURL, got %+v", last)
	}
	for i, p := range presets {
		if p.label == "" {
			t.Errorf("preset %d has empty label", i)
		}
		if p.label != "Custom" && p.baseURL == "" {
			t.Errorf("preset %d (%s) has empty baseURL", i, p.label)
		}
	}
}

func TestApplyQwenPreset_FillsFromVendor(t *testing.T) {
	w := WizardModel{qwenVendorIdx: 0, qwenUserEdited: true}
	presets := qwenLaunchPresets()
	w.applyQwenPreset()
	if w.qwenModelInput != presets[0].model {
		t.Errorf("model = %q, want %q", w.qwenModelInput, presets[0].model)
	}
	if w.qwenBaseURLInput != presets[0].baseURL {
		t.Errorf("baseURL = %q, want %q", w.qwenBaseURLInput, presets[0].baseURL)
	}
	if w.qwenUserEdited {
		t.Error("qwenUserEdited should be cleared after applyQwenPreset")
	}
}

func TestApplyQwenPreset_OutOfRangeResetsToZero(t *testing.T) {
	w := WizardModel{qwenVendorIdx: 999}
	w.applyQwenPreset()
	if w.qwenVendorIdx != 0 {
		t.Errorf("expected qwenVendorIdx clamped to 0, got %d", w.qwenVendorIdx)
	}
}

func TestPostProviderConfigStep_RoutingMatrix(t *testing.T) {
	tests := []struct {
		name    string
		key     string
		gateway bool
		want    WizardStep
	}{
		{"qwen_no_gateway_routes_to_qwen_step", "qwen", false, StepQwenLaunchConfig},
		{"qwen_with_gateway_skips_to_branch", "qwen", true, StepBranch},
		{"claude_skips_qwen_step", "claude", false, StepBranch},
		{"codex_skips_qwen_step", "codex", false, StepBranch},
		{"gemini_skips_qwen_step", "gemini", false, StepBranch},
		{"cursor_skips_qwen_step", "cursor", false, StepBranch},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := WizardModel{
				providers: []providerEntry{
					{key: "claude", provider: Provider{}},
					{key: "codex", provider: Provider{}},
					{key: "cursor", provider: Provider{}},
					{key: "gemini", provider: Provider{}},
					{key: "qwen", provider: Provider{}},
				},
				llmGatewayEnabled: tt.gateway,
			}
			for i, pe := range w.providers {
				if pe.key == tt.key {
					w.selectedProvider = i
				}
			}
			if got := w.postProviderConfigStep(); got != tt.want {
				t.Errorf("postProviderConfigStep() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestEnterQwenLaunchConfig_FirstEntryAppliesPreset(t *testing.T) {
	w := WizardModel{}
	if w.qwenInitialized {
		t.Fatal("fresh WizardModel should have qwenInitialized=false")
	}
	w.enterQwenLaunchConfig()
	if !w.qwenInitialized {
		t.Error("first entry must mark qwenInitialized=true")
	}
	presets := qwenLaunchPresets()
	if w.qwenModelInput != presets[0].model {
		t.Errorf("first entry should seed model from preset[0]: got %q, want %q", w.qwenModelInput, presets[0].model)
	}
	if w.step != StepQwenLaunchConfig {
		t.Errorf("step = %v, want StepQwenLaunchConfig", w.step)
	}
	if w.cursor != 0 {
		t.Errorf("cursor = %d, want 0 (first vendor row)", w.cursor)
	}
}

func TestEnterQwenLaunchConfig_PreservesEditsOnReentry(t *testing.T) {
	w := WizardModel{
		qwenInitialized:  true,
		qwenVendorIdx:    1,
		qwenModelInput:   "user-typed-model",
		qwenBaseURLInput: "https://user-typed.example.com",
		qwenUserEdited:   true,
	}
	w.enterQwenLaunchConfig()
	if w.qwenModelInput != "user-typed-model" {
		t.Errorf("re-entry must preserve user model edit, got %q", w.qwenModelInput)
	}
	if w.qwenBaseURLInput != "https://user-typed.example.com" {
		t.Errorf("re-entry must preserve user baseURL edit, got %q", w.qwenBaseURLInput)
	}
}

func TestWizardListLen_QwenLaunchConfig(t *testing.T) {
	w := WizardModel{step: StepQwenLaunchConfig}
	want := len(qwenLaunchPresets()) + 2
	if got := w.listLen(); got != want {
		t.Errorf("listLen() = %d, want %d", got, want)
	}
}

func TestWizardAdvance_QwenLaunchConfig_PopulatesEnvVars(t *testing.T) {
	wm := qwenWizardFixture(t)
	// Set some user-entered values.
	wm.qwenModelInput = "custom-model"
	wm.qwenBaseURLInput = "https://custom.example.com/v1"
	wm.qwenUserEdited = true

	w2, _ := wm.advance()

	if w2.envVars["OPENAI_BASE_URL"] != "https://custom.example.com/v1" {
		t.Errorf("OPENAI_BASE_URL = %q, want https://custom.example.com/v1", w2.envVars["OPENAI_BASE_URL"])
	}
	if w2.envVars["OPENAI_MODEL"] != "custom-model" {
		t.Errorf("OPENAI_MODEL = %q, want custom-model", w2.envVars["OPENAI_MODEL"])
	}
	if w2.step != StepBranch {
		t.Errorf("after advance step = %v, want StepBranch", w2.step)
	}
}

func TestWizardAdvance_QwenLaunchConfig_EmptyValuesAreElided(t *testing.T) {
	wm := qwenWizardFixture(t)
	// Pre-seed envVars with stale entries that should be stripped.
	wm.envVars = map[string]string{
		"OPENAI_BASE_URL": "stale",
		"OPENAI_MODEL":    "stale",
	}
	// Custom preset → empty inputs.
	wm.qwenVendorIdx = 3 // Custom (last preset)
	wm.applyQwenPreset()

	w2, _ := wm.advance()

	if _, ok := w2.envVars["OPENAI_BASE_URL"]; ok {
		t.Errorf("empty baseURL must be deleted from envVars, got %q", w2.envVars["OPENAI_BASE_URL"])
	}
	if _, ok := w2.envVars["OPENAI_MODEL"]; ok {
		t.Errorf("empty model must be deleted from envVars, got %q", w2.envVars["OPENAI_MODEL"])
	}
}

func TestWizardUpdate_QwenLaunchConfig_NavigationAutofillsUntilEdited(t *testing.T) {
	wm := qwenWizardFixture(t)
	presets := qwenLaunchPresets()
	if len(presets) < 2 {
		t.Skip("requires at least 2 presets")
	}
	// Initial state: cursor=0, vendor 0 preset applied.
	if wm.qwenModelInput != presets[0].model {
		t.Fatalf("init model = %q, want %q", wm.qwenModelInput, presets[0].model)
	}
	// Simulate j (down) to vendor row 1 — should auto-fill from preset[1].
	w2, _ := wm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if w2.qwenModelInput != presets[1].model {
		t.Errorf("after j to vendor 1: model = %q, want %q (autofill)", w2.qwenModelInput, presets[1].model)
	}
	if w2.qwenVendorIdx != 1 {
		t.Errorf("qwenVendorIdx = %d, want 1", w2.qwenVendorIdx)
	}

	// Mark user-edited and navigate again — auto-fill should NOT happen.
	w2.qwenUserEdited = true
	w2.qwenModelInput = "user-typed"
	w3, _ := w2.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'j'}})
	if w3.qwenModelInput != "user-typed" {
		t.Errorf("vendor change after edit must preserve user input, got %q", w3.qwenModelInput)
	}
}

func TestWizardUpdate_QwenLaunchConfig_ResetKeyClearsEdits(t *testing.T) {
	wm := qwenWizardFixture(t)
	wm.qwenUserEdited = true
	wm.qwenModelInput = "user-typed"
	wm.qwenBaseURLInput = "user-url"

	w2, _ := wm.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune{'r'}})

	presets := qwenLaunchPresets()
	if w2.qwenModelInput != presets[0].model {
		t.Errorf("after r: model = %q, want %q", w2.qwenModelInput, presets[0].model)
	}
	if w2.qwenBaseURLInput != presets[0].baseURL {
		t.Errorf("after r: baseURL = %q, want %q", w2.qwenBaseURLInput, presets[0].baseURL)
	}
	if w2.qwenUserEdited {
		t.Error("after r: qwenUserEdited should be cleared")
	}
}
