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

// Bubble Tea v2 delivers bracketed paste as tea.PasteMsg instead of a
// multi-rune key message (the v1 behavior). These tests prove the paste
// translation in SetupModel.Update and WizardModel.Update keeps text inputs
// paste-capable after the v2 migration.

func TestSetupModel_PasteIntoURLInput(t *testing.T) {
	m := NewSetupModel(&Config{}, "")
	updated, _ := m.Update(tea.PasteMsg{Content: "https://example.com"})
	got := updated.(SetupModel)
	if got.urlInput != "https://example.com" {
		t.Fatalf("urlInput = %q, want pasted URL", got.urlInput)
	}
}

func TestWizardModel_PasteIntoWorkDirInput(t *testing.T) {
	w := WizardModel{editingWorkDir: true}
	w2, _ := w.Update(tea.PasteMsg{Content: "/tmp/project"})
	if w2.workDirInput != "/tmp/project" {
		t.Fatalf("workDirInput = %q, want pasted path", w2.workDirInput)
	}
}
