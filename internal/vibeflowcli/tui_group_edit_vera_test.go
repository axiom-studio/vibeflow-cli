package vibeflowcli

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
)

func wizardPersonaIndex(w WizardModel, key string) int {
	for i, p := range w.personas {
		if p.key == key {
			return i
		}
	}
	return -1
}

func veraGroup() (SessionMeta, []SessionMeta) {
	dev := SessionMeta{Name: "dev", Provider: "claude", Persona: "developer", Project: "p", Branch: "main", WorkingDir: "/repo/a", SessionType: "vibeflow"}
	vera := SessionMeta{Name: "vera", Provider: "codex", Persona: "code_reviewer", Project: "p", Branch: "old", WorkingDir: "/repo/a", SessionType: "vibeflow", Vera: &veraBinding{ProjectID: 1, RepositoryLinkID: 2}}
	return dev, []SessionMeta{dev, vera}
}

func TestNewGroupEditWizard_CRAListsAndPreselectsVera(t *testing.T) {
	cfg := DefaultConfig()
	dev, group := veraGroup()

	w := NewGroupEditWizard(group, dev, NewProviderRegistry(cfg), "/repo/a", nil, cfg, true)

	vi := wizardPersonaIndex(w, "code_reviewer")
	if vi < 0 {
		t.Fatal("group edit with --cra must list Vera like New Agent")
	}
	if !w.selectedPersonas[vi] {
		t.Error("the group's running Vera must be preselected")
	}
	if !slices.Contains(w.groupRunning, "code_reviewer") {
		t.Errorf("groupRunning = %v, want Vera included", w.groupRunning)
	}
	if got := w.providers[w.resolvedProviderForPersona(vi)].key; got != "codex" {
		t.Errorf("Vera harness = %q, want codex from its session", got)
	}
}

func TestNewGroupEditWizard_WithoutCRAOmitsVera(t *testing.T) {
	cfg := DefaultConfig()
	dev, group := veraGroup()

	w := NewGroupEditWizard(group, dev, NewProviderRegistry(cfg), "/repo/a", nil, cfg, false)

	if wizardPersonaIndex(w, "code_reviewer") >= 0 {
		t.Error("group edit without --cra must not list Vera")
	}
	if !slices.Equal(w.groupRunning, []string{"developer"}) {
		t.Errorf("groupRunning = %v, want [developer]", w.groupRunning)
	}
}

func TestGroupEditWizard_TitleAndSteps(t *testing.T) {
	cfg := DefaultConfig()
	dev, group := veraGroup()
	w := NewGroupEditWizard(group, dev, NewProviderRegistry(cfg), "/repo/a", nil, cfg, true)

	view := w.View()
	if !strings.Contains(view, "Edit Group") || strings.Contains(view, "New Session") {
		t.Errorf("group edit title should say Edit Group, got:\n%s", view)
	}
	for _, skipped := range []string{"Directory", "Branch", "Worktree", "Permissions"} {
		if strings.Contains(view, " "+skipped+" ") {
			t.Errorf("group edit step line shows inherited step %q:\n%s", skipped, view)
		}
	}
}

func TestGroupEditWizard_VeraHarnessRestricted(t *testing.T) {
	cfg := DefaultConfig()
	cfg.Providers["shell"] = Provider{Name: "Shell", Binary: "sh"} // installed, not a review harness
	dev, group := veraGroup()
	reg := NewProviderRegistry(cfg)
	w := NewGroupEditWizard(group, dev, reg, "/repo/a", nil, cfg, true)
	vi := wizardPersonaIndex(w, "code_reviewer")
	for i, pe := range w.providers {
		if pe.key == "shell" {
			w.personaProviderIdx[vi] = i
		}
	}
	w.step = StepProvider

	w, _ = w.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	if w.step != StepProvider {
		t.Errorf("group edit advanced with a harness Vera cannot run (step %d)", w.step)
	}
}

func TestGroupSessionsFor_VeraJoinsCheckoutRegardlessOfBranch(t *testing.T) {
	dev, all := veraGroup()
	identity := func(dir string) string { return dir }

	if got := groupSessionsFor(dev, all, identity); len(got) != 2 {
		t.Errorf("coding anchor group = %d sessions, want dev and Vera", len(got))
	}
	if got := groupSessionsFor(all[1], all, identity); len(got) != 2 {
		t.Errorf("Vera anchor group = %d sessions, want Vera and dev", len(got))
	}
}

// Pressing e on a group whose first row is Vera edits the coding group with
// Vera preselected, inheriting settings from a coding session.
func TestUpdate_EKeyOnVeraGroupPreselectsVera(t *testing.T) {
	st := &Store{path: filepath.Join(t.TempDir(), "sessions.json")}
	_ = st.Add(SessionMeta{Name: "v", TmuxSession: "vibeflow_codex-v", Provider: "codex", Persona: "code_reviewer", Project: "p", Branch: "old", WorkingDir: "/work/a", SessionType: "vibeflow", Vera: &veraBinding{ProjectID: 1}})
	_ = st.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_claude-a", Provider: "claude", Persona: "developer", Project: "p", Branch: "main", WorkingDir: "/work/a", SessionType: "vibeflow", SkipPermissions: true})
	cfg := DefaultConfig()
	m := Model{
		store:         st,
		registry:      NewProviderRegistry(cfg),
		config:        cfg,
		craEnabled:    true,
		repoRootCache: map[string]string{"/work/a": "/work/a"},
		sessions: []SessionRow{
			{Name: "codex-v", WorkingDir: "/work/a", Branch: "old"},
			{Name: "claude-a", WorkingDir: "/work/a", Branch: "main"},
		},
	}

	nm, _ := m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	got := nm.(Model)
	if got.activeView != ViewWizard {
		t.Fatalf("e on Vera did not open group edit (view %d)", got.activeView)
	}
	w := got.wizard
	if w.groupAnchor == nil || w.groupAnchor.Persona != "developer" {
		t.Errorf("anchor = %+v, want the coding session", w.groupAnchor)
	}
	for _, key := range []string{"developer", "code_reviewer"} {
		if i := wizardPersonaIndex(w, key); i < 0 || !w.selectedPersonas[i] {
			t.Errorf("%s should be preselected", key)
		}
	}
}

func TestApplyGroupEdit_TickingVeraStartsVera(t *testing.T) {
	dev, _ := veraGroup()
	m := Model{craEnabled: true}

	msg := m.applyGroupEdit([]SessionMeta{dev}, WizardResult{Personas: []string{"developer", "code_reviewer"}, ProjectName: "p", WorkDir: "/repo/a"})

	req, ok := msg.(veraLaunchRequestedMsg)
	if !ok {
		t.Fatalf("applyGroupEdit = %T, want a Vera launch request", msg)
	}
	if !slices.Equal(req.result.Personas, []string{"code_reviewer"}) || req.result.WorkDir != "/repo/a" || req.result.ProjectName != "p" {
		t.Errorf("Vera launch result = %+v", req.result)
	}
}

func TestApplyGroupEdit_UntickingVeraStopsVera(t *testing.T) {
	m, tm, running := launchedGroupEditModel(t, "vftest-groupedit-vera", []string{"developer", "code_reviewer"})
	m.craEnabled = true

	_ = m.applyGroupEdit(running, WizardResult{Personas: []string{"developer"}})

	if tm.HasSession(running[1].TmuxSession) {
		t.Error("unticked Vera session is still running")
	}
	if !tm.HasSession(running[0].TmuxSession) {
		t.Error("kept developer session was stopped")
	}
}

// Without --cra the wizard hides Vera, but groupSessionsFor still counts a Vera
// session on another branch of the checkout as part of the group. Confirming
// Edit Group unchanged must not stop that hidden Vera session.
func TestUpdate_EKeyUnchangedConfirmWithoutCRAKeepsVera(t *testing.T) {
	m, tm, running := launchedGroupEditModel(t, "vftest-groupedit-nocra-"+itoa(os.Getpid()), []string{"developer", "code_reviewer"})
	running[1].Branch = "old"
	running[1].Vera = &veraBinding{ProjectID: 1, RepositoryLinkID: 2}
	if err := m.store.Add(running[1]); err != nil {
		t.Fatal(err)
	}
	dir := running[0].WorkingDir
	cfg := DefaultConfig()
	m.registry, m.config = NewProviderRegistry(cfg), cfg
	m.repoRootCache = map[string]string{dir: dir}
	m.sessions = []SessionRow{{Name: "claude-a", WorkingDir: dir, Branch: "main"}, {Name: "claude-b", WorkingDir: dir, Branch: "old"}}

	nm, _ := m.Update(tea.KeyPressMsg{Code: 'e', Text: "e"})
	m = nm.(Model)
	if m.activeView != ViewWizard {
		t.Fatalf("e did not open group edit (view %d)", m.activeView)
	}
	var cmd tea.Cmd
	for i := 0; i < 5 && m.activeView == ViewWizard; i++ {
		nm, cmd = m.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
		m = nm.(Model)
	}
	if cmd == nil {
		t.Fatal("confirming group edit returned no apply command")
	}
	_ = cmd()

	if !tm.HasSession(running[1].TmuxSession) {
		t.Error("unchanged Edit Group confirm without --cra stopped the hidden Vera session")
	}
	if !tm.HasSession(running[0].TmuxSession) {
		t.Error("developer session was stopped")
	}
}
