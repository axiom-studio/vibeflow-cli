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
)

func TestRestartSelectModel_InitialState(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a", Provider: "claude", Persona: "developer"},
		{Name: "session-b", Provider: "codex", Persona: "architect"},
	}
	r := NewRestartSelectModel(dead)

	if r.cursor != 0 {
		t.Errorf("cursor = %d, want 0", r.cursor)
	}
	if len(r.selected) != 0 {
		t.Errorf("selected should be empty initially")
	}
	if r.done || r.skipped {
		t.Error("should not be done or skipped initially")
	}
}

func TestRestartSelectModel_ToggleSelection(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a", Provider: "claude"},
		{Name: "session-b", Provider: "codex"},
	}
	r := NewRestartSelectModel(dead)

	// Toggle first item.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if !r.selected[0] {
		t.Error("item 0 should be selected after space")
	}

	// Toggle again to deselect.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})
	if r.selected[0] {
		t.Error("item 0 should be deselected after second space")
	}
}

func TestRestartSelectModel_Navigation(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a"},
		{Name: "session-b"},
		{Name: "session-c"},
	}
	r := NewRestartSelectModel(dead)

	// Move down.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if r.cursor != 1 {
		t.Errorf("cursor = %d, want 1", r.cursor)
	}

	// Move down again.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if r.cursor != 2 {
		t.Errorf("cursor = %d, want 2", r.cursor)
	}

	// Can't go past end.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("j")})
	if r.cursor != 2 {
		t.Errorf("cursor = %d, want 2 (clamped)", r.cursor)
	}

	// Move up.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("k")})
	if r.cursor != 1 {
		t.Errorf("cursor = %d, want 1", r.cursor)
	}
}

func TestRestartSelectModel_SelectAll(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a"},
		{Name: "session-b"},
	}
	r := NewRestartSelectModel(dead)

	// Select all.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if !r.selected[0] || !r.selected[1] {
		t.Error("all items should be selected after 'a'")
	}

	// Toggle all off.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune("a")})
	if r.selected[0] || r.selected[1] {
		t.Error("all items should be deselected after second 'a'")
	}
}

func TestRestartSelectModel_EnterWithSelection(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a", Provider: "claude"},
		{Name: "session-b", Provider: "codex"},
	}
	r := NewRestartSelectModel(dead)

	// Select first item.
	r, _ = r.Update(tea.KeyMsg{Type: tea.KeyRunes, Runes: []rune(" ")})

	// Press enter.
	r, cmd := r.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !r.done {
		t.Error("should be done after enter with selection")
	}

	// Execute the command and check the message.
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg := cmd()
	confirm, ok := msg.(restartConfirmMsg)
	if !ok {
		t.Fatalf("expected restartConfirmMsg, got %T", msg)
	}
	if len(confirm.sessions) != 1 {
		t.Fatalf("expected 1 session to restart, got %d", len(confirm.sessions))
	}
	if confirm.sessions[0].Name != "session-a" {
		t.Errorf("expected session-a, got %q", confirm.sessions[0].Name)
	}
}

func TestRestartSelectModel_EnterNoSelection(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a"},
	}
	r := NewRestartSelectModel(dead)

	// Press enter without selecting anything → skip.
	r, cmd := r.Update(tea.KeyMsg{Type: tea.KeyEnter})
	if !r.skipped {
		t.Error("should be skipped after enter with no selection")
	}
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg := cmd()
	if _, ok := msg.(restartSkipMsg); !ok {
		t.Fatalf("expected restartSkipMsg, got %T", msg)
	}
}

func TestRestartSelectModel_EscSkips(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a"},
	}
	r := NewRestartSelectModel(dead)

	r, cmd := r.Update(tea.KeyMsg{Type: tea.KeyEsc})
	if !r.skipped {
		t.Error("should be skipped after esc")
	}
	if cmd == nil {
		t.Fatal("expected a command")
	}
	msg := cmd()
	if _, ok := msg.(restartSkipMsg); !ok {
		t.Fatalf("expected restartSkipMsg, got %T", msg)
	}
}

func TestRestartSelectModel_View(t *testing.T) {
	dead := []SessionMeta{
		{Name: "session-a", Provider: "claude", Persona: "developer", Branch: "main", Project: "myproj"},
		{Name: "session-b", Provider: "codex", Branch: "feature"},
	}
	r := NewRestartSelectModel(dead)

	view := r.View()
	if !strings.Contains(view, "Dead sessions detected") {
		t.Error("view should contain header")
	}
	if !strings.Contains(view, "session-a") {
		t.Error("view should contain session-a")
	}
	if !strings.Contains(view, "session-b") {
		t.Error("view should contain session-b")
	}
	if !strings.Contains(view, "space: toggle") {
		t.Error("view should contain help text")
	}
}
