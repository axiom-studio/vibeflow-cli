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
	"regexp"
	"testing"

	"github.com/charmbracelet/lipgloss"
)

func TestOceanPalette_ValidDistinctHex(t *testing.T) {
	hexRe := regexp.MustCompile(`^#[0-9a-fA-F]{6}$`)
	palette := map[string]lipgloss.Color{
		"background": oceanBackground,
		"foreground": oceanForeground,
		"primary":    oceanPrimary,
		"secondary":  oceanSecondary,
		"accent":     oceanAccent,
		"success":    oceanSuccess,
		"warning":    oceanWarning,
		"error":      oceanError,
		"muted":      oceanMuted,
		"surface":    oceanSurface,
		"shallow":    oceanShallow,
		"abyss":      oceanAbyss,
	}
	seen := map[string]string{}
	for name, c := range palette {
		s := string(c)
		if !hexRe.MatchString(s) {
			t.Errorf("ocean %s = %q, want #RRGGBB", name, s)
		}
		if prev, ok := seen[s]; ok {
			t.Errorf("ocean %s and %s share color %s", name, prev, s)
		}
		seen[s] = name
	}
}

// TestOceanHexConstantsMatchColors keeps the raw-hex string constants (used by
// tmux format strings) in lockstep with the lipgloss colors — a drift would
// silently theme the tmux chrome differently from the Go-side TUI.
func TestOceanHexConstantsMatchColors(t *testing.T) {
	pairs := []struct {
		hex   string
		color lipgloss.Color
	}{
		{oceanHexBackground, oceanBackground},
		{oceanHexForeground, oceanForeground},
		{oceanHexPrimary, oceanPrimary},
		{oceanHexSecondary, oceanSecondary},
		{oceanHexAccent, oceanAccent},
		{oceanHexSuccess, oceanSuccess},
		{oceanHexWarning, oceanWarning},
		{oceanHexError, oceanError},
		{oceanHexMuted, oceanMuted},
		{oceanHexSurface, oceanSurface},
		{oceanHexShallow, oceanShallow},
		{oceanHexAbyss, oceanAbyss},
	}
	for _, p := range pairs {
		if string(p.color) != p.hex {
			t.Errorf("hex const %q != color %q", p.hex, string(p.color))
		}
	}
}

func TestOceanIcons_NonEmptyDistinct(t *testing.T) {
	icons := []string{iconActive, iconInactive, iconSuccess, iconError, iconWarning, iconInfo, iconArrow, iconBullet}
	seen := map[string]bool{}
	for _, ic := range icons {
		if ic == "" {
			t.Error("ocean icon is empty")
		}
		if seen[ic] {
			t.Errorf("duplicate ocean icon %q", ic)
		}
		seen[ic] = true
	}
	if iconActive != "◆" || iconInactive != "◇" {
		t.Errorf("selection icons = %q/%q, want ◆/◇", iconActive, iconInactive)
	}
}

func TestOceanBorder_Rounded(t *testing.T) {
	if got := oceanBorder().TopLeft; got != "╭" {
		t.Errorf("oceanBorder top-left = %q, want ╭ (rounded)", got)
	}
}
