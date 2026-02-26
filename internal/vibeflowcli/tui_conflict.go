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

// ConflictAction is the user's choice from the conflict modal.
type ConflictAction int

const (
	ConflictSwitch   ConflictAction = iota // Attach to existing session.
	ConflictWorktree                       // Launch in a new worktree instead.
	ConflictCleanup                        // Clean up stale session and proceed.
	ConflictCancel                         // Return to main view.
)

// ConflictModal is a Bubble Tea sub-model that displays when a session
// conflict is detected in the target directory.
type ConflictModal struct {
	conflict ConflictResult
	options  []conflictOption
	cursor   int
	done     bool
	action   ConflictAction
}

type conflictOption struct {
	key    string
	label  string
	action ConflictAction
}

// NewConflictModal creates a modal for the given conflict result.
func NewConflictModal(conflict ConflictResult) ConflictModal {
	var opts []conflictOption

	switch conflict.Status {
	case ActiveConflict:
		opts = []conflictOption{
			{key: "s", label: "Switch to existing session", action: ConflictSwitch},
			{key: "w", label: "Launch in a new worktree", action: ConflictWorktree},
			{key: "c", label: "Cancel", action: ConflictCancel},
		}
	case ExternalConflict:
		opts = []conflictOption{
			{key: "p", label: "Take over (reuse session ID)", action: ConflictCleanup},
			{key: "w", label: "Launch in a new worktree", action: ConflictWorktree},
			{key: "c", label: "Cancel", action: ConflictCancel},
		}
	default:
		// StaleConflict
		opts = []conflictOption{
			{key: "p", label: "Clean up and proceed", action: ConflictCleanup},
			{key: "c", label: "Cancel", action: ConflictCancel},
		}
	}

	return ConflictModal{
		conflict: conflict,
		options:  opts,
	}
}

// Done returns true when the user has made a selection.
func (cm ConflictModal) Done() bool { return cm.done }

// Action returns the selected action.
func (cm ConflictModal) Action() ConflictAction { return cm.action }

// Conflict returns the underlying conflict result.
func (cm ConflictModal) Conflict() ConflictResult { return cm.conflict }

// Update handles input for the conflict modal.
func (cm ConflictModal) Update(msg tea.Msg) (ConflictModal, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if cm.cursor > 0 {
				cm.cursor--
			}
		case "down", "j":
			if cm.cursor < len(cm.options)-1 {
				cm.cursor++
			}
		case "enter":
			cm.action = cm.options[cm.cursor].action
			cm.done = true
		case "esc":
			cm.action = ConflictCancel
			cm.done = true
		default:
			// Check for shortcut keys (s/w/c/p).
			for _, opt := range cm.options {
				if msg.String() == opt.key {
					cm.action = opt.action
					cm.done = true
					break
				}
			}
		}
	}
	return cm, nil
}

// View renders the conflict modal.
func (cm ConflictModal) View() string {
	var b strings.Builder

	// Title
	titleStyle := lipgloss.NewStyle().Bold(true).Foreground(warningColor)
	b.WriteString(titleStyle.Render("Session Conflict Detected"))
	b.WriteString("\n\n")

	// Conflict details
	var statusLabel string
	var statusColor lipgloss.Color
	switch cm.conflict.Status {
	case ActiveConflict:
		statusLabel = "Running"
		statusColor = errorColor
	case ExternalConflict:
		statusLabel = "External (not managed by TUI)"
		statusColor = warningColor
	default:
		statusLabel = "Stale (not running)"
		statusColor = warningColor
	}

	b.WriteString(fmt.Sprintf("  Session:  %s\n", cm.conflict.SessionID))
	b.WriteString(fmt.Sprintf("  Provider: %s\n", cm.conflict.Provider))
	b.WriteString(fmt.Sprintf("  Status:   %s\n",
		lipgloss.NewStyle().Foreground(statusColor).Render(statusLabel)))
	b.WriteString(fmt.Sprintf("  File:     %s\n", cm.conflict.FilePath))
	b.WriteString("\n")

	// Options
	for i, opt := range cm.options {
		cursor := "  "
		if i == cm.cursor {
			cursor = "> "
		}
		b.WriteString(fmt.Sprintf("%s[%s] %s\n", cursor, opt.key, opt.label))
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("j/k: navigate  enter: select  esc: cancel"))

	return b.String()
}
