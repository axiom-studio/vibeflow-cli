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
	"os/exec"
	"path/filepath"
	"testing"
)

// These are real-tmux integration tests (no mocks): they drive the group-edit
// removal path against an actual tmux server on a throwaway socket. They guard
// issue #3438's QA rejection — deselecting an agent must actually kill its tmux
// session, which the old code failed to do for freshly-launched sessions because
// it keyed the kill off SessionMeta.Name (the base name) instead of
// SessionMeta.TmuxSession (the provider-prefixed session that actually exists).

// launchedGroupEditModel builds a Model wired to a real tmux server plus temp
// store/cache files, and creates one real "launched" session per persona (base
// name = persona initial-free short name, provider "claude"). It returns the
// model, the TmuxManager, and the running SessionMeta list for applyGroupEdit.
func launchedGroupEditModel(t *testing.T, socket string, personas []string) (Model, *TmuxManager, []SessionMeta) {
	t.Helper()
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager(socket)
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}

	dir := t.TempDir()
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))
	cache := NewSessionCacheWithPath(filepath.Join(dir, "cache.json"))

	var running []SessionMeta
	for i, persona := range personas {
		// Distinct base name per session; the store Name is the BASE name (as
		// executeLaunch stores it), while TmuxSession is provider-prefixed.
		base := string(rune('a' + i))
		if err := tm.CreateSessionWithOpts(SessionOpts{
			Name: base, Provider: "claude", WorkDir: dir, Command: "sleep 300", Persona: persona,
		}); err != nil {
			t.Fatalf("create session %s: %v", base, err)
		}
		meta := SessionMeta{
			Name:        base, // base name — NOT the tmux short/full name
			TmuxSession: tm.FullSessionName("claude", base),
			Provider:    "claude",
			Persona:     persona,
			Branch:      "main",
			WorkingDir:  dir,
		}
		if err := store.Add(meta); err != nil {
			t.Fatalf("store.Add(%s): %v", base, err)
		}
		if err := cache.Add(meta); err != nil {
			t.Fatalf("cache.Add(%s): %v", base, err)
		}
		running = append(running, meta)
	}

	m := Model{tmux: tm, store: store, cache: cache, logger: NewLogger(), config: &Config{}}
	return m, tm, running
}

func storeHasName(t *testing.T, s *Store, name string) bool {
	t.Helper()
	_, found, err := s.Get(name)
	if err != nil {
		t.Fatalf("store.Get(%s): %v", name, err)
	}
	return found
}

// TestKillSessionMeta_KillsLaunchedSessionByTmuxName isolates the fix: the tmux
// kill must target meta.TmuxSession, and the store/cache removal must key off
// meta.Name. With the pre-fix code (KillSession(meta.Name)) the base name "a"
// resolves to "vibeflow_a", leaving the real "vibeflow_claude-a" alive.
func TestKillSessionMeta_KillsLaunchedSessionByTmuxName(t *testing.T) {
	m, tm, running := launchedGroupEditModel(t, "vftest-killmeta", []string{"developer"})
	meta := running[0]

	if !tm.HasSession(meta.TmuxSession) {
		t.Fatalf("setup: session %s not running", meta.TmuxSession)
	}

	m.killSessionMeta(meta)

	if tm.HasSession(meta.TmuxSession) {
		t.Fatalf("tmux session %s still alive after killSessionMeta — killed the wrong target", meta.TmuxSession)
	}
	if storeHasName(t, m.store, meta.Name) {
		t.Fatalf("store still has %q after killSessionMeta", meta.Name)
	}
	if entries, _ := m.cache.List(); len(entries) != 0 {
		t.Fatalf("cache still has %d entries after killSessionMeta, want 0", len(entries))
	}
}

// TestApplyGroupEdit_RemovesDeselectedLaunchedSession drives the real reconcile
// path (the QA-reported flow): a two-agent group where the user deselects one
// persona. The deselected session's tmux process must be gone; the kept one must
// survive. This is the exact scenario from the #3438 QA rejection.
func TestApplyGroupEdit_RemovesDeselectedLaunchedSession(t *testing.T) {
	m, tm, running := launchedGroupEditModel(t, "vftest-groupedit-remove", []string{"developer", "architect"})
	dev, arch := running[0], running[1]

	// Desired lineup keeps only "architect" → "developer" is deselected.
	result := WizardResult{Personas: []string{"architect"}}
	_ = m.applyGroupEdit(running, result)

	if tm.HasSession(dev.TmuxSession) {
		t.Errorf("deselected session %s still alive — QA rejection not fixed", dev.TmuxSession)
	}
	if !tm.HasSession(arch.TmuxSession) {
		t.Errorf("kept session %s was killed — over-removal", arch.TmuxSession)
	}
	if storeHasName(t, m.store, dev.Name) {
		t.Errorf("store still has deselected %q", dev.Name)
	}
	if !storeHasName(t, m.store, arch.Name) {
		t.Errorf("store lost kept %q", arch.Name)
	}
}
