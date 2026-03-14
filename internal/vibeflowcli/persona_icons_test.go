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

var allPersonaKeys = []string{
	"developer", "architect", "ux_designer", "qa_lead", "security_lead",
	"product_manager", "project_manager", "customer",
}

func TestPersonaLargeIcon_AllKeysReturnNonEmpty(t *testing.T) {
	for _, key := range allPersonaKeys {
		icon := PersonaLargeIcon(key)
		if icon == "" {
			t.Errorf("PersonaLargeIcon(%q) returned empty string", key)
		}
	}
}

func TestPersonaLargeIcon_UnknownKeyReturnsEmpty(t *testing.T) {
	icon := PersonaLargeIcon("nonexistent")
	if icon != "" {
		t.Errorf("PersonaLargeIcon(\"nonexistent\") = %q, want empty", icon)
	}
}

func TestPersonaLargeIcon_ConsistentLineCount(t *testing.T) {
	var expectedLines int
	for i, key := range allPersonaKeys {
		icon := PersonaLargeIcon(key)
		lines := strings.Split(icon, "\n")
		if i == 0 {
			expectedLines = len(lines)
		}
		if len(lines) != expectedLines {
			t.Errorf("PersonaLargeIcon(%q) has %d lines, expected %d", key, len(lines), expectedLines)
		}
	}
}

func TestPersonaLargeIcon_FiveLinesTall(t *testing.T) {
	for _, key := range allPersonaKeys {
		icon := PersonaLargeIcon(key)
		lines := strings.Split(icon, "\n")
		if len(lines) != 5 {
			t.Errorf("PersonaLargeIcon(%q) has %d lines, want 5", key, len(lines))
		}
	}
}

func TestPersonaCompactIcon_AllKeysReturnNonEmpty(t *testing.T) {
	for _, key := range allPersonaKeys {
		icon := PersonaCompactIcon(key)
		if icon == "" {
			t.Errorf("PersonaCompactIcon(%q) returned empty string", key)
		}
	}
}

func TestPersonaCompactIcon_UnknownKeyReturnsEmpty(t *testing.T) {
	icon := PersonaCompactIcon("nonexistent")
	if icon != "" {
		t.Errorf("PersonaCompactIcon(\"nonexistent\") = %q, want empty", icon)
	}
}

func TestPersonaColor_AllKeysReturnDistinctColors(t *testing.T) {
	seen := make(map[lipgloss.Color]string)
	for _, key := range allPersonaKeys {
		color := PersonaColor(key)
		if prev, ok := seen[color]; ok {
			t.Errorf("PersonaColor(%q) and PersonaColor(%q) both return %v", key, prev, color)
		}
		seen[color] = key
	}
}

func TestPersonaColor_UnknownKeyReturnsFallback(t *testing.T) {
	color := PersonaColor("nonexistent")
	expected := lipgloss.Color("#888888")
	if color != expected {
		t.Errorf("PersonaColor(\"nonexistent\") = %v, want %v", color, expected)
	}
}

func TestPersonaColor_AllKeysReturnValidHexColor(t *testing.T) {
	for _, key := range allPersonaKeys {
		color := PersonaColor(key)
		s := string(color)
		if len(s) != 7 || s[0] != '#' {
			t.Errorf("PersonaColor(%q) = %q, want #RRGGBB format", key, s)
		}
	}
}

func TestPersonaCompactIcon_AllDistinct(t *testing.T) {
	seen := make(map[string]string)
	for _, key := range allPersonaKeys {
		icon := PersonaCompactIcon(key)
		if prev, ok := seen[icon]; ok {
			t.Errorf("PersonaCompactIcon(%q) and PersonaCompactIcon(%q) both return %q", key, prev, icon)
		}
		seen[icon] = key
	}
}

func TestPersonaLargeIcon_NoEmptyLines(t *testing.T) {
	for _, key := range allPersonaKeys {
		icon := PersonaLargeIcon(key)
		for i, line := range strings.Split(icon, "\n") {
			if strings.TrimSpace(line) == "" {
				t.Errorf("PersonaLargeIcon(%q) has empty line at index %d", key, i)
			}
		}
	}
}
