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
)

// personaIndex returns the index of a persona key within defaultPersonas(),
// or -1 if absent. Local test helper so assertions read by key, not by number.
func personaIndex(key string) int {
	for i, p := range defaultPersonas() {
		if p.key == key {
			return i
		}
	}
	return -1
}

// --- Group membership resolution (repo root + branch) ---

func TestGroupSessionsFor_MatchesRepoRootAndBranch(t *testing.T) {
	// repoRoot normalizes a worktree checkout back to its repo root so that a
	// worktree session groups with the main checkout it was cut from.
	repoRoot := func(dir string) string {
		if i := strings.Index(dir, "/.wt/"); i >= 0 {
			return dir[:i]
		}
		return dir
	}

	anchor := SessionMeta{Name: "a", Persona: "developer", WorkingDir: "/repo/a", Branch: "main"}
	all := []SessionMeta{
		anchor,
		{Name: "b", Persona: "architect", WorkingDir: "/repo/a", Branch: "main"},        // same root+branch → in
		{Name: "c", Persona: "qa_lead", WorkingDir: "/repo/a/.wt/feat", Branch: "main"}, // worktree of same root → in
		{Name: "d", Persona: "developer", WorkingDir: "/repo/a", Branch: "feature-x"},   // different branch → out
		{Name: "e", Persona: "developer", WorkingDir: "/repo/other", Branch: "main"},    // different repo → out
	}

	group := groupSessionsFor(anchor, all, repoRoot)

	gotNames := make(map[string]bool)
	for _, m := range group {
		gotNames[m.Name] = true
	}
	if len(group) != 3 {
		t.Fatalf("group size = %d, want 3 (names: %v)", len(group), gotNames)
	}
	for _, want := range []string{"a", "b", "c"} {
		if !gotNames[want] {
			t.Errorf("expected session %q in group, got %v", want, gotNames)
		}
	}
	for _, notWant := range []string{"d", "e"} {
		if gotNames[notWant] {
			t.Errorf("session %q should be excluded (branch/repo mismatch)", notWant)
		}
	}
}

func TestGroupSessionsFor_SingletonGroupIsJustAnchor(t *testing.T) {
	identity := func(dir string) string { return dir }
	anchor := SessionMeta{Name: "solo", Persona: "developer", WorkingDir: "/repo/a", Branch: "main"}
	all := []SessionMeta{
		anchor,
		{Name: "other", Persona: "developer", WorkingDir: "/repo/b", Branch: "main"},
	}

	group := groupSessionsFor(anchor, all, identity)
	if len(group) != 1 || group[0].Name != "solo" {
		t.Fatalf("group = %+v, want just the anchor session", group)
	}
}

// --- Add/remove diff logic ---

func TestDiffGroupPersonas_AddsAndRemoves(t *testing.T) {
	running := []string{"developer", "architect", "qa_lead"}
	desired := []string{"developer", "security_lead", "qa_lead"}

	toAdd, toRemove := diffGroupPersonas(running, desired)

	if len(toAdd) != 1 || toAdd[0] != "security_lead" {
		t.Errorf("toAdd = %v, want [security_lead]", toAdd)
	}
	if len(toRemove) != 1 || toRemove[0] != "architect" {
		t.Errorf("toRemove = %v, want [architect]", toRemove)
	}
}

func TestDiffGroupPersonas_NoChange(t *testing.T) {
	set := []string{"developer", "architect"}
	toAdd, toRemove := diffGroupPersonas(set, set)
	if len(toAdd) != 0 || len(toRemove) != 0 {
		t.Errorf("expected no changes, got add=%v remove=%v", toAdd, toRemove)
	}
}

func TestDiffGroupPersonas_AllAddAllRemove(t *testing.T) {
	// Empty running → everything is an add.
	toAdd, toRemove := diffGroupPersonas(nil, []string{"developer", "qa_lead"})
	if len(toAdd) != 2 || toAdd[0] != "developer" || toAdd[1] != "qa_lead" {
		t.Errorf("toAdd = %v, want [developer qa_lead] in order", toAdd)
	}
	if len(toRemove) != 0 {
		t.Errorf("toRemove = %v, want empty", toRemove)
	}

	// Empty desired → everything is a remove.
	toAdd, toRemove = diffGroupPersonas([]string{"developer", "qa_lead"}, nil)
	if len(toRemove) != 2 || toRemove[0] != "developer" || toRemove[1] != "qa_lead" {
		t.Errorf("toRemove = %v, want [developer qa_lead] in order", toRemove)
	}
	if len(toAdd) != 0 {
		t.Errorf("toAdd = %v, want empty", toAdd)
	}
}

func TestDiffGroupPersonas_PreservesOrder(t *testing.T) {
	// Adds follow desired order; removes follow running order.
	running := []string{"a", "b", "c", "d"}
	desired := []string{"d", "z", "a", "y"}
	toAdd, toRemove := diffGroupPersonas(running, desired)

	if strings.Join(toAdd, ",") != "z,y" {
		t.Errorf("toAdd = %v, want [z y] (desired order)", toAdd)
	}
	if strings.Join(toRemove, ",") != "b,c" {
		t.Errorf("toRemove = %v, want [b c] (running order)", toRemove)
	}
}

// --- Wizard seeding from an anchor SessionMeta ---

func TestNewGroupEditWizard_SeedsFromAnchorAndRunningGroup(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	anchor := SessionMeta{
		Name:              "titan",
		Provider:          "claude",
		Persona:           "principal_engineer",
		Project:           "vibeflow-cli",
		Branch:            "main",
		WorkingDir:        "/repo/a",
		SessionType:       "vibeflow",
		SkipPermissions:   true,
		LLMGatewayEnabled: true,
	}
	group := []SessionMeta{
		anchor,
		{Name: "arch", Provider: "gemini", Persona: "architect", Branch: "main", WorkingDir: "/repo/a"},
	}

	w := NewGroupEditWizard(group, anchor, reg, "/repo/a", nil, cfg)

	if !w.groupEdit {
		t.Error("groupEdit should be true")
	}
	if w.step != StepTeam {
		t.Errorf("step = %d, want StepTeam (%d)", w.step, StepTeam)
	}
	if w.groupAnchor == nil || w.groupAnchor.Name != "titan" {
		t.Fatalf("groupAnchor not seeded from anchor: %+v", w.groupAnchor)
	}

	// Shared settings inherited from the anchor.
	if len(w.branches) != 1 || w.branches[0] != "main" {
		t.Errorf("branches = %v, want [main]", w.branches)
	}
	if w.selectedWorkDir != "/repo/a" {
		t.Errorf("selectedWorkDir = %q, want /repo/a", w.selectedWorkDir)
	}
	if !w.llmGatewayEnabled {
		t.Error("llmGatewayEnabled should be inherited as true")
	}
	if w.providers[w.selectedProvider].key != "claude" {
		t.Errorf("selectedProvider key = %q, want claude", w.providers[w.selectedProvider].key)
	}

	// Every running persona is pre-checked.
	peIdx := personaIndex("principal_engineer")
	archIdx := personaIndex("architect")
	if !w.selectedPersonas[peIdx] {
		t.Error("principal_engineer should be pre-checked")
	}
	if !w.selectedPersonas[archIdx] {
		t.Error("architect should be pre-checked")
	}
	// A persona NOT in the group must not be checked.
	if w.selectedPersonas[personaIndex("qa_lead")] {
		t.Error("qa_lead is not running and must not be pre-checked")
	}

	// groupRunning records exactly the running persona keys.
	if len(w.groupRunning) != 2 {
		t.Fatalf("groupRunning = %v, want 2 entries", w.groupRunning)
	}

	// Per-persona provider override: architect ran under gemini (differs from
	// the anchor's claude), so its resolved provider must be gemini while the
	// anchor persona keeps the team default.
	if got := w.providers[w.resolvedProviderForPersona(archIdx)].key; got != "gemini" {
		t.Errorf("architect resolved provider = %q, want gemini (per-persona override)", got)
	}
	if got := w.providers[w.resolvedProviderForPersona(peIdx)].key; got != "claude" {
		t.Errorf("principal_engineer resolved provider = %q, want claude (team default)", got)
	}
}

func TestNewGroupEditWizard_EmptyGroupFallsBackToAnchorPersona(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	anchor := SessionMeta{Provider: "claude", Persona: "developer", Branch: "main", WorkingDir: "/repo/a"}

	w := NewGroupEditWizard(nil, anchor, reg, "/repo/a", nil, cfg)

	if !w.selectedPersonas[personaIndex("developer")] {
		t.Error("with an empty group, the anchor persona (developer) must be pre-checked")
	}
	if len(w.groupRunning) != 1 || w.groupRunning[0] != "developer" {
		t.Errorf("groupRunning = %v, want [developer]", w.groupRunning)
	}
}

func TestBuildGroupEditResult_InheritsSharedSettingsAndDesiredPersonas(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)

	anchor := SessionMeta{
		Provider:          "claude",
		Persona:           "principal_engineer",
		Project:           "vibeflow-cli",
		Branch:            "main",
		WorkingDir:        "/repo/a",
		SessionType:       "vibeflow",
		SkipPermissions:   true,
		LLMGatewayEnabled: true,
	}
	group := []SessionMeta{
		anchor,
		{Name: "arch", Provider: "gemini", Persona: "architect", Branch: "main", WorkingDir: "/repo/a"},
	}

	w := NewGroupEditWizard(group, anchor, reg, "/repo/a", nil, cfg)
	// User adds qa_lead to the lineup.
	w.selectedPersonas[personaIndex("qa_lead")] = true

	w, _ = w.buildGroupEditResult()
	r := w.result

	if !w.done {
		t.Error("buildGroupEditResult should mark the wizard done")
	}
	// Shared settings inherited verbatim from the anchor.
	if r.Branch != "main" {
		t.Errorf("result.Branch = %q, want main", r.Branch)
	}
	if r.WorkDir != "/repo/a" {
		t.Errorf("result.WorkDir = %q, want /repo/a", r.WorkDir)
	}
	if r.ProjectName != "vibeflow-cli" {
		t.Errorf("result.ProjectName = %q, want vibeflow-cli", r.ProjectName)
	}
	if !r.SkipPermissions {
		t.Error("result.SkipPermissions should be inherited as true")
	}
	if !r.LLMGatewayEnabled {
		t.Error("result.LLMGatewayEnabled should be inherited as true")
	}
	if r.WorktreeChoice != WorktreeCurrent {
		t.Errorf("result.WorktreeChoice = %v, want WorktreeCurrent", r.WorktreeChoice)
	}

	// Desired persona set carries all three checked personas.
	gotPersonas := make(map[string]bool)
	for _, p := range r.Personas {
		gotPersonas[p] = true
	}
	for _, want := range []string{"principal_engineer", "architect", "qa_lead"} {
		if !gotPersonas[want] {
			t.Errorf("result.Personas missing %q (got %v)", want, r.Personas)
		}
	}

	// Per-persona provider override preserved for architect (gemini), and no
	// override recorded for personas that match the team default (claude).
	if r.PersonaProviders["architect"] != "gemini" {
		t.Errorf("PersonaProviders[architect] = %q, want gemini", r.PersonaProviders["architect"])
	}
	if _, ok := r.PersonaProviders["principal_engineer"]; ok {
		t.Error("principal_engineer matches team default — should have no per-persona override")
	}
}
