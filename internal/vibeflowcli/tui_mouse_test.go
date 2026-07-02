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
	"reflect"
	"testing"

	tea "charm.land/bubbletea/v2"
)

// TestSessionRowHeight verifies the row-height helper stays in lockstep with
// renderSessionRow's subtitle condition (the whole hitmap depends on this).
func TestSessionRowHeight(t *testing.T) {
	cases := []struct {
		name string
		row  SessionRow
		want int
	}{
		{"no metadata", SessionRow{Name: "a"}, 1},
		{"branch only", SessionRow{Name: "a", Branch: "main"}, 2},
		{"persona only", SessionRow{Name: "a", Persona: "developer"}, 2},
		{"project only", SessionRow{Name: "a", Project: "proj"}, 2},
		{"all set", SessionRow{Name: "a", Branch: "b", Persona: "p", Project: "pr"}, 2},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := sessionRowHeight(tc.row); got != tc.want {
				t.Fatalf("sessionRowHeight(%+v) = %d, want %d", tc.row, got, tc.want)
			}
		})
	}
}

// TestRenderSessionList_HitmapFlat proves the flat renderer records one span
// per session, with the correct variable height (rows with metadata take two
// lines) and cursor position, offset below the "Sessions" header (line 0).
func TestRenderSessionList_HitmapFlat(t *testing.T) {
	m := Model{
		hitmap: &listHitmap{},
		sessions: []SessionRow{
			{Name: "with-meta", Branch: "main"}, // 2 lines
			{Name: "bare"},                      // 1 line
		},
	}
	_ = m.renderSessionList(40, 20)

	want := []listRowSpan{
		{startY: 1, height: 2, pos: 0},
		{startY: 3, height: 1, pos: 1},
	}
	if !reflect.DeepEqual(m.hitmap.spans, want) {
		t.Fatalf("flat spans = %+v, want %+v", m.hitmap.spans, want)
	}
}

// TestRenderSessionList_HitmapGrouped proves the grouped renderer records a
// one-line span for each group header followed by its (expanded) sessions, and
// that collapsing a group drops its session spans.
func TestRenderSessionList_HitmapGrouped(t *testing.T) {
	base := func() Model {
		return Model{
			hitmap:    &listHitmap{},
			groupMode: true,
			sessions: []SessionRow{
				{Name: "s0"}, // 1 line
				{Name: "s1"}, // 1 line
			},
			groupOrder:      []string{"/repo"},
			groupedSessions: map[string][]int{"/repo": {0, 1}},
			collapsedGroups: map[string]bool{},
		}
	}

	// Expanded: header (pos0) + two sessions (pos1, pos2).
	m := base()
	_ = m.renderSessionList(40, 20)
	wantExpanded := []listRowSpan{
		{startY: 1, height: 1, pos: 0},
		{startY: 2, height: 1, pos: 1},
		{startY: 3, height: 1, pos: 2},
	}
	if !reflect.DeepEqual(m.hitmap.spans, wantExpanded) {
		t.Fatalf("grouped(expanded) spans = %+v, want %+v", m.hitmap.spans, wantExpanded)
	}

	// Collapsed: only the header span remains.
	mc := base()
	mc.collapsedGroups["/repo"] = true
	_ = mc.renderSessionList(40, 20)
	wantCollapsed := []listRowSpan{{startY: 1, height: 1, pos: 0}}
	if !reflect.DeepEqual(mc.hitmap.spans, wantCollapsed) {
		t.Fatalf("grouped(collapsed) spans = %+v, want %+v", mc.hitmap.spans, wantCollapsed)
	}
}

// TestHandleListClick_SelectsRow: a click on a different row moves the cursor
// there and does not attach (no command returned).
func TestHandleListClick_SelectsRow(t *testing.T) {
	m := Model{
		activeView: ViewSessions,
		cursor:     0,
		sessions:   []SessionRow{{Name: "a"}, {Name: "b"}},
		hitmap: &listHitmap{
			contentTop: 5,
			leftWidth:  30,
			spans: []listRowSpan{
				{startY: 1, height: 1, pos: 0},
				{startY: 2, height: 1, pos: 1},
			},
		},
	}
	// contentTop(5) + span.startY(2) = absolute Y 7 → selects pos 1.
	updated, cmd := m.handleListClick(5, 7)
	got := updated.(Model)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", got.cursor)
	}
	if cmd != nil {
		t.Fatalf("selecting a new row must not return a command")
	}
}

// TestHandleListClick_SecondClickAttaches: clicking the already-selected
// session activates it (returns an attach command).
func TestHandleListClick_SecondClickAttaches(t *testing.T) {
	m := Model{
		activeView: ViewSessions,
		cursor:     1,
		tmux:       NewTmuxManager("vftest"),
		sessions:   []SessionRow{{Name: "a"}, {Name: "b"}},
		hitmap: &listHitmap{
			contentTop: 5,
			leftWidth:  30,
			spans: []listRowSpan{
				{startY: 1, height: 1, pos: 0},
				{startY: 2, height: 1, pos: 1},
			},
		},
	}
	updated, cmd := m.handleListClick(5, 7) // pos 1, already selected
	got := updated.(Model)
	if got.cursor != 1 {
		t.Fatalf("cursor = %d, want 1", got.cursor)
	}
	if cmd == nil {
		t.Fatalf("clicking the selected session must return an attach command")
	}
}

// TestHandleListClick_OutsideList_Ignored: clicks in the right column or above
// the first row leave the cursor untouched.
func TestHandleListClick_OutsideList_Ignored(t *testing.T) {
	newModel := func() Model {
		return Model{
			activeView: ViewSessions,
			cursor:     0,
			sessions:   []SessionRow{{Name: "a"}, {Name: "b"}},
			hitmap: &listHitmap{
				contentTop: 5,
				leftWidth:  30,
				spans: []listRowSpan{
					{startY: 1, height: 1, pos: 0},
					{startY: 2, height: 1, pos: 1},
				},
			},
		}
	}

	// x beyond the left column (detail panel).
	if got, cmd := newModel().handleListClick(35, 7); got.(Model).cursor != 0 || cmd != nil {
		t.Fatalf("click in right column changed state: cursor=%d cmd=%v", got.(Model).cursor, cmd)
	}
	// y above the first row (on the "Sessions" header line at contentTop).
	if got, cmd := newModel().handleListClick(5, 5); got.(Model).cursor != 0 || cmd != nil {
		t.Fatalf("click on header line changed state: cursor=%d cmd=%v", got.(Model).cursor, cmd)
	}
	// y below the last row.
	if got, cmd := newModel().handleListClick(5, 99); got.(Model).cursor != 0 || cmd != nil {
		t.Fatalf("click below list changed state: cursor=%d cmd=%v", got.(Model).cursor, cmd)
	}
}

// TestHandleListClick_GroupHeaderTogglesCollapse: clicking a group header row
// selects it and flips its collapsed state.
func TestHandleListClick_GroupHeaderTogglesCollapse(t *testing.T) {
	m := Model{
		activeView:      ViewSessions,
		groupMode:       true,
		cursor:          2,
		sessions:        []SessionRow{{Name: "s0"}},
		groupOrder:      []string{"/repo"},
		groupedSessions: map[string][]int{"/repo": {0}},
		collapsedGroups: map[string]bool{},
		hitmap: &listHitmap{
			contentTop: 5,
			leftWidth:  30,
			spans:      []listRowSpan{{startY: 1, height: 1, pos: 0}}, // the header
		},
	}
	updated, cmd := m.handleListClick(5, 6) // contentTop(5)+startY(1) = 6 → header pos 0
	got := updated.(Model)
	if got.cursor != 0 {
		t.Fatalf("cursor = %d, want 0 (header)", got.cursor)
	}
	if !got.collapsedGroups["/repo"] {
		t.Fatalf("group should be collapsed after clicking its header")
	}
	if cmd != nil {
		t.Fatalf("toggling a group header must not return a command")
	}
}

// TestHandleMouse_WheelMovesCursor: wheel down/up move the selection and clamp
// at the list bounds.
func TestHandleMouse_WheelMovesCursor(t *testing.T) {
	wheel := func(btn tea.MouseButton) tea.MouseMsg {
		return tea.MouseWheelMsg{Button: btn}
	}
	m := Model{
		activeView: ViewSessions,
		cursor:     0,
		sessions:   []SessionRow{{Name: "a"}, {Name: "b"}, {Name: "c"}},
		hitmap:     &listHitmap{},
	}

	// Wheel up at the top is a no-op.
	if got, _ := m.handleMouse(wheel(tea.MouseWheelUp)); got.(Model).cursor != 0 {
		t.Fatalf("wheel up at top: cursor = %d, want 0", got.(Model).cursor)
	}
	// Wheel down advances.
	step, _ := m.handleMouse(wheel(tea.MouseWheelDown))
	m = step.(Model)
	if m.cursor != 1 {
		t.Fatalf("wheel down: cursor = %d, want 1", m.cursor)
	}
	// Advance to the last row and confirm it clamps.
	m2, _ := m.handleMouse(wheel(tea.MouseWheelDown))
	m = m2.(Model)
	m3, _ := m.handleMouse(wheel(tea.MouseWheelDown)) // already at max (2)
	m = m3.(Model)
	if m.cursor != 2 {
		t.Fatalf("wheel down clamp: cursor = %d, want 2", m.cursor)
	}
	// Wheel up steps back.
	m4, _ := m.handleMouse(wheel(tea.MouseWheelUp))
	if m4.(Model).cursor != 1 {
		t.Fatalf("wheel up: cursor = %d, want 1", m4.(Model).cursor)
	}
}

// TestHandleMouse_IgnoredOutsideSessionsView: mouse events are dropped when a
// sub-view or confirmation dialog is active.
func TestHandleMouse_IgnoredOutsideSessionsView(t *testing.T) {
	press := tea.MouseWheelMsg{Button: tea.MouseWheelDown}

	help := Model{activeView: ViewHelp, cursor: 0, sessions: []SessionRow{{Name: "a"}, {Name: "b"}}, hitmap: &listHitmap{}}
	if got, _ := help.handleMouse(press); got.(Model).cursor != 0 {
		t.Fatalf("help view: cursor changed to %d", got.(Model).cursor)
	}

	confirming := Model{activeView: ViewSessions, confirmQuit: true, cursor: 0, sessions: []SessionRow{{Name: "a"}, {Name: "b"}}, hitmap: &listHitmap{}}
	if got, _ := confirming.handleMouse(press); got.(Model).cursor != 0 {
		t.Fatalf("confirm dialog: cursor changed to %d", got.(Model).cursor)
	}
}

// TestHandleListClick_NilHitmap_Safe: a zero-value model (nil hitmap) never
// panics on a click.
func TestHandleListClick_NilHitmap_Safe(t *testing.T) {
	m := Model{activeView: ViewSessions}
	if got, cmd := m.handleListClick(1, 1); got.(Model).cursor != 0 || cmd != nil {
		t.Fatalf("nil hitmap click changed state")
	}
}
