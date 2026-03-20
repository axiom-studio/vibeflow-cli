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
	"fmt"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// RestartSelectModel is a Bubble Tea sub-model that presents a multiselect
// list of dead sessions the user can choose to restart on CLI startup.
type RestartSelectModel struct {
	sessions []SessionMeta
	selected map[int]bool
	cursor   int
	done     bool
	skipped  bool
}

// NewRestartSelectModel creates a restart selector for the given dead sessions.
func NewRestartSelectModel(dead []SessionMeta) RestartSelectModel {
	return RestartSelectModel{
		sessions: dead,
		selected: make(map[int]bool),
		cursor:   0,
	}
}

// restartConfirmMsg signals that the user confirmed their restart selection.
type restartConfirmMsg struct {
	sessions []SessionMeta
}

// restartSkipMsg signals that the user skipped restart.
type restartSkipMsg struct{}

// Update handles input for the restart selector.
func (r RestartSelectModel) Update(msg tea.Msg) (RestartSelectModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if r.cursor > 0 {
				r.cursor--
			}
		case "down", "j":
			if r.cursor < len(r.sessions)-1 {
				r.cursor++
			}
		case " ":
			r.selected[r.cursor] = !r.selected[r.cursor]
		case "a":
			// Select all.
			allSelected := true
			for i := range r.sessions {
				if !r.selected[i] {
					allSelected = false
					break
				}
			}
			for i := range r.sessions {
				r.selected[i] = !allSelected
			}
		case "enter":
			var toRestart []SessionMeta
			for i, s := range r.sessions {
				if r.selected[i] {
					toRestart = append(toRestart, s)
				}
			}
			if len(toRestart) > 0 {
				r.done = true
				return r, func() tea.Msg { return restartConfirmMsg{sessions: toRestart} }
			}
			// No selection → treat as skip.
			r.skipped = true
			return r, func() tea.Msg { return restartSkipMsg{} }
		case "esc", "q":
			r.skipped = true
			return r, func() tea.Msg { return restartSkipMsg{} }
		}
	}
	return r, nil
}

// View renders the restart selector.
func (r RestartSelectModel) View() string {
	var b strings.Builder

	headerStyle := lipgloss.NewStyle().Bold(true).Foreground(warningColor)
	b.WriteString(headerStyle.Render("  Dead sessions detected — restart?"))
	b.WriteString("\n\n")

	for i, s := range r.sessions {
		cursor := "  "
		if i == r.cursor {
			cursor = "▸ "
		}

		check := "[ ]"
		if r.selected[i] {
			check = "[✓]"
		}

		// Format: [✓] session-name  provider | persona | branch | project
		name := s.Name
		if len(name) > 30 {
			name = name[:27] + "..."
		}

		details := s.Provider
		if s.Persona != "" {
			details += " | " + s.Persona
		}
		if s.Branch != "" {
			details += " | " + s.Branch
		}
		if s.Project != "" {
			details += " | " + s.Project
		}

		line := fmt.Sprintf("%s%s %s  %s", cursor, check, name, lipgloss.NewStyle().Foreground(dimColor).Render(details))
		if i == r.cursor {
			line = selectedStyle.Render(line)
		}
		b.WriteString(line)
		b.WriteString("\n")
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("  space: toggle • a: select all • enter: restart selected • esc: skip"))
	b.WriteString("\n")

	return b.String()
}
