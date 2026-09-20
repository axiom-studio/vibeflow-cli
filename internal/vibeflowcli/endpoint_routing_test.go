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
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

func TestSessionMeta_EndpointFieldsRoundTrip(t *testing.T) {
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	store := NewStore()
	meta := SessionMeta{
		Name:     "s1",
		Provider: "copilot",
		Routing:  RoutingEndpoint,
		Vendor:   "example-vendor",
		BaseURL:  "http://llm-proxy.local:4000/v1",
		Model:    "some-model",
	}
	if err := store.Add(meta); err != nil {
		t.Fatal(err)
	}
	got, found, err := store.Get("s1")
	if err != nil || !found {
		t.Fatalf("Get: found=%v err=%v", found, err)
	}
	if got.Routing != meta.Routing || got.Vendor != meta.Vendor || got.BaseURL != meta.BaseURL || got.Model != meta.Model {
		t.Errorf("round trip = %+v, want routing/vendor/base_url/model of %+v", got, meta)
	}

	// JSON field names are part of the on-disk format.
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"routing":"endpoint"`, `"vendor":"example-vendor"`, `"base_url":"http://llm-proxy.local:4000/v1"`, `"model":"some-model"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("JSON %s missing %s", data, field)
		}
	}

	// Records written before these fields existed still decode, and resolve
	// to the routing they were launched with.
	var legacy SessionMeta
	if err := json.Unmarshal([]byte(`{"name":"old","provider":"claude","llm_gateway_enabled":true}`), &legacy); err != nil {
		t.Fatalf("legacy record: %v", err)
	}
	if legacy.Routing != "" || legacy.Vendor != "" || legacy.BaseURL != "" {
		t.Errorf("legacy record decoded unexpected values: %+v", legacy)
	}
	if got := routingForMeta(legacy); got != RoutingGateway {
		t.Errorf("legacy gateway record routing = %q, want gateway", got)
	}
}

func TestRestartSession_EndpointWithoutURLOrModelFailsBeforeLaunch(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	tm := NewTmuxManager(fmt.Sprintf("vftest-endpoint-noep-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	cfg := DefaultConfig()

	for _, meta := range []SessionMeta{
		{Name: "no-url", Provider: "copilot", Routing: RoutingEndpoint, Model: "some-model", WorkingDir: t.TempDir()},
		{Name: "no-model", Provider: "qwen", Routing: RoutingEndpoint, BaseURL: "http://llm-proxy.local/v1", WorkingDir: t.TempDir()},
	} {
		meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)
		_, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg))
		if err == nil || !strings.Contains(err.Error(), "base URL or model") {
			t.Errorf("%s: err = %v, want missing base URL/model error", meta.Name, err)
		}
		if tm.HasSession(meta.TmuxSession) {
			t.Errorf("%s: a session was launched despite the error", meta.Name)
		}
	}
}

// TestRestartSession_QwenEndpointRestoresEndpoint restarts a qwen session
// routed to a compatible endpoint in a real tmux server with a fake agent
// that records its argv and OPENAI_* env. The test process (and so the tmux
// server it starts) exports a real-looking OpenAI key and base URL, which
// must never reach the pane.
func TestRestartSession_QwenEndpointRestoresEndpoint(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	// Values the tmux server inherits as its global env.
	t.Setenv("OPENAI_API_KEY", "sk-shell-openai")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_COMPAT_API_KEY", "")
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "")
	t.Setenv("OPENAI_COMPAT_API_KEY_KEYLESS_VENDOR", "")

	tm := NewTmuxManager(fmt.Sprintf("vftest-endpoint-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	// Fake agent: record argv, then the env the endpoint would see, then idle.
	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-qwen")
	script := "#!/bin/sh\n{ printf 'ARG=%s\\n' \"$@\"; printf 'KEY=%s\\nURL=%s\\nMODEL=%s\\n' \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_MODEL\"; } > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	prov := cfg.Providers["qwen"]
	prov.Binary = binary
	cfg.Providers["qwen"] = prov
	cfg.SaveOpenAICompatKey("example-vendor", "sk-vendor")

	readRecord := func() string {
		t.Helper()
		deadline := time.Now().Add(5 * time.Second)
		for time.Now().Before(deadline) {
			if data, err := os.ReadFile(record); err == nil && strings.Contains(string(data), "MODEL=") {
				return string(data)
			}
			time.Sleep(10 * time.Millisecond)
		}
		t.Fatal("fake agent did not record its launch")
		return ""
	}

	tests := []struct {
		vendor  string
		wantKey string
	}{
		{"example-vendor", "KEY=sk-vendor"},            // saved per-vendor key
		{"keyless-vendor", "KEY=" + openAICompatNoKey}, // keyless endpoint → placeholder
	}
	for _, tt := range tests {
		t.Run(tt.vendor, func(t *testing.T) {
			_ = os.Remove(record)
			meta := SessionMeta{
				Name:        "ep-" + tt.vendor,
				Provider:    "qwen",
				WorkingDir:  repo,
				Branch:      "main",
				SessionType: "vanilla",
				Routing:     RoutingEndpoint,
				Vendor:      tt.vendor,
				BaseURL:     "http://llm-proxy.local:4000/v1",
				Model:       "some-model",
			}
			meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)

			updated, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg))
			if err != nil {
				t.Fatal(err)
			}
			got := readRecord()
			for _, want := range []string{
				"ARG=--auth-type\nARG=openai\n",
				"ARG=--openai-base-url\nARG=http://llm-proxy.local:4000/v1\n",
				"ARG=--model\nARG=some-model\n",
				tt.wantKey + "\n",
				"URL=http://llm-proxy.local:4000/v1\n",
				"MODEL=some-model\n",
			} {
				if !strings.Contains(got, want) {
					t.Errorf("agent record missing %q:\n%s", want, got)
				}
			}
			if strings.Contains(got, "sk-shell-openai") || strings.Contains(got, "api.openai.com") {
				t.Errorf("inherited shell OPENAI_* value reached the session:\n%s", got)
			}
			// The endpoint must survive into the metadata used by the next restart.
			if updated.Routing != RoutingEndpoint || updated.Vendor != tt.vendor || updated.BaseURL != meta.BaseURL || updated.Model != meta.Model {
				t.Errorf("updated meta lost the endpoint: %+v", updated)
			}
		})
	}
}

// endpointTestProviders returns the built-in harnesses with a binary that is
// always installed, so every one of them is selectable in the wizard.
func endpointTestProviders() map[string]Provider {
	providers := make(map[string]Provider)
	for key, p := range DefaultConfig().Providers {
		p.Binary = "sh"
		providers[key] = p
	}
	return providers
}

// endpointWizardFixture returns a real vanilla wizard on the provider step
// with providerKey selected. Config writes go to a temp root.
func endpointWizardFixture(t *testing.T, cfg *Config, providerKey string) WizardModel {
	t.Helper()
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	clearShellEndpoints(t) // the host shell may export one
	cfg.Providers = endpointTestProviders()
	wm := NewWizardModel(NewProviderRegistry(cfg), ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 0 // vanilla
	wm.selectedProvider = providerIdxByKey(t, wm, providerKey)
	wm.cursor = wm.selectedProvider
	wm.step = StepProvider
	wm.branches = []string{"[+] Create new branch", "main"}
	wm.filteredBranches = []int{0, 1}
	return wm
}

// routingModes lists the modes the Routing step offers, in display order.
func routingModes(w WizardModel) []string {
	var modes []string
	for _, opt := range w.routingOptions() {
		modes = append(modes, opt.mode)
	}
	return modes
}

// chooseRouting selects mode on the Routing step and advances.
func chooseRouting(t *testing.T, w WizardModel, mode string) WizardModel {
	t.Helper()
	if w.step != StepLLMGateway {
		t.Fatalf("step = %v, want the Routing step", w.step)
	}
	for i, opt := range w.routingOptions() {
		if opt.mode == mode {
			w.cursor = i
			w, _ = w.advance()
			return w
		}
	}
	t.Fatalf("routing %q not offered (have %v)", mode, routingModes(w))
	return w
}

// toEndpointStep advances from the provider step through Routing to the
// compatible endpoint inputs.
func toEndpointStep(t *testing.T, w WizardModel) WizardModel {
	t.Helper()
	w, _ = w.advance()
	w = chooseRouting(t, w, RoutingEndpoint)
	if w.step != StepOpenAICompatConfig {
		t.Fatalf("after choosing endpoint: step = %v, want StepOpenAICompatConfig", w.step)
	}
	return w
}

// press sends one key to the wizard.
func press(w WizardModel, key tea.KeyPressMsg) WizardModel {
	got, _ := w.Update(key)
	return got
}

// typeText types s into the focused wizard input.
func typeText(w WizardModel, s string) WizardModel {
	return press(w, tea.KeyPressMsg{Text: s})
}

var (
	keyEnter = tea.KeyPressMsg{Code: tea.KeyEnter}
	keyTab   = tea.KeyPressMsg{Code: tea.KeyTab}
	keyEsc   = tea.KeyPressMsg{Code: tea.KeyEscape}
	keyBack  = tea.KeyPressMsg{Code: tea.KeyBackspace}
)

// fillEndpoint types base URL, vendor, model and key into the endpoint step.
func fillEndpoint(w WizardModel, baseURL, vendor, model, key string) WizardModel {
	for _, v := range []string{baseURL, vendor, model, key} {
		w = typeText(w, v)
		w = press(w, keyTab)
	}
	w.cursor = oacRowAPIKey
	return w
}

func TestWizard_RoutingOptionsPerHarness(t *testing.T) {
	tests := []struct {
		provider       string
		endpointOn     bool
		endpointNoteIn string // part of the option's note
	}{
		{"copilot", true, "OpenAI API"},
		{"qwen", true, "OpenAI API"},
		{"codex", true, "OpenAI Responses API"},
		{"claude", true, "Anthropic Messages API"},
		{"gemini", true, "Gemini API"},
		{"cursor", false, "not supported by Cursor Agent"},
		{"kiro", false, "not supported by Kiro CLI"},
	}
	for _, tt := range tests {
		t.Run(tt.provider, func(t *testing.T) {
			w := endpointWizardFixture(t, &Config{}, tt.provider)
			w.enterRoutingStep()
			// Vanilla sessions never see the gateway: direct and endpoint only.
			if got := routingModes(w); strings.Join(got, ",") != "direct,endpoint" {
				t.Fatalf("routing modes = %v, want [direct endpoint]", got)
			}
			ep := w.routingOptions()[1]
			if ep.enabled != tt.endpointOn || !strings.Contains(ep.note, tt.endpointNoteIn) {
				t.Errorf("endpoint option = %+v, want enabled=%v note containing %q", ep, tt.endpointOn, tt.endpointNoteIn)
			}
			// The option is always visible; an unavailable one says why.
			view := w.View()
			for _, want := range []string{"Configure routing for your coding agent", "Connect directly to the provider", "Connect to a compatible endpoint", tt.endpointNoteIn} {
				if !strings.Contains(view, want) {
					t.Errorf("routing view missing %q:\n%s", want, view)
				}
			}
			if !tt.endpointOn {
				// Selecting the unavailable option does nothing.
				w.cursor = 1
				w, _ = w.advance()
				if w.step != StepLLMGateway || w.routingChosen {
					t.Errorf("disabled endpoint option was accepted: step=%v", w.step)
				}
			}
		})
	}
}

func TestWizard_RoutingOffersGatewayOnlyWhereItWorks(t *testing.T) {
	tests := []struct {
		provider    string
		sessionType int // 0 vanilla, 1 vibeflow
		token       string
		want        string
	}{
		{"claude", 1, "tok", "gateway,direct,endpoint"},
		{"codex", 1, "tok", "gateway,direct,endpoint"},
		{"claude", 1, "", "direct,endpoint"}, // no API token → no gateway
		{"claude", 0, "tok", "direct,endpoint"},
		{"copilot", 1, "tok", "direct,endpoint"}, // harness without gateway support
		{"qwen", 1, "tok", "direct,endpoint"},
		{"cursor", 1, "tok", "direct,endpoint"},
		{"kiro", 1, "tok", "direct,endpoint"},
	}
	for _, tt := range tests {
		t.Run(fmt.Sprintf("%s/type%d/token=%v", tt.provider, tt.sessionType, tt.token != ""), func(t *testing.T) {
			w := endpointWizardFixture(t, &Config{APIToken: tt.token}, tt.provider)
			w.selectedSessionType = tt.sessionType
			if got := strings.Join(routingModes(w), ","); got != tt.want {
				t.Errorf("routing modes = %s, want %s", got, tt.want)
			}
		})
	}
}

func TestWizard_RoutingCursorStartsOnSavedChoice(t *testing.T) {
	// A saved gateway preference puts the cursor on the gateway where it is
	// offered, and falls back to direct where it isn't.
	w := endpointWizardFixture(t, &Config{APIToken: "tok", LLMGatewayEnabled: true}, "claude")
	w.selectedSessionType = 1
	w.routing = RoutingGateway
	w.enterRoutingStep()
	if opt := w.routingOptions()[w.cursor]; opt.mode != RoutingGateway {
		t.Errorf("cursor on %q, want gateway", opt.mode)
	}
	w.selectedProvider = providerIdxByKey(t, w, "copilot")
	w.enterRoutingStep()
	if opt := w.routingOptions()[w.cursor]; opt.mode != RoutingDirect {
		t.Errorf("cursor on %q, want direct when the gateway is not offered", opt.mode)
	}
}

func TestWizard_EndpointGoesToEndpointStepThenBranch(t *testing.T) {
	w := toEndpointStep(t, endpointWizardFixture(t, &Config{}, "copilot"))
	w = fillEndpoint(w, "http://llm-proxy.local:4000/v1", "example-vendor", "some-model", "")
	w = press(w, keyEnter)
	if w.oacErr != "" {
		t.Fatalf("unexpected validation error: %s", w.oacErr)
	}
	if w.step != StepBranch {
		t.Errorf("after endpoint: step = %v, want StepBranch", w.step)
	}
	// Back from branch returns to the endpoint step.
	back, _ := w.goBack()
	if back.step != StepOpenAICompatConfig {
		t.Errorf("back from branch: step = %v, want StepOpenAICompatConfig", back.step)
	}
	// Back from the endpoint step returns to Routing, then to the provider list.
	back, _ = back.goBack()
	if back.step != StepLLMGateway {
		t.Errorf("back from endpoint: step = %v, want the Routing step", back.step)
	}
	back, _ = back.goBack()
	if back.step != StepProvider {
		t.Errorf("back from routing: step = %v, want StepProvider", back.step)
	}
}

func TestWizard_QwenEndpointSkipsQwenPresets(t *testing.T) {
	// The endpoint step configures qwen's endpoint, so qwen's own vendor
	// presets step is skipped (and back from branch returns to the endpoint).
	cfg := &Config{SavedEnvVars: map[string]string{"OPENAI_API_KEY": "sk-saved"}}
	w := toEndpointStep(t, endpointWizardFixture(t, cfg, "qwen"))
	w = fillEndpoint(w, "http://llm-proxy.local:4000/v1", "", "some-model", "")
	w = press(w, keyEnter)
	if w.step != StepBranch {
		t.Fatalf("step = %v, want StepBranch", w.step)
	}

	// Direct routing still shows the qwen presets.
	w = endpointWizardFixture(t, cfg, "qwen")
	w, _ = w.advance()
	w = chooseRouting(t, w, RoutingDirect)
	if w.step != StepQwenLaunchConfig {
		t.Errorf("direct qwen: step = %v, want StepQwenLaunchConfig", w.step)
	}
}

func TestWizard_ProviderKeyAskedOnlyForDirectRouting(t *testing.T) {
	// qwen reads OPENAI_API_KEY. With none saved, the key prompt waits until
	// the user picks direct routing; an endpoint brings its own key.
	t.Setenv("OPENAI_API_KEY", "")
	w := endpointWizardFixture(t, &Config{}, "qwen")
	w, _ = w.advance()
	if w.step != StepLLMGateway {
		t.Fatalf("after provider: step = %v, want the Routing step before any key prompt", w.step)
	}
	w = chooseRouting(t, w, RoutingDirect)
	if w.step != StepEnvToken || w.envTokenVarName != "OPENAI_API_KEY" {
		t.Fatalf("direct: step = %v var = %q, want the OPENAI_API_KEY prompt", w.step, w.envTokenVarName)
	}
	// Esc from that prompt returns to Routing; choosing an endpoint skips it.
	w = press(w, keyEsc)
	if w.step != StepLLMGateway {
		t.Fatalf("esc from key prompt: step = %v, want the Routing step", w.step)
	}
	w = chooseRouting(t, w, RoutingEndpoint)
	if w.step != StepOpenAICompatConfig {
		t.Errorf("endpoint: step = %v, want StepOpenAICompatConfig", w.step)
	}
}

func TestWizard_EndpointForcesGatewayOff(t *testing.T) {
	// A vibeflow session with a token and a saved gateway "yes" starts on the
	// gateway; choosing an endpoint must turn it off and remember that.
	cfg := &Config{APIToken: "tok", LLMGatewayEnabled: true}
	w := endpointWizardFixture(t, cfg, "claude")
	w.selectedSessionType = 1
	w.routing = RoutingGateway
	w.llmGatewayEnabled = true
	w = toEndpointStep(t, w)
	if w.llmGatewayEnabled || w.routing != RoutingEndpoint {
		t.Errorf("gateway=%v routing=%q, want gateway off and endpoint routing", w.llmGatewayEnabled, w.routing)
	}
	if cfg.LLMGatewayEnabled {
		t.Error("saved gateway preference not updated")
	}
}

func TestWizard_EndpointValidation(t *testing.T) {
	tests := []struct {
		name, baseURL, vendor, model, wantErr string
	}{
		{"empty URL", "", "example-vendor", "some-model", "base URL"},
		{"non-http URL", "ftp://host/v1", "example-vendor", "some-model", "base URL"},
		{"URL without host", "http://", "example-vendor", "some-model", "base URL"},
		{"empty model", "http://host/v1", "example-vendor", "   ", "model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := toEndpointStep(t, endpointWizardFixture(t, &Config{}, "copilot"))
			w = fillEndpoint(w, tt.baseURL, tt.vendor, tt.model, "")
			w = press(w, keyEnter)
			if w.step != StepOpenAICompatConfig {
				t.Errorf("step = %v, want to stay on StepOpenAICompatConfig", w.step)
			}
			if !strings.Contains(w.oacErr, tt.wantErr) {
				t.Errorf("oacErr = %q, want it to mention %q", w.oacErr, tt.wantErr)
			}
			if !strings.Contains(w.View(), w.oacErr) {
				t.Error("validation error is not rendered inline")
			}
			// Editing clears the error.
			w = press(w, keyBack)
			if w.oacErr != "" {
				t.Errorf("error not cleared after edit: %q", w.oacErr)
			}
		})
	}
}

func TestWizard_EndpointStepNamesTheAPIFormat(t *testing.T) {
	for provider, format := range map[string]string{"copilot": "OpenAI API", "codex": "OpenAI Responses API", "claude": "Anthropic Messages API"} {
		w := toEndpointStep(t, endpointWizardFixture(t, &Config{}, provider))
		if view := w.View(); !strings.Contains(view, "must speak the "+format) {
			t.Errorf("%s endpoint step does not name the %s:\n%s", provider, format, view)
		}
	}
}

func TestWizard_EndpointKeyMaskedStoredPerVendorAndNeverInConfirm(t *testing.T) {
	const key = "sk-secret-value-123"
	cfg := &Config{SavedEnvVars: map[string]string{"OPENAI_API_KEY": "sk-shared"}}
	w := endpointWizardFixture(t, cfg, "copilot")
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "")
	w = toEndpointStep(t, w)
	w = fillEndpoint(w, "http://llm-proxy.local:4000/v1", "example-vendor", "some-model", key)

	// Masked while typing: bullets, never the characters.
	if view := w.View(); strings.Contains(view, key) || !strings.Contains(view, strings.Repeat("•", len(key))) {
		t.Errorf("API key input is not masked:\n%s", view)
	}

	w = press(w, keyEnter)
	if got := cfg.SavedEnvVars["OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR"]; got != key {
		t.Errorf("vendor key slot = %q, want the typed key", got)
	}
	if cfg.SavedEnvVars["OPENAI_API_KEY"] != "sk-shared" {
		t.Error("shared OPENAI_API_KEY slot was modified")
	}
	if cfg.OpenAICompat != (OpenAICompatConfig{LastBaseURL: "http://llm-proxy.local:4000/v1", LastVendor: "example-vendor", LastModel: "some-model"}) {
		t.Errorf("last-used endpoint = %+v", cfg.OpenAICompat)
	}
	if w.oacInputs[oacRowAPIKey] != "" {
		t.Error("typed key must be dropped from wizard state after it is stored")
	}

	// Confirm view: routing and endpoint visible, key absent (not even masked).
	w.step = StepConfirm
	view := w.View()
	for _, want := range []string{"Routing:       Compatible endpoint", "example-vendor", "http://llm-proxy.local:4000/v1", "some-model", "saved for this vendor"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, key) || strings.Contains(view, "•") {
		t.Errorf("confirm view exposes the API key:\n%s", view)
	}
}

func TestWizard_EndpointPrefillsLastUsedEndpoint(t *testing.T) {
	cfg := &Config{OpenAICompat: OpenAICompatConfig{LastBaseURL: "http://prev/v1", LastVendor: "prev-vendor", LastModel: "prev-model"}}
	w := toEndpointStep(t, endpointWizardFixture(t, cfg, "copilot"))
	if w.oacInputs[oacRowBaseURL] != "http://prev/v1" || w.oacInputs[oacRowVendor] != "prev-vendor" || w.oacInputs[oacRowModel] != "prev-model" {
		t.Errorf("prefill = %q", w.oacInputs)
	}
	if w.oacInputs[oacRowAPIKey] != "" {
		t.Error("API key input must never be prefilled")
	}
	// Re-entry keeps what the user typed rather than re-prefilling.
	w = typeText(w, "x")
	w, _ = w.goBack()
	w = chooseRouting(t, w, RoutingEndpoint)
	if w.oacInputs[oacRowBaseURL] != "http://prev/v1x" {
		t.Errorf("re-entry lost edits: %q", w.oacInputs[oacRowBaseURL])
	}
}

func TestWizard_EndpointLettersAreTypedNotNavigation(t *testing.T) {
	// j/k navigate other steps; here they must land in the input.
	w := toEndpointStep(t, endpointWizardFixture(t, &Config{}, "copilot"))
	w = typeText(w, "jk")
	if w.cursor != oacRowBaseURL || w.oacInputs[oacRowBaseURL] != "jk" {
		t.Errorf("cursor=%d input=%q, want row 0 containing \"jk\"", w.cursor, w.oacInputs[oacRowBaseURL])
	}
	// Enter on a non-final row moves down instead of validating.
	w = press(w, keyEnter)
	if w.cursor != oacRowVendor || w.oacErr != "" {
		t.Errorf("enter on row 0: cursor=%d err=%q, want row 1 and no error", w.cursor, w.oacErr)
	}
	// Esc returns to the Routing step.
	if w = press(w, keyEsc); w.step != StepLLMGateway {
		t.Errorf("esc: step = %v, want the Routing step", w.step)
	}
}

func TestWizardResult_QuickSwitchReusesRouting(t *testing.T) {
	t.Run("endpoint session reuses its endpoint", func(t *testing.T) {
		w := endpointWizardFixture(t, &Config{}, "copilot")
		w.switchSource = &SessionMeta{SessionType: "vanilla", Routing: RoutingEndpoint, Vendor: "example-vendor", BaseURL: "http://host/v1", Model: "m"}
		w.worktreeOpts = []string{"Current directory"}
		got, _ := w.buildQuickSwitchResult()
		if got.result.Routing != RoutingEndpoint || got.result.Vendor != "example-vendor" || got.result.BaseURL != "http://host/v1" || got.result.Model != "m" {
			t.Errorf("result = %q %q %q %q", got.result.Routing, got.result.Vendor, got.result.BaseURL, got.result.Model)
		}
	})
	t.Run("gateway session stays on the gateway", func(t *testing.T) {
		w := endpointWizardFixture(t, &Config{}, "claude")
		w.switchSource = &SessionMeta{SessionType: "vanilla", LLMGatewayEnabled: true, BaseURL: "http://stale/v1"}
		w.worktreeOpts = []string{"Current directory"}
		got, _ := w.buildQuickSwitchResult()
		if got.result.Routing != RoutingGateway || got.result.BaseURL != "" {
			t.Errorf("result routing = %q base URL = %q, want gateway and no endpoint", got.result.Routing, got.result.BaseURL)
		}
	})
}

func TestExecuteLaunch_EndpointRequiresSupportAndEndpoint(t *testing.T) {
	// A launch that reached the TUI without a usable endpoint must fail
	// cleanly before any worktree or session is created.
	m := Model{config: &Config{}}
	tests := []struct {
		result  WizardResult
		wantErr string
	}{
		{WizardResult{ProviderKey: "qwen", Routing: RoutingEndpoint, WorktreeChoice: WorktreeCurrent}, "needs a compatible endpoint"},
		{WizardResult{ProviderKey: "cursor", Provider: Provider{Name: "Cursor Agent"}, Routing: RoutingEndpoint, BaseURL: "http://h/v1", Model: "m", WorktreeChoice: WorktreeCurrent}, "cannot connect to a compatible endpoint"},
	}
	for _, tt := range tests {
		msg := m.executeLaunch(tt.result)
		sm, ok := msg.(sessionsMsg)
		if !ok || sm.err == nil || !strings.Contains(sm.err.Error(), tt.wantErr) {
			t.Errorf("%s: executeLaunch = %#v, want error %q", tt.result.ProviderKey, msg, tt.wantErr)
		}
	}
}

func TestWizard_EndpointConfirmCarriesEndpointIntoResult(t *testing.T) {
	w := toEndpointStep(t, endpointWizardFixture(t, &Config{}, "copilot"))
	w = fillEndpoint(w, " http://llm-proxy.local:4000/v1 ", "example-vendor", "some-model", "")
	w = press(w, keyEnter)
	// Skip the branch/worktree/permission choices: confirm straight away.
	w.step = StepConfirm
	w.selectedBranch = 1
	w.worktreeOpts = []string{"Current directory"}
	w.permissionOpts = []string{"Yes", "No"}
	w, _ = w.advance()
	if !w.done {
		t.Fatal("wizard did not finish on confirm")
	}
	if w.result.Routing != RoutingEndpoint || w.result.LLMGatewayEnabled {
		t.Errorf("result routing = %q gateway = %v, want endpoint without gateway", w.result.Routing, w.result.LLMGatewayEnabled)
	}
	if w.result.Vendor != "example-vendor" || w.result.BaseURL != "http://llm-proxy.local:4000/v1" || w.result.Model != "some-model" {
		t.Errorf("result endpoint = %q %q %q (base URL must be trimmed)", w.result.Vendor, w.result.BaseURL, w.result.Model)
	}
}

func TestValidateRoutingFlags(t *testing.T) {
	const url, vendor, model = "http://llm-proxy.local:4000/v1", "example-vendor", "some-model"
	tests := []struct {
		name, provider, routing string
		llmGateway              bool
		baseURL, vendor, model  string
		wantErr                 []string // substrings; nil = no error
	}{
		{"no routing flags", "claude", "", false, "", "", model, nil},
		{"legacy --llm-gateway", "claude", "", true, "", "", "", nil},
		{"--llm-gateway with --routing gateway", "claude", RoutingGateway, true, "", "", "", nil},
		{"direct", "copilot", RoutingDirect, false, "", "", "", nil},
		{"endpoint complete", "copilot", RoutingEndpoint, false, url, vendor, model, nil},
		{"endpoint without vendor", "codex", RoutingEndpoint, false, url, "", model, nil},
		{"unknown routing", "claude", "proxy", false, "", "", "", []string{"--routing must be gateway, direct, endpoint or shell"}},
		{"--llm-gateway conflicts with direct", "claude", RoutingDirect, true, "", "", "", []string{"--llm-gateway conflicts with --routing direct"}},
		{"--llm-gateway conflicts with endpoint", "claude", RoutingEndpoint, true, url, "", model, []string{"conflicts"}},
		{"--base-url without endpoint", "claude", "", false, url, "", "", []string{"only valid with --routing endpoint"}},
		{"--vendor with direct", "qwen", RoutingDirect, false, "", vendor, "", []string{"only valid with --routing endpoint"}},
		{"gateway on harness without it", "copilot", RoutingGateway, false, "", "", "", []string{"cannot route through the Axiom Studio AI Gateway"}},
		{"gateway on kiro", "kiro", RoutingGateway, false, "", "", "", []string{"cannot route through the Axiom Studio AI Gateway"}},
		{"endpoint on cursor", "cursor", RoutingEndpoint, false, url, "", model, []string{"cannot connect to a compatible endpoint"}},
		{"endpoint on kiro", "kiro", RoutingEndpoint, false, url, "", model, []string{"cannot connect to a compatible endpoint"}},
		{"all missing named at once", "copilot", RoutingEndpoint, false, "", "", "", []string{"requires --base-url, --model"}},
		{"missing model", "claude", RoutingEndpoint, false, url, vendor, "", []string{"requires --model"}},
		{"invalid url", "qwen", RoutingEndpoint, false, "localhost:4000", vendor, model, []string{"http(s) URL"}},
		{"credentialed url", "copilot", RoutingEndpoint, false, "https://u:S3cret@h/v1", "", model, []string{"credentials"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateRoutingFlags(tt.provider, tt.routing, tt.llmGateway, tt.baseURL, tt.vendor, tt.model)
			if tt.wantErr == nil {
				if err != nil {
					t.Errorf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("expected error mentioning %v", tt.wantErr)
			}
			for _, want := range tt.wantErr {
				if !strings.Contains(err.Error(), want) {
					t.Errorf("error %q missing %q", err, want)
				}
			}
			if strings.Contains(err.Error(), "S3cret") {
				t.Errorf("error echoes the credential: %v", err)
			}
		})
	}
}

// TestLaunchCmd_CopilotEndpoint runs `vibeflow launch --provider copilot
// --routing endpoint` end to end on a real tmux server with a fake agent
// recording its Copilot BYOK env.
func TestLaunchCmd_CopilotEndpoint(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	t.Chdir(repo)
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	// Values the tmux server inherits; none may reach the pane.
	t.Setenv("COPILOT_PROVIDER_BEARER_TOKEN", "shell-bearer")
	t.Setenv("COPILOT_PROVIDER_WIRE_API", "responses")
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "sk-vendor-from-shell")
	t.Setenv("MCP_TOKEN", "") // keep any real token out of the test

	socket := fmt.Sprintf("vftest-endpoint-launch-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-copilot")
	script := "#!/bin/sh\nprintf 'URL=%s\\nTYPE=%s\\nKEY=%s\\nBEARER=%s\\nWIRE=%s\\nMODEL=%s\\n' \"$COPILOT_PROVIDER_BASE_URL\" \"$COPILOT_PROVIDER_TYPE\" \"$COPILOT_PROVIDER_API_KEY\" \"$COPILOT_PROVIDER_BEARER_TOKEN\" \"$COPILOT_PROVIDER_WIRE_API\" \"$COPILOT_MODEL\" > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	prov := cfg.Providers["copilot"]
	prov.Binary = binary
	cfg.Providers["copilot"] = prov
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}

	root := &cobra.Command{Use: "vibeflow"}
	root.PersistentFlags().String("config", "", "")
	root.PersistentFlags().String("mcp", "", "")
	root.AddCommand(launchCmd())
	root.SilenceErrors, root.SilenceUsage = true, true
	root.SetArgs([]string{"launch", "--provider", "copilot", "--routing", "endpoint", "--base-url", "http://llm-proxy.local:4000/v1", "--vendor", "example-vendor", "--model", "some-model"})
	if err := root.Execute(); err != nil {
		t.Fatal(err)
	}

	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(record); err == nil && strings.Contains(string(data), "MODEL=") {
			got = string(data)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{
		"URL=http://llm-proxy.local:4000/v1\n",
		"TYPE=openai\n",
		"KEY=sk-vendor-from-shell\n",
		"BEARER=\n",
		"WIRE=\n",
		"MODEL=some-model\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("agent record missing %q:\n%s", want, got)
		}
	}

	// Metadata keeps the routing and endpoint for restart; the key is never stored.
	metas, err := NewStore().List()
	if err != nil || len(metas) != 1 {
		t.Fatalf("stored sessions = %v, %v", metas, err)
	}
	m := metas[0]
	if m.Routing != RoutingEndpoint || m.Vendor != "example-vendor" || m.BaseURL != "http://llm-proxy.local:4000/v1" || m.Model != "some-model" {
		t.Errorf("meta = %q %q %q %q", m.Routing, m.Vendor, m.BaseURL, m.Model)
	}
	raw, err := os.ReadFile(DefaultStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-vendor-from-shell") {
		t.Error("API key written to sessions.json")
	}
}

func TestValidateOpenAICompatEndpoint_RejectsCredentialsInURL(t *testing.T) {
	tests := []struct {
		baseURL string
		wantErr string // "" = accepted
	}{
		{"http://localhost:4000/v1", ""},         // plain URL still accepted
		{"https://llm.internal:8443/api/v1", ""}, // port + path accepted
		{"https://ci:S3cret@llm.internal/v1", "credentials"},
		{"https://ci@llm.internal/v1", "credentials"}, // user without password
		{"https://h/v1?api-key=S3cret", "query string"},
		{"https://h/v1?", "query string"}, // empty query marker
		{"https://h/v1#token=S3cret", "query string or fragment"},
	}
	for _, tt := range tests {
		err := ValidateOpenAICompatEndpoint(tt.baseURL, "example-vendor", "some-model")
		switch {
		case tt.wantErr == "" && err != nil:
			t.Errorf("%q: unexpected error %v", tt.baseURL, err)
		case tt.wantErr != "" && (err == nil || !strings.Contains(err.Error(), tt.wantErr)):
			t.Errorf("%q: err = %v, want it to mention %q", tt.baseURL, err, tt.wantErr)
		}
		// The rejection message must never echo the secret back.
		if err != nil && strings.Contains(err.Error(), "S3cret") {
			t.Errorf("%q: error echoes the credential: %v", tt.baseURL, err)
		}
	}
}

func TestWizard_EndpointCredentialedURLIsNotSaved(t *testing.T) {
	cfg := &Config{}
	w := toEndpointStep(t, endpointWizardFixture(t, cfg, "copilot"))
	w = fillEndpoint(w, "https://ci:S3cret@llm.internal/v1", "example-vendor", "some-model", "")
	w = press(w, keyEnter)
	if w.step != StepOpenAICompatConfig || !strings.Contains(w.oacErr, "credentials") {
		t.Errorf("step=%v err=%q, want to stay on the endpoint step with a credentials error", w.step, w.oacErr)
	}
	if cfg.OpenAICompat.LastBaseURL != "" {
		t.Errorf("credentialed URL written to config: %q", cfg.OpenAICompat.LastBaseURL)
	}
	if _, err := os.Stat(ConfigPath()); err == nil {
		if data, _ := os.ReadFile(ConfigPath()); strings.Contains(string(data), "S3cret") {
			t.Error("credential written to config.yaml")
		}
	}
}

// TestExecuteLaunch_EndpointDoesNotPersistSessionEnv reproduces the
// config-persistence path: load config → registry → TUI launch → SaveConfig.
// The shell-only vendor key must not end up in config.yaml, and a second
// launch with another vendor must not inherit the first launch's values.
func TestExecuteLaunch_EndpointDoesNotPersistSessionEnv(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("OPENAI_COMPAT_API_KEY_VENDOR_ONE", "sk-shell-only-one")
	t.Setenv("OPENAI_COMPAT_API_KEY_VENDOR_TWO", "sk-shell-only-two")
	t.Setenv("MCP_TOKEN", "") // keep any real token out of the test

	socket := fmt.Sprintf("vftest-endpoint-persist-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	binary := filepath.Join(state, "fake-qwen")
	if err := os.WriteFile(binary, []byte("#!/bin/sh\nsleep 300\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	seed := DefaultConfig()
	prov := seed.Providers["qwen"]
	prov.Binary = binary
	seed.Providers["qwen"] = prov
	if err := SaveConfig(seed, ConfigPath()); err != nil {
		t.Fatal(err)
	}
	cfg, err := LoadConfig(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	m := Model{config: cfg, tmux: tm, logger: NewLogger(), store: NewStore(), cache: NewSessionCache()}

	launch := func(vendor string) string {
		t.Helper()
		p, _ := NewProviderRegistry(cfg).Get("qwen")
		msg := m.executeLaunch(WizardResult{
			SessionType: "vanilla", ProviderKey: "qwen", Provider: p,
			WorkDir: repo, WorktreeChoice: WorktreeCurrent, Branch: "main",
			Routing: RoutingEndpoint, Vendor: vendor, BaseURL: "http://llm-proxy.local/v1", Model: "some-model",
		})
		if sm, ok := msg.(sessionsMsg); ok && sm.err != nil {
			t.Fatalf("launch %s: %v", vendor, sm.err)
		}
		// Newest session for this vendor → its pane env.
		metas, _ := NewStore().List()
		for i := len(metas) - 1; i >= 0; i-- {
			if metas[i].Vendor == vendor {
				out, err := tm.run("show-environment", "-t", metas[i].TmuxSession, "OPENAI_API_KEY")
				if err != nil {
					t.Fatal(err)
				}
				return strings.TrimSpace(out)
			}
		}
		t.Fatalf("no session recorded for %s", vendor)
		return ""
	}

	if got := launch("vendor-one"); got != "OPENAI_API_KEY=sk-shell-only-one" {
		t.Errorf("first session env = %q", got)
	}
	if got := launch("vendor-two"); got != "OPENAI_API_KEY=sk-shell-only-two" {
		t.Errorf("second session env = %q, want only the second vendor's key", got)
	}

	// Nothing session-specific may reach the saved config.
	data, err := os.ReadFile(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	for _, leaked := range []string{"sk-shell-only-one", "sk-shell-only-two", "llm-proxy.local"} {
		if strings.Contains(string(data), leaked) {
			t.Errorf("config.yaml contains %q after launch", leaked)
		}
	}
	if env := cfg.Providers["qwen"].Env; len(env) != 0 {
		// Report key names only — values may be secrets.
		keys := make([]string, 0, len(env))
		for k := range env {
			keys = append(keys, k)
		}
		t.Errorf("in-memory provider env mutated by launch (keys: %v)", keys)
	}
}

func TestNewProviderRegistry_EnvIsNotSharedWithConfig(t *testing.T) {
	cfg := &Config{Providers: map[string]Provider{"p": {Binary: "sh", Env: map[string]string{"A": "1"}}}}
	p, _ := NewProviderRegistry(cfg).Get("p")
	p.Env["SECRET"] = "x"
	if _, leaked := cfg.Providers["p"].Env["SECRET"]; leaked {
		t.Error("mutating a registry provider's env changed the config")
	}
}

func TestMigrateProviders_RemovesLegacyEndpointProvider(t *testing.T) {
	// Pre-release builds had a separate provider for compatible endpoints,
	// and could persist session values into its env. Migration drops it.
	path := filepath.Join(t.TempDir(), "config.yaml")
	cfg := DefaultConfig()
	cfg.Providers[legacyOpenAICompatProvider] = Provider{Binary: "qwen", Env: map[string]string{
		"OPENAI_API_KEY": "sk-leaked", "OPENAI_BASE_URL": "http://h/v1", "OPENAI_MODEL": "m",
	}}
	migrateProviders(cfg, path)
	if _, ok := cfg.Providers[legacyOpenAICompatProvider]; ok {
		t.Error("legacy provider still present after migration")
	}
	if _, ok := cfg.Providers["qwen"]; !ok {
		t.Error("migration removed a built-in provider")
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("migration did not save the cleaned config: %v", err)
	}
	if strings.Contains(string(data), "sk-leaked") || strings.Contains(string(data), legacyOpenAICompatProvider) {
		t.Error("saved config still contains the legacy provider")
	}
}

func TestWizard_EndpointVendorIsOptional(t *testing.T) {
	// The vendor is a display label; leaving it blank must still launch.
	cfg := &Config{}
	w := endpointWizardFixture(t, cfg, "copilot")
	t.Setenv("OPENAI_COMPAT_API_KEY", "")
	w = toEndpointStep(t, w)
	w = fillEndpoint(w, "http://llm-proxy.local:4000/v1", "", "some-model", "sk-no-vendor")
	w = press(w, keyEnter)
	if w.oacErr != "" || w.step != StepBranch {
		t.Fatalf("err=%q step=%v, want to continue to branch with no vendor", w.oacErr, w.step)
	}
	if got := cfg.SavedEnvVars["OPENAI_COMPAT_API_KEY"]; got != "sk-no-vendor" {
		t.Errorf("default key slot = %q, want the typed key", got)
	}
}
