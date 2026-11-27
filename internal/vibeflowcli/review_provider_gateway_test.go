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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// With the gateway off, gateway values saved in provider.env by an earlier
// gateway launch (or exported in the shell) must not send a review to the
// gateway, which refuses built-in models (#7704). A custom endpoint the user
// chose is kept, and gateway mode is untouched.
func TestReviewProviderEnv_GatewayOffDropsGatewayRouting(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ServerURL = "https://cloud.example.test"
	cfg.Providers["claude"] = Provider{Binary: "claude", Env: map[string]string{
		"ANTHROPIC_BASE_URL":       "https://cloud.example.test/rest/v1/llm-gateway",
		"ANTHROPIC_CUSTOM_HEADERS": "x-axiom-api-key: secret-gateway-token",
	}}
	cfg.Providers["codex"] = Provider{Binary: "codex", Env: map[string]string{"OPENAI_BASE_URL": "https://cloud.example.test/rest/v1/llm-gateway/v1"}}
	t.Setenv("GOOGLE_GEMINI_BASE_URL", "https://cloud.example.test/rest/v1/llm-gateway/") // from the shell

	env := reviewProviderEnv(cfg, "claude")
	if env["ANTHROPIC_BASE_URL"] != "" || env["ANTHROPIC_CUSTOM_HEADERS"] != "" {
		t.Errorf("gateway off, claude still routed to the gateway: base=%q headers=%q", env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_CUSTOM_HEADERS"])
	}
	if got := reviewProviderEnv(cfg, "codex")["OPENAI_BASE_URL"]; got != "" {
		t.Errorf("gateway off, codex still routed to the gateway: %q", got)
	}
	if got := reviewProviderEnv(cfg, "gemini")["GOOGLE_GEMINI_BASE_URL"]; got != "" {
		t.Errorf("gateway off, a shell gateway URL still reached gemini: %q", got)
	}

	// The user's own endpoint stays.
	cfg.Providers["claude"] = Provider{Binary: "claude", Env: map[string]string{"ANTHROPIC_BASE_URL": "https://llm.internal.test/anthropic", "ANTHROPIC_CUSTOM_HEADERS": "x-team: blue"}}
	if env := reviewProviderEnv(cfg, "claude"); env["ANTHROPIC_BASE_URL"] != "https://llm.internal.test/anthropic" || env["ANTHROPIC_CUSTOM_HEADERS"] != "x-team: blue" {
		t.Errorf("a custom endpoint was dropped: %v %v", env["ANTHROPIC_BASE_URL"], env["ANTHROPIC_CUSTOM_HEADERS"])
	}

	// Gateway mode keeps its values (the relay overrides them per attempt).
	cfg.LLMGatewayEnabled = true
	cfg.Providers["claude"] = Provider{Binary: "claude", Env: map[string]string{"ANTHROPIC_BASE_URL": "https://cloud.example.test/rest/v1/llm-gateway"}}
	if got := reviewProviderEnv(cfg, "claude")["ANTHROPIC_BASE_URL"]; got != "https://cloud.example.test/rest/v1/llm-gateway" {
		t.Errorf("gateway on: base URL changed to %q", got)
	}
}

// End to end: the claude a review launches really receives no gateway URL or
// header, as seen by the process itself.
func TestReviewProviderEnv_HarnessProcessSeesNoGateway(t *testing.T) {
	cfg := DefaultConfig()
	cfg.ServerURL = "https://cloud.example.test/"
	dir := t.TempDir()
	out := filepath.Join(dir, "env.txt")
	harness := filepath.Join(dir, "claude")
	if err := os.WriteFile(harness, []byte("#!/bin/sh\nenv > "+shellQuote(out)+"\n"), 0700); err != nil {
		t.Fatal(err)
	}
	cfg.Providers["claude"] = Provider{Binary: harness, Env: map[string]string{
		"ANTHROPIC_BASE_URL":       "https://cloud.example.test/rest/v1/llm-gateway",
		"ANTHROPIC_CUSTOM_HEADERS": "X-Axiom-Api-Key: secret-gateway-token",
	}}
	cmd := exec.Command(harness)
	cmd.Env = reviewEnvList(reviewProviderEnv(cfg, "claude"))
	if err := cmd.Run(); err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(out)
	if err != nil {
		t.Fatal(err)
	}
	if s := string(data); strings.Contains(s, "llm-gateway") || strings.Contains(s, "secret-gateway-token") {
		t.Errorf("the harness process received gateway routing:\n%s", s)
	}
}
