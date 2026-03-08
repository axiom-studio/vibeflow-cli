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
)

func TestNewWizardModel_PreselectsDeveloper(t *testing.T) {
	cfg := DefaultConfig()
	reg := NewProviderRegistry(cfg)
	wm := NewWizardModel(reg, ".", nil, nil, "", nil, cfg)

	if !wm.selectedPersonas[0] {
		t.Error("selectedPersonas[0] (developer) should be pre-selected")
	}
	for i := 1; i < len(wm.personas); i++ {
		if wm.selectedPersonas[i] {
			t.Errorf("selectedPersonas[%d] (%s) should not be pre-selected", i, wm.personas[i].key)
		}
	}
}

func TestSelectedPersonas_ToggleOnOff(t *testing.T) {
	sp := map[int]bool{0: true}

	// Toggle architect on.
	sp[1] = !sp[1]
	if !sp[1] {
		t.Error("after toggle on, selectedPersonas[1] should be true")
	}

	// Toggle architect off.
	sp[1] = !sp[1]
	if sp[1] {
		t.Error("after toggle off, selectedPersonas[1] should be false")
	}

	// Toggle developer off.
	sp[0] = !sp[0]
	if sp[0] {
		t.Error("after toggle off, selectedPersonas[0] should be false")
	}
}

func TestSelectedPersonas_CountSelected(t *testing.T) {
	sp := map[int]bool{0: true, 2: true, 4: true}
	count := 0
	for _, on := range sp {
		if on {
			count++
		}
	}
	if count != 3 {
		t.Errorf("count = %d, want 3", count)
	}
}

func TestSelectedPersonas_EmptyBlocksAdvance(t *testing.T) {
	sp := map[int]bool{}
	count := 0
	for _, on := range sp {
		if on {
			count++
		}
	}
	if count != 0 {
		t.Errorf("empty map should have count 0, got %d", count)
	}
}

func TestWizardResult_PersonasField(t *testing.T) {
	personas := defaultPersonas()
	selectedPersonas := map[int]bool{0: true, 1: true, 3: true}

	var result []string
	for i := 0; i < len(personas); i++ {
		if selectedPersonas[i] {
			result = append(result, personas[i].key)
		}
	}

	if len(result) != 3 {
		t.Fatalf("len(Personas) = %d, want 3", len(result))
	}
	expected := []string{"developer", "architect", "security_lead"}
	for i, want := range expected {
		if result[i] != want {
			t.Errorf("Personas[%d] = %q, want %q", i, result[i], want)
		}
	}
}

func TestWizardResult_PersonaBackwardCompat(t *testing.T) {
	// First selected persona should become the primary Persona field.
	personas := defaultPersonas()
	selectedPersonas := map[int]bool{2: true, 4: true} // qa_lead and product_manager

	selectedPersona := -1
	for i := 0; i < len(personas); i++ {
		if selectedPersonas[i] {
			if selectedPersona < 0 {
				selectedPersona = i
			}
		}
	}

	if selectedPersona != 2 {
		t.Errorf("selectedPersona = %d, want 2 (qa_lead)", selectedPersona)
	}
	if personas[selectedPersona].key != "qa_lead" {
		t.Errorf("primary persona = %q, want qa_lead", personas[selectedPersona].key)
	}
}

func TestDefaultPersonas_SevenEntries(t *testing.T) {
	personas := defaultPersonas()
	if len(personas) != 7 {
		t.Errorf("len(defaultPersonas()) = %d, want 7", len(personas))
	}
}

func TestDefaultPersonas_UniqueKeys(t *testing.T) {
	personas := defaultPersonas()
	seen := make(map[string]bool)
	for _, p := range personas {
		if seen[p.key] {
			t.Errorf("duplicate persona key: %q", p.key)
		}
		seen[p.key] = true
	}
}

func TestDefaultPersonas_DeveloperFirst(t *testing.T) {
	personas := defaultPersonas()
	if personas[0].key != "developer" {
		t.Errorf("first persona key = %q, want developer", personas[0].key)
	}
}
