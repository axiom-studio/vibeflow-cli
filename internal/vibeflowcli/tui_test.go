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

// detailPanelModel builds a minimal Model with a single session row selected,
// sufficient to exercise renderDetailPanel.
func detailPanelModel(row SessionRow, cfg *Config) Model {
	return Model{
		config:   cfg,
		sessions: []SessionRow{row},
	}
}

func TestRenderDetailPanel_GatewayEnv_ClaudeMasksSecrets(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "claude",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(120, 40), "")

	if !strings.Contains(out, "Gateway") || !strings.Contains(out, "enabled") {
		t.Errorf("detail panel missing Gateway enabled row:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway") {
		t.Errorf("detail panel missing non-secret base URL value:\n%s", out)
	}
	if !strings.Contains(out, "ANTHROPIC_CUSTOM_HEADERS=<redacted>") {
		t.Errorf("detail panel must mask ANTHROPIC_CUSTOM_HEADERS:\n%s", out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestRenderDetailPanel_GatewayEnv_QwenMasksSecrets(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "qwen",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(200, 40), "")

	if !strings.Contains(out, "OPENAI_API_KEY=<redacted>") {
		t.Errorf("detail panel must mask OPENAI_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "OPENAI_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway/v1") {
		t.Errorf("detail panel missing non-secret base URL value:\n%s", out)
	}
	qwenKey := QwenCustomAPIKeyEnvName("OPENAI", "https://cloud.example.com/rest/v1/llm-gateway/v1")
	if !strings.Contains(out, qwenKey+"=<redacted>") {
		t.Errorf("detail panel must mask %s:\n%s", qwenKey, out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestRenderDetailPanel_GatewayEnv_GeminiUsesGeminiVars(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:              "s1",
		Provider:          "gemini",
		LLMGatewayEnabled: true,
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(200, 40), "")

	if !strings.Contains(out, "GEMINI_API_KEY=<redacted>") {
		t.Errorf("detail panel must mask GEMINI_API_KEY:\n%s", out)
	}
	if !strings.Contains(out, "GOOGLE_GEMINI_BASE_URL=https://cloud.example.com/rest/v1/llm-gateway") {
		t.Errorf("detail panel missing Gemini gateway base URL:\n%s", out)
	}
	if strings.Contains(out, "OPENAI_API_KEY=") || strings.Contains(out, "OPENAI_BASE_URL=") {
		t.Errorf("gemini gateway env must not display OPENAI_* vars:\n%s", out)
	}
	if strings.Contains(out, "secret-jwt-token") {
		t.Errorf("detail panel leaked the API token:\n%s", out)
	}
}

func TestRenderDetailPanel_GatewayDisabled_NoEnvSection(t *testing.T) {
	cfg := &Config{ServerURL: "https://cloud.example.com", APIToken: "secret-jwt-token"}
	m := detailPanelModel(SessionRow{
		Name:     "s1",
		Provider: "claude",
	}, cfg)

	out := ansiRe.ReplaceAllString(m.renderDetailPanel(120, 40), "")

	if strings.Contains(out, "Gateway") {
		t.Errorf("detail panel must not show Gateway row when gateway is disabled:\n%s", out)
	}
	if strings.Contains(out, "ANTHROPIC_BASE_URL") {
		t.Errorf("detail panel must not show gateway env vars when gateway is disabled:\n%s", out)
	}
}
