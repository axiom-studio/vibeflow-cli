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

import "github.com/charmbracelet/lipgloss"

// PersonaColor returns the theme color for the given persona key.
// Returns a neutral gray for unknown keys.
func PersonaColor(key string) lipgloss.Color {
	if c, ok := personaColors[key]; ok {
		return c
	}
	return lipgloss.Color("#888888")
}

// PersonaLargeIcon returns the multi-line pixel art icon for the given persona.
// Each icon is 5 lines tall. Returns empty string for unknown keys.
func PersonaLargeIcon(key string) string {
	return personaLargeIcons[key]
}

// PersonaCompactIcon returns a small 1-2 character icon for the given persona.
// Suitable for inline display in session lists. Returns empty string for unknown keys.
func PersonaCompactIcon(key string) string {
	return personaCompactIcons[key]
}

// personaColors maps persona keys to their theme colors.
// Palette chosen for distinctness on dark backgrounds and harmony with providerColors.
var personaColors = map[string]lipgloss.Color{
	"principal_engineer": lipgloss.Color("#ffd700"), // gold — royalty/mastery
	"developer":          lipgloss.Color("#61afef"), // soft blue — code/tech
	"architect":          lipgloss.Color("#c678dd"), // purple — design/wisdom
	"ux_designer":     lipgloss.Color("#e88fd2"), // pink — design/creativity
	"qa_lead":         lipgloss.Color("#98c379"), // green — verification
	"security_lead":   lipgloss.Color("#e06c75"), // red — security/alerts
	"product_manager": lipgloss.Color("#e5c07b"), // gold — innovation
	"project_manager": lipgloss.Color("#d19a66"), // orange — organization
	"customer":        lipgloss.Color("#56b6c2"), // teal — communication
}

// personaCompactIcons maps persona keys to small Unicode glyphs for inline display.
var personaCompactIcons = map[string]string{
	"principal_engineer": "♛",
	"developer":          "⟨⟩",
	"architect":          "△",
	"ux_designer":     "✎",
	"qa_lead":         "◎",
	"security_lead":   "◆",
	"product_manager": "✦",
	"project_manager": "☰",
	"customer":        "◈",
}

// personaLargeIcons maps persona keys to 5-line pixel art icons using Unicode block characters.
// Icons use █ ▀ ▄ ▌ ▐ for pixel art. Callers apply color via PersonaColor().
var personaLargeIcons = map[string]string{
	// Principal Engineer — crown
	"principal_engineer": "" +
		" █ ▄██▄ █ \n" +
		" █▄████▄█ \n" +
		"  ████████\n" +
		"  ██▀▀██  \n" +
		"   ▀██▀   ",

	// Developer — terminal/monitor with command prompt
	"developer": "" +
		"▄████████▄\n" +
		"█ ▸_     █\n" +
		"█ █▌▀▀▀  █\n" +
		"▀████████▀\n" +
		"   ▀██▀   ",

	// Architect — classical temple with columns
	"architect": "" +
		" ▄██████▄ \n" +
		"▀████████▀\n" +
		"  ▐▌  ▐▌  \n" +
		"  ▐▌  ▐▌  \n" +
		" ▀██████▀ ",

	// UX Designer — pen/stylus over artboard
	"ux_designer": "" +
		"      ▄██▄\n" +
		"    ▄█▀▀  \n" +
		"  ▄█▀     \n" +
		" █▀       \n" +
		"▀▄▄██████▀",

	// QA Lead — magnifying glass over checkmark
	"qa_lead": "" +
		"  ▄████▄  \n" +
		" █  ▄▀  █ \n" +
		" █ ▀    █ \n" +
		"  ▀████▀▄ \n" +
		"        ▀ ",

	// Security Lead — shield with keyhole
	"security_lead": "" +
		" ▄██████▄ \n" +
		" ██  ▄▄ ██\n" +
		" ██  ▀▀ ██\n" +
		"  ██▄▄██  \n" +
		"    ▀▀    ",

	// Product Manager — lightbulb (ideas)
	"product_manager": "" +
		"   ▄██▄   \n" +
		"  █▀  ▀█  \n" +
		"  █▄  ▄█  \n" +
		"   ▐██▌   \n" +
		"    ▀▀    ",

	// Project Manager — clipboard with checklist
	"project_manager": "" +
		"  ▄▀██▀▄  \n" +
		" ████████ \n" +
		" █ █▀▀▀ █ \n" +
		" █ █▀▀▀ █ \n" +
		" ▀██████▀ ",

	// Customer — speech bubble
	"customer": "" +
		" ▄██████▄ \n" +
		"██ ▀▀▀▀ ██\n" +
		"██       █\n" +
		" ▀██████▀ \n" +
		"▄▀        ",
}
