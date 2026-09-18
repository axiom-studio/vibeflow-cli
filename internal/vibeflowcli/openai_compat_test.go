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
)

func TestSessionMeta_OpenAICompatFieldsRoundTrip(t *testing.T) {
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	store := NewStore()
	meta := SessionMeta{
		Name:     "s1",
		Provider: "openai-compatible",
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
	if got.Vendor != meta.Vendor || got.BaseURL != meta.BaseURL || got.Model != meta.Model {
		t.Errorf("round trip = %+v, want vendor/base_url/model of %+v", got, meta)
	}

	// JSON field names are part of the on-disk format.
	data, err := json.Marshal(meta)
	if err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{`"vendor":"example-vendor"`, `"base_url":"http://llm-proxy.local:4000/v1"`, `"model":"some-model"`} {
		if !strings.Contains(string(data), field) {
			t.Errorf("JSON %s missing %s", data, field)
		}
	}

	// Records written before these fields existed still decode.
	var legacy SessionMeta
	if err := json.Unmarshal([]byte(`{"name":"old","provider":"qwen"}`), &legacy); err != nil {
		t.Fatalf("legacy record: %v", err)
	}
	if legacy.Vendor != "" || legacy.BaseURL != "" {
		t.Errorf("legacy record decoded unexpected values: %+v", legacy)
	}
}

func TestRestartSession_OpenAICompatWithoutEndpointFailsBeforeLaunch(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	tm := NewTmuxManager(fmt.Sprintf("vftest-oacompat-noep-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })
	cfg := DefaultConfig()

	for _, meta := range []SessionMeta{
		{Name: "no-url", Provider: "openai-compatible", Vendor: "example-vendor", Model: "some-model", WorkingDir: t.TempDir()},
		{Name: "no-model", Provider: "openai-compatible", Vendor: "example-vendor", BaseURL: "http://llm-proxy.local/v1", WorkingDir: t.TempDir()},
	} {
		meta.TmuxSession = tm.FullSessionName(meta.Provider, meta.Name)
		_, err := RestartSession(meta, cfg, tm, NewStore(), NewSessionCache(), NewProviderRegistry(cfg))
		if err == nil || !strings.Contains(err.Error(), "missing the base URL or model") {
			t.Errorf("%s: err = %v, want missing base URL/model error", meta.Name, err)
		}
		if tm.HasSession(meta.TmuxSession) {
			t.Errorf("%s: a session was launched despite the error", meta.Name)
		}
	}
}

// TestRestartSession_OpenAICompatRestoresEndpoint restarts an openai-compatible
// session in a real tmux server with a fake agent that records its argv and
// OPENAI_* env. The test process (and so the tmux server it starts) exports a
// real-looking OpenAI key and base URL, which must never reach the pane.
func TestRestartSession_OpenAICompatRestoresEndpoint(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	// Values the tmux server inherits as its global env.
	t.Setenv("OPENAI_API_KEY", "sk-shell-openai")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "")
	t.Setenv("OPENAI_COMPAT_API_KEY_KEYLESS_VENDOR", "")

	tm := NewTmuxManager(fmt.Sprintf("vftest-oacompat-%d-%d", os.Getpid(), time.Now().UnixNano()))
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	// Fake agent: record argv, then the env the endpoint would see, then idle.
	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-qwen")
	script := "#!/bin/sh\n{ printf 'ARG=%s\\n' \"$@\"; printf 'KEY=%s\\nURL=%s\\nMODEL=%s\\n' \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_MODEL\"; } > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.Providers["openai-compatible"] = Provider{Binary: binary, LaunchTemplate: "{{.Binary}}"}
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
				Name:        "oac-" + tt.vendor,
				Provider:    "openai-compatible",
				WorkingDir:  repo,
				Branch:      "main",
				SessionType: "vanilla",
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
			if updated.Vendor != tt.vendor || updated.BaseURL != meta.BaseURL || updated.Model != meta.Model {
				t.Errorf("updated meta lost the endpoint: %+v", updated)
			}
		})
	}
}

// oacWizardFixture returns a real wizard on the provider step with the
// openai-compatible provider selected. Config writes go to a temp root.
func oacWizardFixture(t *testing.T, cfg *Config) WizardModel {
	t.Helper()
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	cfg.Providers = map[string]Provider{"openai-compatible": {Name: "OpenAI Compatible", Binary: "sh"}}
	wm := NewWizardModel(NewProviderRegistry(cfg), ".", nil, nil, "", nil, cfg)
	wm.selectedSessionType = 0 // vanilla
	wm.selectedProvider = providerIdxByKey(t, wm, "openai-compatible")
	wm.cursor = wm.selectedProvider
	wm.step = StepProvider
	wm.branches = []string{"[+] Create new branch", "main"}
	wm.filteredBranches = []int{0, 1}
	return wm
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

// fillOAC types base URL, vendor, model and key into the endpoint step.
func fillOAC(w WizardModel, baseURL, vendor, model, key string) WizardModel {
	for _, v := range []string{baseURL, vendor, model, key} {
		w = typeText(w, v)
		w = press(w, keyTab)
	}
	w.cursor = oacRowAPIKey
	return w
}

func TestWizard_OpenAICompatProviderGoesToEndpointStepThenBranch(t *testing.T) {
	w := oacWizardFixture(t, &Config{})
	w, _ = w.advance()
	if w.step != StepOpenAICompatConfig {
		t.Fatalf("after provider: step = %v, want StepOpenAICompatConfig", w.step)
	}
	w = fillOAC(w, "http://llm-proxy.local:4000/v1", "example-vendor", "some-model", "")
	w = press(w, keyEnter)
	if w.oacErr != "" {
		t.Fatalf("unexpected validation error: %s", w.oacErr)
	}
	if w.step != StepBranch {
		t.Errorf("after endpoint: step = %v, want StepBranch", w.step)
	}
	// Back from branch returns to the endpoint step (not the gateway step).
	back, _ := w.goBack()
	if back.step != StepOpenAICompatConfig {
		t.Errorf("back from branch: step = %v, want StepOpenAICompatConfig", back.step)
	}
	// Back from the endpoint step returns to provider selection.
	back, _ = back.goBack()
	if back.step != StepProvider {
		t.Errorf("back from endpoint: step = %v, want StepProvider", back.step)
	}
}

func TestWizard_OpenAICompatNeverShowsGatewayStep(t *testing.T) {
	// vibeflow session with an API token and a saved gateway "yes" would show
	// the gateway step for gateway-capable providers.
	w := oacWizardFixture(t, &Config{APIToken: "tok", LLMGatewayEnabled: true})
	w.selectedSessionType = 1
	w.llmGatewayEnabled = true
	w, _ = w.advance()
	if w.step != StepOpenAICompatConfig {
		t.Errorf("step = %v, want StepOpenAICompatConfig (gateway skipped)", w.step)
	}
	if w.llmGatewayEnabled {
		t.Error("stale gateway preference must be forced off")
	}
}

func TestWizard_OpenAICompatValidation(t *testing.T) {
	tests := []struct {
		name, baseURL, vendor, model, wantErr string
	}{
		{"empty URL", "", "example-vendor", "some-model", "base URL"},
		{"non-http URL", "ftp://host/v1", "example-vendor", "some-model", "base URL"},
		{"URL without host", "http://", "example-vendor", "some-model", "base URL"},
		{"empty vendor", "http://host/v1", "", "some-model", "vendor"},
		{"punctuation-only vendor", "http://host/v1", "--", "some-model", "vendor"},
		{"empty model", "http://host/v1", "example-vendor", "   ", "model"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := oacWizardFixture(t, &Config{})
			w, _ = w.advance()
			w = fillOAC(w, tt.baseURL, tt.vendor, tt.model, "")
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

func TestWizard_OpenAICompatKeyMaskedStoredPerVendorAndNeverInConfirm(t *testing.T) {
	const key = "sk-secret-value-123"
	cfg := &Config{SavedEnvVars: map[string]string{"OPENAI_API_KEY": "sk-shared"}}
	w := oacWizardFixture(t, cfg)
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "")
	w, _ = w.advance()
	w = fillOAC(w, "http://llm-proxy.local:4000/v1", "example-vendor", "some-model", key)

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

	// Confirm view: endpoint visible, key absent (not even masked).
	w.step = StepConfirm
	view := w.View()
	for _, want := range []string{"example-vendor", "http://llm-proxy.local:4000/v1", "some-model", "saved for this vendor"} {
		if !strings.Contains(view, want) {
			t.Errorf("confirm view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, key) || strings.Contains(view, "•") {
		t.Errorf("confirm view exposes the API key:\n%s", view)
	}
}

func TestWizard_OpenAICompatPrefillsLastUsedEndpoint(t *testing.T) {
	cfg := &Config{OpenAICompat: OpenAICompatConfig{LastBaseURL: "http://prev/v1", LastVendor: "prev-vendor", LastModel: "prev-model"}}
	w := oacWizardFixture(t, cfg)
	w, _ = w.advance()
	if w.oacInputs[oacRowBaseURL] != "http://prev/v1" || w.oacInputs[oacRowVendor] != "prev-vendor" || w.oacInputs[oacRowModel] != "prev-model" {
		t.Errorf("prefill = %q", w.oacInputs)
	}
	if w.oacInputs[oacRowAPIKey] != "" {
		t.Error("API key input must never be prefilled")
	}
	// Re-entry keeps what the user typed rather than re-prefilling.
	w = typeText(w, "x")
	w, _ = w.goBack()
	w, _ = w.advance()
	if w.oacInputs[oacRowBaseURL] != "http://prev/v1x" {
		t.Errorf("re-entry lost edits: %q", w.oacInputs[oacRowBaseURL])
	}
}

func TestWizard_OpenAICompatLettersAreTypedNotNavigation(t *testing.T) {
	// j/k navigate other steps; here they must land in the input.
	w := oacWizardFixture(t, &Config{})
	w, _ = w.advance()
	w = typeText(w, "jk")
	if w.cursor != oacRowBaseURL || w.oacInputs[oacRowBaseURL] != "jk" {
		t.Errorf("cursor=%d input=%q, want row 0 containing \"jk\"", w.cursor, w.oacInputs[oacRowBaseURL])
	}
	// Enter on a non-final row moves down instead of validating.
	w = press(w, keyEnter)
	if w.cursor != oacRowVendor || w.oacErr != "" {
		t.Errorf("enter on row 0: cursor=%d err=%q, want row 1 and no error", w.cursor, w.oacErr)
	}
	// Esc returns to provider selection.
	if w = press(w, keyEsc); w.step != StepProvider {
		t.Errorf("esc: step = %v, want StepProvider", w.step)
	}
}

func TestWizardResult_OpenAICompatCarriesEndpoint(t *testing.T) {
	t.Run("quick switch reuses the source session endpoint", func(t *testing.T) {
		w := oacWizardFixture(t, &Config{})
		w.switchSource = &SessionMeta{SessionType: "vanilla", Vendor: "example-vendor", BaseURL: "http://host/v1", Model: "m"}
		w.worktreeOpts = []string{"Current directory"}
		got, _ := w.buildQuickSwitchResult()
		if got.result.Vendor != "example-vendor" || got.result.BaseURL != "http://host/v1" || got.result.Model != "m" {
			t.Errorf("result endpoint = %q %q %q", got.result.Vendor, got.result.BaseURL, got.result.Model)
		}
	})
}

func TestExecuteLaunch_OpenAICompatWithoutEndpointFails(t *testing.T) {
	// A launch that reached the TUI without an endpoint must fail cleanly
	// before any worktree or session is created.
	m := Model{config: &Config{}}
	msg := m.executeLaunch(WizardResult{ProviderKey: "openai-compatible", WorktreeChoice: WorktreeCurrent})
	sm, ok := msg.(sessionsMsg)
	if !ok || sm.err == nil || !strings.Contains(sm.err.Error(), "needs an endpoint") {
		t.Errorf("executeLaunch = %#v, want endpoint error", msg)
	}
}

func TestWizard_OpenAICompatConfirmCarriesEndpointIntoResult(t *testing.T) {
	w := oacWizardFixture(t, &Config{})
	w, _ = w.advance()
	w = fillOAC(w, " http://llm-proxy.local:4000/v1 ", "example-vendor", "some-model", "")
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
	if w.result.Vendor != "example-vendor" || w.result.BaseURL != "http://llm-proxy.local:4000/v1" || w.result.Model != "some-model" {
		t.Errorf("result endpoint = %q %q %q (base URL must be trimmed)", w.result.Vendor, w.result.BaseURL, w.result.Model)
	}
}

func TestValidateOpenAICompatLaunchFlags(t *testing.T) {
	const url, vendor, model = "http://llm-proxy.local:4000/v1", "example-vendor", "some-model"
	tests := []struct {
		name, provider, baseURL, vendor, model string
		wantErr                                []string // substrings; nil = no error
	}{
		{"complete", "openai-compatible", url, vendor, model, nil},
		{"all missing named at once", "openai-compatible", "", "", "", []string{"--base-url", "--vendor", "--model"}},
		{"missing base url", "openai-compatible", "", vendor, model, []string{"requires --base-url"}},
		{"missing vendor", "openai-compatible", url, "", model, []string{"requires --vendor"}},
		{"missing model", "openai-compatible", url, vendor, "", []string{"requires --model"}},
		{"invalid url", "openai-compatible", "localhost:4000", vendor, model, []string{"http(s) URL"}},
		{"other provider without endpoint flags", "claude", "", "", model, nil},
		{"other provider rejects --base-url", "claude", url, "", "", []string{"only valid with --provider openai-compatible"}},
		{"other provider rejects --vendor", "qwen", "", vendor, "", []string{"only valid with --provider openai-compatible"}},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			err := validateOpenAICompatLaunchFlags(tt.provider, tt.baseURL, tt.vendor, tt.model)
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
		})
	}
}

// TestLaunchCmd_OpenAICompat runs `vibeflow launch --provider openai-compatible`
// end to end on a real tmux server with a fake agent recording argv and env.
func TestLaunchCmd_OpenAICompat(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	t.Chdir(repo)
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv("OPENAI_API_KEY", "sk-shell-openai") // must never reach the pane
	t.Setenv("OPENAI_COMPAT_API_KEY_EXAMPLE_VENDOR", "sk-vendor-from-shell")

	socket := fmt.Sprintf("vftest-oacompat-launch-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-qwen")
	script := "#!/bin/sh\n{ printf 'ARG=%s\\n' \"$@\"; printf 'KEY=%s\\nURL=%s\\nMODEL=%s\\n' \"$OPENAI_API_KEY\" \"$OPENAI_BASE_URL\" \"$OPENAI_MODEL\"; } > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	prov := cfg.Providers["openai-compatible"]
	prov.Binary = binary
	cfg.Providers["openai-compatible"] = prov
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}

	cmd := launchCmd()
	cmd.SilenceErrors, cmd.SilenceUsage = true, true
	cmd.SetArgs([]string{"--provider", "openai-compatible", "--base-url", "http://llm-proxy.local:4000/v1", "--vendor", "example-vendor", "--model", "some-model"})
	if err := cmd.Execute(); err != nil {
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
		"ARG=--auth-type\nARG=openai\n",
		"ARG=--openai-base-url\nARG=http://llm-proxy.local:4000/v1\n",
		"ARG=--model\nARG=some-model\n",
		"KEY=sk-vendor-from-shell\n",
		"URL=http://llm-proxy.local:4000/v1\n",
		"MODEL=some-model\n",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("agent record missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-shell-openai") {
		t.Errorf("shell OPENAI_API_KEY reached the session:\n%s", got)
	}

	// Metadata keeps the endpoint for restart; the key is never stored.
	metas, err := NewStore().List()
	if err != nil || len(metas) != 1 {
		t.Fatalf("stored sessions = %v, %v", metas, err)
	}
	m := metas[0]
	if m.Vendor != "example-vendor" || m.BaseURL != "http://llm-proxy.local:4000/v1" || m.Model != "some-model" {
		t.Errorf("meta endpoint = %q %q %q", m.Vendor, m.BaseURL, m.Model)
	}
	raw, err := os.ReadFile(DefaultStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-vendor-from-shell") {
		t.Error("API key written to sessions.json")
	}
}
