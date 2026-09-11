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

// hasExistingSessionState gates the first-run setup wizard (issue #3484): a root
// created by the headless spawn/dispatch path has a sessions.json (or live tmux
// sessions) but no config.yaml, and must attach rather than show the fresh
// install screen. These tests pin that behavior.

func TestHasExistingSessionState_StoreEntry(t *testing.T) {
	store := NewStoreWithPath(filepath.Join(t.TempDir(), "sessions.json"))
	if err := store.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_codex-x", Provider: "codex"}); err != nil {
		t.Fatalf("Add: %v", err)
	}
	// A stored session alone is enough — no tmux needed.
	if !hasExistingSessionState(store, nil) {
		t.Fatal("expected true when the store holds a session entry")
	}
}

func TestHasExistingSessionState_Empty(t *testing.T) {
	store := NewStoreWithPath(filepath.Join(t.TempDir(), "sessions.json"))
	if hasExistingSessionState(store, nil) {
		t.Fatal("expected false for an empty store and no tmux")
	}
	if hasExistingSessionState(nil, nil) {
		t.Fatal("expected false when both store and tmux are nil")
	}
}

// TestSetupWizardGate_StatePresentVsEmptyRoot exercises the exact gate condition
// from runTUI: `!ConfigFileExists(cfgPath) && !hasExistingSessionState(...)`.
func TestSetupWizardGate_StatePresentVsEmptyRoot(t *testing.T) {
	origRoot := rootDir
	t.Cleanup(func() { rootDir = origRoot })
	t.Setenv("VIBEFLOW_ROOT", "")

	// Empty root (no config.yaml, no sessions) → wizard SHOULD show.
	SetRootDir(t.TempDir())
	emptyStore := NewStore() // DefaultStorePath = <root>/sessions.json
	if show := !ConfigFileExists(ConfigPath()) && !hasExistingSessionState(emptyStore, nil); !show {
		t.Fatal("empty root: expected the setup wizard to show")
	}

	// Root with sessions.json entries but no config.yaml (the reporter's case,
	// e.g. a dispatcher-created root) → wizard SUPPRESSED.
	SetRootDir(t.TempDir())
	stateStore := NewStore()
	if err := stateStore.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_codex-x", Provider: "codex"}); err != nil {
		t.Fatalf("seed store: %v", err)
	}
	if ConfigFileExists(ConfigPath()) {
		t.Fatal("precondition failed: config.yaml should be absent")
	}
	if show := !ConfigFileExists(ConfigPath()) && !hasExistingSessionState(stateStore, nil); show {
		t.Fatal("state-present/config-absent root: expected the setup wizard to be suppressed")
	}
}

// TestHasExistingSessionState_LiveTmux is a real-tmux integration test (no mocks)
// for the live-session branch: an empty store but a live tmux session on the
// resolved socket must still count as existing state.
func TestHasExistingSessionState_LiveTmux(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	tm := NewTmuxManager("vibeflow-test-3484-live")
	_, _ = tm.run("kill-server")
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	if err := tm.EnsureServer(); err != nil {
		t.Skipf("cannot start tmux server: %v", err)
	}
	dir := t.TempDir()
	store := NewStoreWithPath(filepath.Join(dir, "sessions.json"))

	// Empty store, freshly started server with no sessions → false.
	if hasExistingSessionState(store, tm) {
		t.Fatal("expected false with an empty store and no live tmux sessions")
	}

	// A live tmux session that is NOT recorded in the store → true via the
	// live-tmux branch.
	if err := tm.CreateSessionWithOpts(SessionOpts{
		Name: "a", Provider: "codex", WorkDir: dir, Command: "sleep 300",
	}); err != nil {
		t.Fatalf("create session: %v", err)
	}
	if !hasExistingSessionState(store, tm) {
		t.Fatal("expected true when a live tmux session exists on the socket")
	}
}
