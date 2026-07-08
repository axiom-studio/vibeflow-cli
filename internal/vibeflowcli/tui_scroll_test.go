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

import "testing"

// bareSessions builds n single-line sessions (no metadata → sessionRowHeight 1),
// giving a predictable body of n lines for viewport math.
func bareSessions(n int) []SessionRow {
	rows := make([]SessionRow, n)
	for i := range rows {
		rows[i] = SessionRow{Name: string(rune('a' + i))}
	}
	return rows
}

// spanForPos returns the hitmap span mapped to cursor position pos, or nil.
func spanForPos(m Model, pos int) *listRowSpan {
	for i := range m.hitmap.spans {
		if m.hitmap.spans[i].pos == pos {
			return &m.hitmap.spans[i]
		}
	}
	return nil
}

// TestRenderSessionList_NoScrollWhenListFits: a list shorter than the viewport
// stays anchored at top 0 and records a span for every row (no regression to the
// pre-viewport behavior).
func TestRenderSessionList_NoScrollWhenListFits(t *testing.T) {
	m := Model{hitmap: &listHitmap{}, sessions: bareSessions(3)}
	_ = m.renderSessionList(40, 20) // avail = 19 >> 3

	if m.hitmap.top != 0 {
		t.Fatalf("top = %d, want 0 for a list that fits", m.hitmap.top)
	}
	if len(m.hitmap.spans) != 3 {
		t.Fatalf("got %d spans, want 3 (one per row)", len(m.hitmap.spans))
	}
}

// TestRenderSessionList_ScrollsToKeepCursorVisible: with more rows than fit, a
// cursor past the fold scrolls the window down by the minimum needed, the cursor
// row gets a visible span, and rows scrolled off the top have none.
func TestRenderSessionList_ScrollsToKeepCursorVisible(t *testing.T) {
	m := Model{hitmap: &listHitmap{}, sessions: bareSessions(10), cursor: 7}
	_ = m.renderSessionList(40, 6) // avail = 5

	// cursorStart=7, height=1, avail=5 → top = 7+1-5 = 3.
	if m.hitmap.top != 3 {
		t.Fatalf("top = %d, want 3", m.hitmap.top)
	}
	// Exactly `avail` single-line rows are visible (pos 3..7).
	if len(m.hitmap.spans) != 5 {
		t.Fatalf("got %d spans, want 5 (avail)", len(m.hitmap.spans))
	}
	if s := spanForPos(m, 7); s == nil {
		t.Fatal("cursor row (pos 7) has no span — it is off-screen")
	} else if s.startY != 5 {
		t.Fatalf("cursor span startY = %d, want 5 (last visible body line)", s.startY)
	}
	// Rows above the window are not clickable.
	for _, off := range []int{0, 1, 2} {
		if spanForPos(m, off) != nil {
			t.Fatalf("pos %d scrolled above the fold but still has a span", off)
		}
	}
	// Rows below the window are not clickable.
	for _, off := range []int{8, 9} {
		if spanForPos(m, off) != nil {
			t.Fatalf("pos %d below the fold but still has a span", off)
		}
	}
}

// TestRenderSessionList_ScrollFollowsCursorUp: the persisted offset scrolls back
// up when the cursor moves above the current window.
func TestRenderSessionList_ScrollFollowsCursorUp(t *testing.T) {
	m := Model{hitmap: &listHitmap{}, sessions: bareSessions(10), cursor: 7}
	_ = m.renderSessionList(40, 6) // top → 3
	if m.hitmap.top != 3 {
		t.Fatalf("setup: top = %d, want 3", m.hitmap.top)
	}

	// Move the cursor above the window; the offset must follow it up.
	m.cursor = 1
	_ = m.renderSessionList(40, 6)
	if m.hitmap.top != 1 {
		t.Fatalf("top = %d, want 1 after cursor moved to row 1", m.hitmap.top)
	}
	if spanForPos(m, 1) == nil {
		t.Fatal("cursor row (pos 1) has no span after scroll-up")
	}
}

// TestRenderSessionList_ClickReachesRowBelowFold: the core bug — a row that is
// below the visible fold at top 0 becomes clickable once the cursor scrolls the
// window to it. handleListClick resolves the click via the windowed hitmap.
func TestRenderSessionList_ClickReachesRowBelowFold(t *testing.T) {
	m := Model{activeView: ViewSessions, hitmap: &listHitmap{}, sessions: bareSessions(10), cursor: 7}
	_ = m.renderSessionList(40, 6) // avail=5, top=3 → pos 5,6,7 now visible

	// pos 5 was below the fold at top 0 (only 0..4 visible); it now has a span.
	span := spanForPos(m, 5)
	if span == nil {
		t.Fatal("pos 5 unreachable after scroll — no span")
	}
	// Wire up the click geometry the way View does, then click pos 5's line.
	m.hitmap.contentTop = 0
	m.hitmap.leftWidth = 40
	updated, cmd := m.handleListClick(3, span.startY) // contentTop 0 + startY
	if got := updated.(Model).cursor; got != 5 {
		t.Fatalf("click resolved to cursor %d, want 5", got)
	}
	if cmd != nil {
		t.Fatal("selecting a new row should not attach (nil cmd expected)")
	}
}

// TestRenderSessionList_GroupedScrollWindowsHeadersAndRows: grouped mode windows
// group headers and their session rows together, keeping the cursor visible.
func TestRenderSessionList_GroupedScrollWindowsHeadersAndRows(t *testing.T) {
	m := Model{
		hitmap:    &listHitmap{},
		groupMode: true,
		sessions:  bareSessions(8),
		// One group holding all 8 sessions → positions: header=0, sessions=1..8.
		groupOrder:      []string{"/repo"},
		groupedSessions: map[string][]int{"/repo": {0, 1, 2, 3, 4, 5, 6, 7}},
		collapsedGroups: map[string]bool{},
		cursor:          8, // last session (grouped pos 8)
	}
	_ = m.renderSessionList(40, 6) // avail = 5

	// total body = 9 lines (1 header + 8 rows); cursorStart=8 → top = 8+1-5 = 4.
	if m.hitmap.top != 4 {
		t.Fatalf("top = %d, want 4", m.hitmap.top)
	}
	if len(m.hitmap.spans) != 5 {
		t.Fatalf("got %d spans, want 5 (avail)", len(m.hitmap.spans))
	}
	if spanForPos(m, 8) == nil {
		t.Fatal("cursor (grouped pos 8) has no span — off-screen")
	}
	// The group header (pos 0) scrolled off the top.
	if spanForPos(m, 0) != nil {
		t.Fatal("group header (pos 0) scrolled off but still has a span")
	}
}
