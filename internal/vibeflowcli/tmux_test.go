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
)

func TestRenderLaunchCommand(t *testing.T) {
	t.Run("empty template", func(t *testing.T) {
		got, err := RenderLaunchCommand("", LaunchTemplateVars{Binary: "claude"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "" {
			t.Errorf("expected empty, got %q", got)
		}
	})

	t.Run("basic template", func(t *testing.T) {
		tmpl := "{{.Binary}} --some-flag"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{Binary: "claude"})
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		if got != "claude --some-flag" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("skip permissions true", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "claude",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "claude --dangerously-skip-permissions" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("skip permissions false", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --dangerously-skip-permissions{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "claude",
			SkipPermissions: false,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "claude" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("codex full-auto", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --full-auto{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "codex",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "codex --full-auto" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("gemini yolo", func(t *testing.T) {
		tmpl := "{{.Binary}}{{ if .SkipPermissions }} --yolo{{ end }}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:          "gemini",
			SkipPermissions: true,
		})
		if err != nil {
			t.Fatal(err)
		}
		if got != "gemini --yolo" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("all vars", func(t *testing.T) {
		tmpl := "{{.Binary}} --project={{.Project}} --branch={{.Branch}} --server={{.ServerURL}}"
		got, err := RenderLaunchCommand(tmpl, LaunchTemplateVars{
			Binary:    "agent",
			Project:   "my-project",
			Branch:    "dev",
			ServerURL: "http://localhost:7080",
		})
		if err != nil {
			t.Fatal(err)
		}
		expected := "agent --project=my-project --branch=dev --server=http://localhost:7080"
		if got != expected {
			t.Errorf("got %q, want %q", got, expected)
		}
	})

	t.Run("invalid template", func(t *testing.T) {
		_, err := RenderLaunchCommand("{{.Invalid}", LaunchTemplateVars{})
		if err == nil {
			t.Fatal("expected error for invalid template")
		}
	})
}

func TestFullSessionName(t *testing.T) {
	tm := &TmuxManager{socketName: "vibeflow"}

	t.Run("with provider", func(t *testing.T) {
		got := tm.FullSessionName("claude", "my-session")
		if got != "vibeflow_claude-my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("without provider", func(t *testing.T) {
		got := tm.FullSessionName("", "my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("codex provider", func(t *testing.T) {
		got := tm.FullSessionName("codex", "feature-branch")
		if got != "vibeflow_codex-feature-branch" {
			t.Errorf("got %q", got)
		}
	})
}

func TestParseSessionProvider(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{"claude session", "vibeflow_claude-my-session", "claude"},
		{"codex session", "vibeflow_codex-feature-123", "codex"},
		{"gemini session", "vibeflow_gemini-dev", "gemini"},
		{"no provider", "vibeflow_my-session", "my"},
		{"no prefix", "some-session", "some"},
		{"no dash", "vibeflow_nodash", ""},
		{"empty", "", ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := ParseSessionProvider(tc.input)
			if got != tc.expected {
				t.Errorf("ParseSessionProvider(%q) = %q, want %q", tc.input, got, tc.expected)
			}
		})
	}
}

func TestAtoi(t *testing.T) {
	tests := []struct {
		input    string
		expected int
	}{
		{"0", 0},
		{"1", 1},
		{"42", 42},
		{"100", 100},
		{"abc", 0},
		{"12abc34", 1234},
		{"", 0},
		{"3.14", 314},
	}

	for _, tc := range tests {
		t.Run(tc.input, func(t *testing.T) {
			got := atoi(tc.input)
			if got != tc.expected {
				t.Errorf("atoi(%q) = %d, want %d", tc.input, got, tc.expected)
			}
		})
	}
}

func TestLaunchTemplateVars_Fields(t *testing.T) {
	vars := LaunchTemplateVars{
		WorkDir:         "/work",
		Project:         "proj",
		Branch:          "main",
		ServerURL:       "http://localhost",
		SessionID:       "session-abc",
		SkipPermissions: true,
		Binary:          "claude",
	}

	// Verify all fields are accessible in templates.
	tmpl := "{{.WorkDir}} {{.Project}} {{.Branch}} {{.ServerURL}} {{.SessionID}} {{.SkipPermissions}} {{.Binary}}"
	got, err := RenderLaunchCommand(tmpl, vars)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(got, "/work") {
		t.Error("missing WorkDir")
	}
	if !strings.Contains(got, "session-abc") {
		t.Error("missing SessionID")
	}
	if !strings.Contains(got, "true") {
		t.Error("missing SkipPermissions")
	}
}

func TestEnsurePrefix(t *testing.T) {
	tm := &TmuxManager{socketName: "vibeflow"}

	t.Run("already prefixed", func(t *testing.T) {
		got := tm.ensurePrefix("vibeflow_my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("not prefixed", func(t *testing.T) {
		got := tm.ensurePrefix("my-session")
		if got != "vibeflow_my-session" {
			t.Errorf("got %q", got)
		}
	})
}

func TestNewTmuxManager_DefaultSocket(t *testing.T) {
	tm := NewTmuxManager("")
	if tm.socketName != "vibeflow" {
		t.Errorf("default socketName = %q, want vibeflow", tm.socketName)
	}
}

func TestNewTmuxManager_CustomSocket(t *testing.T) {
	tm := NewTmuxManager("custom")
	if tm.socketName != "custom" {
		t.Errorf("socketName = %q, want custom", tm.socketName)
	}
}
