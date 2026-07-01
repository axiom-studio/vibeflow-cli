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

// Ocean — TUI design system (design_system document #401).
//
// "Calm, deep, focused. The serenity of ocean depths." A deep-ocean dark
// palette with rounded borders, diamond selection icons, and generous spacing.
// This file is the single source of truth for TUI styling — render code must
// consume these tokens rather than hard-coding hex values.

// Ocean palette as raw hex strings. Used directly by tmux format strings
// (which take `fg=#rrggbb` literals) and as the backing values for the
// lipgloss colors below.
const (
	oceanHexBackground = "#0b1929" // deep ocean — app background
	oceanHexForeground = "#c8d6e5" // soft blue-white — body text
	oceanHexPrimary    = "#00d4aa" // vibeflow teal — main accent
	oceanHexSecondary  = "#0abde3" // deeper blue — supporting accent
	oceanHexAccent     = "#55efc4" // sea foam green — data/values
	oceanHexSuccess    = "#00d2d3" // teal — success
	oceanHexWarning    = "#feca57" // sandy yellow — warning
	oceanHexError      = "#ff6b6b" // coral red — error
	oceanHexMuted      = "#576574" // storm gray — captions, dim chrome
	oceanHexSurface    = "#152d45" // deeper blue surface — panels
	oceanHexShallow    = "#1e3a5f" // active surface / hover
	oceanHexAbyss      = "#060f1a" // deepest background / overlays
)

// Ocean palette as lipgloss colors, for Go-side (Bubble Tea / Lip Gloss) styling.
var (
	oceanBackground = lipgloss.Color(oceanHexBackground)
	oceanForeground = lipgloss.Color(oceanHexForeground)
	oceanPrimary    = lipgloss.Color(oceanHexPrimary)
	oceanSecondary  = lipgloss.Color(oceanHexSecondary)
	oceanAccent     = lipgloss.Color(oceanHexAccent)
	oceanSuccess    = lipgloss.Color(oceanHexSuccess)
	oceanWarning    = lipgloss.Color(oceanHexWarning)
	oceanError      = lipgloss.Color(oceanHexError)
	oceanMuted      = lipgloss.Color(oceanHexMuted)
	oceanSurface    = lipgloss.Color(oceanHexSurface)
	oceanShallow    = lipgloss.Color(oceanHexShallow)
	oceanAbyss      = lipgloss.Color(oceanHexAbyss)
)

// Ocean selection, status, and separator icons (doc #401 §7). Soft, rounded
// glyphs — no sharp edges.
const (
	iconActive   = "◆" // selected / active
	iconInactive = "◇" // unselected / paused
	iconSuccess  = "✓"
	iconError    = "✗"
	iconWarning  = "△"
	iconInfo     = "○"
	iconArrow    = "→"
	iconBullet   = "·"
)

// oceanBorder is the rounded border that defines the Ocean theme (doc #401 §4).
func oceanBorder() lipgloss.Border { return lipgloss.RoundedBorder() }
