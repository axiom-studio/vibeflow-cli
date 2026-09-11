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

// TestView_HitmapOffsetMatchesRender drives the real View() render and checks
// that the hitmap's contentTop lands on the actual terminal row of the
// "Sessions" header — proving a click's Y resolves to the row the user sees.
func TestView_HitmapOffsetMatchesRender(t *testing.T) {
	m := Model{
		config:   &Config{},
		hitmap:   &listHitmap{},
		width:    100,
		height:   30,
		sessions: []SessionRow{{Name: "alpha"}, {Name: "beta"}},
	}
	view := m.View().Content
	lines := strings.Split(view, "\n")

	hdrRow := -1
	for i, l := range lines {
		if strings.Contains(l, "Sessions (flat)") {
			hdrRow = i
			break
		}
	}
	if hdrRow < 0 {
		t.Fatalf("could not find 'Sessions (flat)' header in rendered view:\n%s", view)
	}
	if m.hitmap.contentTop != hdrRow {
		t.Fatalf("hitmap.contentTop=%d but 'Sessions' header rendered at terminal row %d (off by %d)",
			m.hitmap.contentTop, hdrRow, hdrRow-m.hitmap.contentTop)
	}

	// A click on the first session row (contentTop + startY) must select it.
	var span0 listRowSpan
	found := false
	for _, s := range m.hitmap.spans {
		if s.pos == 0 {
			span0 = s
			found = true
			break
		}
	}
	if !found {
		t.Fatalf("no hitmap span for session 0; spans=%+v", m.hitmap.spans)
	}
	m.cursor = 1
	updated, _ := m.handleListClick(3, m.hitmap.contentTop+span0.startY)
	if updated.(Model).cursor != 0 {
		t.Fatalf("click on session 0's row (y=%d) did not move cursor there (got %d)",
			m.hitmap.contentTop+span0.startY, updated.(Model).cursor)
	}
}
