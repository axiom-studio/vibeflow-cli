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

	"github.com/charmbracelet/lipgloss"
)

// TestView_FitsTerminalHeight_HelpBarLastRow guards the #3341 regression: the
// rendered view must be exactly the terminal height with the keyboard
// shortcuts bar on the last row. Bubble Tea v2 crops overflow at the bottom
// (v1 cropped at the top), so any extra line — like the leading newline in the
// bannerText const — silently amputates the help bar.
func TestView_FitsTerminalHeight_HelpBarLastRow(t *testing.T) {
	cases := []struct {
		name    string
		warning string
	}{
		{"no warning line", ""},
		{"with server warning line", "Server unreachable (test)"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			m := Model{
				config:        &Config{},
				hitmap:        &listHitmap{},
				width:         100,
				height:        30,
				sessions:      []SessionRow{{Name: "alpha"}, {Name: "beta"}},
				serverWarning: tc.warning,
			}
			content := m.View().Content
			if got := lipgloss.Height(content); got != m.height {
				t.Fatalf("rendered view height = %d, want exactly %d (overflow is cropped at the bottom in Bubble Tea v2)", got, m.height)
			}
			lines := strings.Split(content, "\n")
			last := lines[len(lines)-1]
			if !strings.Contains(last, "q: quit") {
				t.Fatalf("last row must carry the keyboard shortcuts bar, got: %q", last)
			}
		})
	}
}
