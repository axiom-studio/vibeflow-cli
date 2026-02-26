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
	"path/filepath"
	"strings"

	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// WorktreeRow is a single worktree entry displayed in the TUI.
type WorktreeRow struct {
	Path    string
	Branch  string
	Session string // Name of the session using this worktree, or "".
	Status  string // "active" or "orphaned".
}

// WorktreeListModel is a Bubble Tea sub-model for listing and managing
// git worktrees.
type WorktreeListModel struct {
	rows      []WorktreeRow
	cursor    int
	done      bool
	deleted   bool // set when a delete occurred (triggers refresh)
	deletedWt string
}

// NewWorktreeListModel creates a worktree list from live data.
func NewWorktreeListModel(wm *WorktreeManager, store *Store) WorktreeListModel {
	var rows []WorktreeRow

	if wm == nil {
		return WorktreeListModel{}
	}

	wts, err := wm.List()
	if err != nil {
		return WorktreeListModel{}
	}

	// Build lookup: worktree path → session name.
	sessionByPath := make(map[string]string)
	if store != nil {
		if metas, err := store.List(); err == nil {
			for _, m := range metas {
				if m.WorktreePath != "" {
					abs, _ := filepath.Abs(m.WorktreePath)
					sessionByPath[abs] = m.Name
				}
			}
		}
	}

	for _, wt := range wts {
		abs, _ := filepath.Abs(wt.Path)
		session := ""
		status := "orphaned"
		if name, ok := sessionByPath[abs]; ok {
			session = name
			status = "active"
		}
		branch := wt.Branch
		if wt.Detached {
			branch = "(detached)"
		}
		if wt.Bare {
			continue // skip bare repo entry
		}
		rows = append(rows, WorktreeRow{
			Path:    wt.Path,
			Branch:  branch,
			Session: session,
			Status:  status,
		})
	}

	return WorktreeListModel{rows: rows}
}

// Done returns true when the user is done with the worktree view.
func (wl WorktreeListModel) Done() bool { return wl.done }

// Deleted returns true if a worktree was deleted (triggers main refresh).
func (wl WorktreeListModel) Deleted() bool { return wl.deleted }

// DeletedPath returns the worktree path that was deleted.
func (wl WorktreeListModel) DeletedPath() string { return wl.deletedWt }

// Update handles input for the worktree list.
func (wl WorktreeListModel) Update(msg tea.Msg) (WorktreeListModel, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.KeyMsg:
		switch msg.String() {
		case "up", "k":
			if wl.cursor > 0 {
				wl.cursor--
			}
		case "down", "j":
			if wl.cursor < len(wl.rows)-1 {
				wl.cursor++
			}
		case "d":
			if wl.cursor < len(wl.rows) {
				row := wl.rows[wl.cursor]
				if row.Status == "orphaned" {
					wl.deleted = true
					wl.deletedWt = row.Path
				}
				// Active worktrees can't be deleted from here — kill session first.
			}
		case "esc":
			wl.done = true
		}
	}
	return wl, nil
}

// View renders the worktree list.
func (wl WorktreeListModel) View() string {
	var b strings.Builder

	title := lipgloss.NewStyle().Bold(true).Foreground(accentColor)
	b.WriteString(title.Render("Git Worktrees"))
	b.WriteString("\n\n")

	if len(wl.rows) == 0 {
		b.WriteString(lipgloss.NewStyle().Foreground(dimColor).Render("No worktrees found."))
		b.WriteString("\n")
	} else {
		// Header
		header := fmt.Sprintf("  %-40s %-16s %-20s %-10s", "PATH", "BRANCH", "SESSION", "STATUS")
		b.WriteString(lipgloss.NewStyle().Bold(true).Foreground(dimColor).Render(header))
		b.WriteString("\n")

		for i, row := range wl.rows {
			cursor := "  "
			style := lipgloss.NewStyle()
			if i == wl.cursor {
				cursor = "> "
				style = selectedStyle
			}

			session := row.Session
			if session == "" {
				session = "(none)"
			}

			statusStyle := lipgloss.NewStyle().Foreground(dimColor)
			if row.Status == "active" {
				statusStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("#00ff00"))
			} else {
				statusStyle = lipgloss.NewStyle().Foreground(warningColor)
			}

			line := fmt.Sprintf("%s%-40s %-16s %-20s %s",
				cursor,
				truncate(row.Path, 40),
				truncate(row.Branch, 16),
				truncate(session, 20),
				statusStyle.Render(row.Status),
			)
			b.WriteString(style.Render(line))
			b.WriteString("\n")
		}
	}

	b.WriteString("\n")
	b.WriteString(helpStyle.Render("d: delete orphaned  j/k: navigate  esc: back"))

	return b.String()
}
