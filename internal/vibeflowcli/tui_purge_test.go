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

	tea "charm.land/bubbletea/v2"
)

// TestSessionsMsg_PendingPurgeRaisesConfirmation verifies that dead stored
// sessions are surfaced for confirmation rather than being purged silently.
// This is the guard against the socket-mismatch wipe: a refresh that finds
// stored sessions absent from tmux leaves sessions.json untouched and asks.
func TestSessionsMsg_PendingPurgeRaisesConfirmation(t *testing.T) {
	st := testStore(t)
	_ = st.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_a"})
	_ = st.Add(SessionMeta{Name: "b", TmuxSession: "vibeflow_b"})

	m := Model{store: st, purgeDeclined: make(map[string]bool)}
	nm, _ := m.Update(sessionsMsg{pendingPurge: []SessionMeta{
		{Name: "a", TmuxSession: "vibeflow_a"},
		{Name: "b", TmuxSession: "vibeflow_b"},
	}})
	mm := nm.(Model)

	if !mm.confirmPurge {
		t.Fatal("expected confirmPurge to be raised for pending purge")
	}
	if len(mm.purgeCandidates) != 2 {
		t.Fatalf("purgeCandidates = %d, want 2", len(mm.purgeCandidates))
	}
	// Nothing removed yet — the store must be intact until the user confirms.
	if sessions, _ := st.List(); len(sessions) != 2 {
		t.Errorf("store must be untouched before confirmation: got %d, want 2", len(sessions))
	}
}

// TestConfirmPurge_YesRemovesCandidates verifies that confirming the prompt
// removes exactly the candidate sessions and leaves others alone.
func TestConfirmPurge_YesRemovesCandidates(t *testing.T) {
	st := testStore(t)
	_ = st.Add(SessionMeta{Name: "a", TmuxSession: "vibeflow_a"})
	_ = st.Add(SessionMeta{Name: "b", TmuxSession: "vibeflow_b"})

	m := Model{
		store:           st,
		purgeDeclined:   make(map[string]bool),
		confirmPurge:    true,
		purgeCandidates: []SessionMeta{{Name: "b", TmuxSession: "vibeflow_b"}},
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'y', Text: "y"})
	mm := nm.(Model)

	if mm.confirmPurge {
		t.Error("confirmPurge should be cleared after y")
	}
	if _, ok, _ := st.Get("b"); ok {
		t.Error("confirmed candidate 'b' should have been removed")
	}
	if _, ok, _ := st.Get("a"); !ok {
		t.Error("non-candidate 'a' must be preserved")
	}
}

// TestConfirmPurge_NoKeepsAndSuppresses verifies that declining keeps the
// sessions and records the decision so the user is not re-prompted.
func TestConfirmPurge_NoKeepsAndSuppresses(t *testing.T) {
	st := testStore(t)
	_ = st.Add(SessionMeta{Name: "b", TmuxSession: "vibeflow_b"})

	m := Model{
		store:           st,
		purgeDeclined:   make(map[string]bool),
		confirmPurge:    true,
		purgeCandidates: []SessionMeta{{Name: "b", TmuxSession: "vibeflow_b"}},
	}
	nm, _ := m.Update(tea.KeyPressMsg{Code: 'n', Text: "n"})
	mm := nm.(Model)

	if mm.confirmPurge {
		t.Error("confirmPurge should be cleared after n")
	}
	if !mm.purgeDeclined["vibeflow_b"] {
		t.Error("declined session should be recorded to suppress re-prompt")
	}
	if _, ok, _ := st.Get("b"); !ok {
		t.Error("declined session must be kept in the store")
	}
}
