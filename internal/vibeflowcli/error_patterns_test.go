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
)

func TestNewErrorPatternRegistry(t *testing.T) {
	reg := NewErrorPatternRegistry()
	if reg == nil {
		t.Fatal("expected non-nil registry")
	}
	if len(reg.patterns) == 0 {
		t.Error("expected default patterns to be loaded")
	}
}

func TestDefaultPatterns(t *testing.T) {
	patterns := DefaultPatterns()
	if len(patterns) < 10 {
		t.Errorf("expected at least 10 default patterns, got %d", len(patterns))
	}

	// Verify we have patterns for all providers.
	providers := make(map[string]bool)
	for _, p := range patterns {
		providers[p.Provider] = true
	}
	for _, expected := range []string{"claude", "codex", "gemini", "*"} {
		if !providers[expected] {
			t.Errorf("missing patterns for provider %q", expected)
		}
	}
}

func TestErrorPatternRegistry_Match_ClaudeAPI5xx(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "Some output\nAPI Error: 500\nmore text")
	if match == nil {
		t.Fatal("expected match for Claude API 5xx")
	}
	if match.Severity != SeverityRecoverable {
		t.Error("expected recoverable severity")
	}
	if match.RecoveryMessage == "" {
		t.Error("expected non-empty recovery message")
	}
}

func TestErrorPatternRegistry_Match_ClaudeRateLimit(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "API Error: 429 Too Many Requests")
	if match == nil {
		t.Fatal("expected match for rate limit")
	}
	if !match.RequiresBackoff {
		t.Error("rate limit should require backoff")
	}
}

func TestErrorPatternRegistry_Match_ClaudeOverloaded(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "API Error: 529")
	if match == nil {
		t.Fatal("expected match for 529")
	}
	// 529 matches the 5xx pattern (first in order); both are recoverable.
	if match.Severity != SeverityRecoverable {
		t.Error("expected recoverable severity")
	}
}

func TestErrorPatternRegistry_Match_ClaudeConnectionRefused(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "Error: connection refused")
	if match == nil {
		t.Fatal("expected match for connection refused")
	}
}

func TestErrorPatternRegistry_Match_ClaudeTimeout(t *testing.T) {
	reg := NewErrorPatternRegistry()
	for _, output := range []string{
		"ETIMEDOUT connecting to server",
		"request timed out",
		"Error: time out reached",
	} {
		match := reg.Match("claude", output)
		if match == nil {
			t.Errorf("expected match for timeout pattern: %q", output)
		}
	}
}

func TestErrorPatternRegistry_Match_CodexAPIError(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("codex", "OpenAI API error: something went wrong")
	if match == nil {
		t.Fatal("expected match for Codex API error")
	}
}

func TestErrorPatternRegistry_Match_CodexRateLimit(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("codex", "Error: rate limit exceeded")
	if match == nil {
		t.Fatal("expected match for Codex rate limit")
	}
	if !match.RequiresBackoff {
		t.Error("rate limit should require backoff")
	}
}

func TestErrorPatternRegistry_Match_GeminiResourceExhausted(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("gemini", "Error: RESOURCE_EXHAUSTED: quota exceeded")
	if match == nil {
		t.Fatal("expected match for Gemini resource exhausted")
	}
	if !match.RequiresBackoff {
		t.Error("resource exhausted should require backoff")
	}
}

func TestErrorPatternRegistry_Match_GeminiInternalError(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("gemini", "INTERNAL server error occurred")
	if match == nil {
		t.Fatal("expected match for Gemini internal error")
	}
}

func TestErrorPatternRegistry_Match_UniversalPanic(t *testing.T) {
	reg := NewErrorPatternRegistry()
	// Universal patterns should match any provider.
	for _, provider := range []string{"claude", "codex", "gemini"} {
		match := reg.Match(provider, "panic: runtime error: index out of range")
		if match == nil {
			t.Errorf("expected panic match for provider %q", provider)
			continue
		}
		if match.Severity != SeverityFatal {
			t.Errorf("panic should be fatal for provider %q", provider)
		}
	}
}

func TestErrorPatternRegistry_Match_UniversalFatalError(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "fatal error: all goroutines are asleep")
	if match == nil {
		t.Fatal("expected match for fatal error")
	}
	if match.Severity != SeverityFatal {
		t.Error("expected fatal severity")
	}
}

func TestErrorPatternRegistry_Match_NoMatch(t *testing.T) {
	reg := NewErrorPatternRegistry()
	match := reg.Match("claude", "Everything is working fine.\nNo errors here.")
	if match != nil {
		t.Errorf("expected no match, got: %s", match.Description)
	}
}

func TestErrorPatternRegistry_Match_ProviderIsolation(t *testing.T) {
	reg := NewErrorPatternRegistry()
	// Codex-specific pattern should not match for claude.
	match := reg.Match("claude", "OpenAI API error: something")
	if match != nil {
		t.Error("Codex pattern should not match for claude provider")
	}
}

func TestErrorPatternRegistry_AddPattern(t *testing.T) {
	reg := NewErrorPatternRegistry()
	custom := ErrorPattern{
		Provider:        "custom",
		Regex:           regexp.MustCompile(`CUSTOM_ERROR`),
		Severity:        SeverityRecoverable,
		RecoveryMessage: "Custom recovery",
		Description:     "Custom error",
	}
	reg.AddPattern(custom)

	match := reg.Match("custom", "CUSTOM_ERROR occurred")
	if match == nil {
		t.Fatal("expected match for custom pattern")
	}
	if match.Description != "Custom error" {
		t.Errorf("Description = %q", match.Description)
	}
}

func TestLastNLines(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		n        int
		expected string
	}{
		{"fewer than n", "a\nb\nc", 5, "a\nb\nc"},
		{"exactly n", "a\nb\nc", 3, "a\nb\nc"},
		{"more than n", "a\nb\nc\nd\ne", 3, "c\nd\ne"},
		{"single line", "single", 3, "single"},
		{"empty", "", 3, ""},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := lastNLines(tc.input, tc.n)
			if got != tc.expected {
				t.Errorf("lastNLines(%q, %d) = %q, want %q", tc.input, tc.n, got, tc.expected)
			}
		})
	}
}

func TestTruncateLog(t *testing.T) {
	tests := []struct {
		input    string
		max      int
		expected string
	}{
		{"short", 10, "short"},
		{"exactly10!", 10, "exactly10!"},
		{"this is too long", 10, "this is..."},
		{"", 5, ""},
	}

	for _, tc := range tests {
		got := truncateLog(tc.input, tc.max)
		if got != tc.expected {
			t.Errorf("truncateLog(%q, %d) = %q, want %q", tc.input, tc.max, got, tc.expected)
		}
	}
}
