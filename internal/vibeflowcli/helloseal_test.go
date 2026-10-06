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
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/spf13/cobra"
)

// modelListServer is a real OpenAI-compatible /v1/models endpoint that
// requires a bearer key (when key is non-empty) and records every request.
type modelListServer struct {
	*httptest.Server
	mu       sync.Mutex
	requests []string // "<path> auth=<Authorization header>"
}

func newModelListServer(t *testing.T, key string, models ...string) *modelListServer {
	t.Helper()
	s := &modelListServer{}
	s.Server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s.mu.Lock()
		s.requests = append(s.requests, r.URL.Path+" auth="+r.Header.Get("Authorization"))
		s.mu.Unlock()
		if r.URL.Path != "/v1/models" {
			http.NotFound(w, r)
			return
		}
		if key != "" && r.Header.Get("Authorization") != "Bearer "+key {
			w.WriteHeader(http.StatusUnauthorized)
			fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[`)
		for i, m := range models {
			if i > 0 {
				fmt.Fprint(w, ",")
			}
			fmt.Fprintf(w, `{"id":%q,"object":"model","owned_by":"helloseal"}`, m)
		}
		fmt.Fprint(w, `]}`)
	}))
	t.Cleanup(s.Close)
	return s
}

func (s *modelListServer) seen() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]string(nil), s.requests...)
}

func TestFetchOpenAICompatModels_ListsModelsSortedWithBearerAuth(t *testing.T) {
	srv := newModelListServer(t, "sk-test", "zeta", "alpha")
	for _, base := range []string{srv.URL + "/v1", srv.URL + "/v1/", srv.URL} {
		got, err := FetchOpenAICompatModels(context.Background(), "HelloSeal", base, "sk-test")
		if err != nil {
			t.Fatalf("base %q: %v", base, err)
		}
		if len(got) != 2 || got[0].ID != "alpha" || got[1].ID != "zeta" || got[0].Description != "helloseal" {
			t.Errorf("base %q: models = %+v, want alpha then zeta owned by helloseal", base, got)
		}
	}
	// Every base shape reaches /v1/models exactly once (never /v1/v1/models)
	// with the key as a bearer token.
	for _, req := range srv.seen() {
		if req != "/v1/models auth=Bearer sk-test" {
			t.Errorf("request = %q, want /v1/models with the bearer key", req)
		}
	}
	if n := len(srv.seen()); n != 3 {
		t.Errorf("requests = %d, want 3", n)
	}
}

func TestFetchOpenAICompatModels_RejectedKeyIsAnErrorThatNamesNoSecret(t *testing.T) {
	srv := newModelListServer(t, "sk-right", "alpha")
	_, err := FetchOpenAICompatModels(context.Background(), "HelloSeal", srv.URL+"/v1", "sk-wrong")
	if err == nil {
		t.Fatal("a rejected key must be an error, not an empty or fallback list")
	}
	msg := err.Error()
	if !strings.Contains(msg, "HelloSeal") || !strings.Contains(msg, "rejected the API key (HTTP 401)") {
		t.Errorf("error = %q, want it to name HelloSeal and the rejected key", msg)
	}
	if strings.Contains(msg, "sk-wrong") {
		t.Errorf("error leaks the key: %q", msg)
	}
	// No key at all is reported the same way, so a keyless attempt against a
	// keyed endpoint is never mistaken for an empty catalog.
	if _, err := FetchOpenAICompatModels(context.Background(), "HelloSeal", srv.URL+"/v1", ""); err == nil || !strings.Contains(err.Error(), "rejected the API key") {
		t.Errorf("no key: err = %v, want a rejected-key error", err)
	}
}

func TestFetchOpenAICompatModels_UnreachableEmptyAndBrokenAreErrors(t *testing.T) {
	cases := []struct {
		name    string
		handler http.HandlerFunc
		want    string
	}{
		{"empty list", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"object":"list","data":[]}`) }, "returned no models"},
		{"server error", func(w http.ResponseWriter, _ *http.Request) { w.WriteHeader(http.StatusBadGateway) }, "returned HTTP 502"},
		{"not json", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, "<html>login</html>") }, "unreadable model list"},
		{"entries without ids", func(w http.ResponseWriter, _ *http.Request) { fmt.Fprint(w, `{"data":[{"object":"model"}]}`) }, "returned no models"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(tc.handler)
			defer srv.Close()
			_, err := FetchOpenAICompatModels(context.Background(), "HelloSeal", srv.URL+"/v1", "k")
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("err = %v, want it to contain %q", err, tc.want)
			}
		})
	}
	t.Run("unreachable", func(t *testing.T) {
		srv := httptest.NewServer(http.NotFoundHandler())
		url := srv.URL
		srv.Close()
		_, err := FetchOpenAICompatModels(context.Background(), "HelloSeal", url+"/v1", "k")
		if err == nil || !strings.Contains(err.Error(), "HelloSeal") || !strings.Contains(err.Error(), "is unreachable") {
			t.Errorf("err = %v, want an unreachable error naming HelloSeal", err)
		}
	})
}

func TestResolveHelloSealBaseURL_EnvWinsOverRemembered(t *testing.T) {
	t.Setenv(helloSealBaseURLEnv, "")
	if got := ResolveHelloSealBaseURL(nil); got != "" {
		t.Errorf("nil config, no env: %q, want empty", got)
	}
	cfg := &Config{}
	if got := ResolveHelloSealBaseURL(cfg); got != "" {
		t.Errorf("empty config: %q, want empty", got)
	}
	cfg.RememberEndpoint(helloSealProvider, "https://remembered.example/v1", helloSealVendor, "m")
	if got := ResolveHelloSealBaseURL(cfg); got != "https://remembered.example/v1" {
		t.Errorf("remembered: %q", got)
	}
	// Another harness's remembered endpoint is never offered as HelloSeal.
	other := &Config{}
	other.RememberEndpoint("qwen", "https://proxy.example/v1", "acme", "m")
	if got := ResolveHelloSealBaseURL(other); got != "" {
		t.Errorf("another harness's endpoint offered as HelloSeal: %q", got)
	}
	t.Setenv(helloSealBaseURLEnv, " https://env.example/v1 ")
	if got := ResolveHelloSealBaseURL(cfg); got != "https://env.example/v1" {
		t.Errorf("env override: %q", got)
	}
}

func TestHelloSealProvider_DefaultsRunTheQwenHarness(t *testing.T) {
	cfg := DefaultConfig()
	hs, ok := cfg.Providers[helloSealProvider]
	if !ok {
		t.Fatal("helloseal missing from the built-in providers")
	}
	qwen := cfg.Providers["qwen"]
	if hs.Name != "HelloSeal" || hs.Binary != "qwen" || hs.LaunchTemplate != qwen.LaunchTemplate || hs.Default {
		t.Errorf("helloseal provider = %+v, want HelloSeal on the qwen binary with qwen's launch template", hs)
	}
	for key, want := range map[string]bool{"helloseal": true, "qwen": true, "codex": false, "copilot": false, "": false} {
		if usesQwenHarness(key) != want {
			t.Errorf("usesQwenHarness(%q) = %v, want %v", key, !want, want)
		}
	}
	if providerDocFile[helloSealProvider] != "QWEN.md" {
		t.Errorf("helloseal reads %q, want QWEN.md like the qwen binary", providerDocFile[helloSealProvider])
	}
	if format, ok := EndpointAPIFormat(helloSealProvider); !ok || format != "OpenAI-compatible" {
		t.Errorf("EndpointAPIFormat(helloseal) = %q, %v", format, ok)
	}
	// Same prompt shape and launch flags as qwen, because the same binary runs.
	if got := AppendVibeflowInitPrompt("qwen --yolo", helloSealProvider, "go"); got != "qwen --yolo -i 'go'" {
		t.Errorf("init prompt = %q, want qwen's -i shape", got)
	}
	env := map[string]string{"OPENAI_BASE_URL": "https://hs.example/v1", "OPENAI_MODEL": "m1", "OPENAI_API_KEY": "sk-secret"}
	got := AppendQwenAPIFlags("qwen --yolo", helloSealProvider, env)
	if !strings.Contains(got, "--openai-base-url 'https://hs.example/v1'") || !strings.Contains(got, "--model 'm1'") {
		t.Errorf("flags = %q, want --openai-base-url and --model", got)
	}
	if strings.Contains(got, "sk-secret") {
		t.Errorf("key on the command line: %q", got)
	}
	sessionEnv := map[string]string{}
	t.Setenv("OPENAI_MODEL", "from-shell")
	applyQwenModelPassthrough(helloSealProvider, sessionEnv)
	if sessionEnv["OPENAI_MODEL"] != "from-shell" {
		t.Errorf("model passthrough skipped helloseal: %v", sessionEnv)
	}
}

func TestHelloSealRouting_OnlyEndpointIsSupported(t *testing.T) {
	isolateRoutingEnv(t)
	want := map[string]CellStatus{RoutingDirect: Unsupported, RoutingGateway: Unsupported, RoutingEndpoint: Supported, RoutingShell: Unsupported}
	for mode, status := range want {
		c := FindCell(helloSealProvider, mode)
		if c == nil || c.Status != status {
			t.Errorf("helloseal × %s = %+v, want %s", mode, c, status)
			continue
		}
		if status == Unsupported && c.Reason == "" {
			t.Errorf("helloseal × %s has no reason", mode)
		}
		if derivedSupport(helloSealProvider, mode) != (status == Supported) {
			t.Errorf("derivedSupport(helloseal, %s) disagrees with the cell", mode)
		}
	}
	// Direct stays supported for every real harness.
	for _, key := range []string{"claude", "codex", "copilot", "cursor", "gemini", "kiro", "qwen", "custom"} {
		if !derivedSupport(key, RoutingDirect) {
			t.Errorf("derivedSupport(%s, direct) = false", key)
		}
	}
	if providerSupportsGateway(helloSealProvider) || !strings.Contains(gatewayUnsupportedReason(helloSealProvider, "HelloSeal"), "model endpoint") {
		t.Error("the gateway must be unavailable for helloseal with a specific reason")
	}

	// The endpoint wiring is the qwen wiring: same env, same flags.
	cfg := DefaultConfig()
	hsEnv := BuildEndpointEnv(helloSealProvider, cfg, helloSealVendor, testEndpointURL, "m")
	qwenEnv := BuildEndpointEnv("qwen", cfg, helloSealVendor, testEndpointURL, "m")
	if len(hsEnv) != 3 || fmt.Sprint(hsEnv) != fmt.Sprint(qwenEnv) {
		t.Errorf("helloseal endpoint env = %v, want qwen's %v", hsEnv, qwenEnv)
	}
	if got, want := AppendEndpointFlags("qwen", helloSealProvider, testEndpointURL), AppendEndpointFlags("qwen", "qwen", testEndpointURL); got != want {
		t.Errorf("helloseal endpoint flags = %q, want qwen's %q", got, want)
	}

	// Headless flags: only endpoint routing passes.
	url := "http://hs.local:4000/v1"
	for _, tc := range []struct {
		name                  string
		routing               string
		llmGateway            bool
		baseURL, vendor, want string
	}{
		{"direct rejected", RoutingDirect, false, "", "", "cannot connect directly"},
		{"gateway rejected", RoutingGateway, false, "", "", "cannot route through"},
		{"llm-gateway flag rejected", RoutingGateway, true, "", "", "cannot route through"},
		{"shell rejected", RoutingShell, false, "", "", "cannot use an endpoint from the shell"},
		{"endpoint accepted", RoutingEndpoint, false, url, helloSealVendor, ""},
	} {
		err := validateRoutingFlags(helloSealProvider, tc.routing, tc.llmGateway, tc.baseURL, tc.vendor, "m")
		if tc.want == "" {
			if err != nil {
				t.Errorf("%s: unexpected error %v", tc.name, err)
			}
			continue
		}
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", tc.name, err, tc.want)
		}
	}
}

// helloSealWizard returns a vanilla wizard on the provider step with HelloSeal
// selected and no HelloSeal state leaking in from the host shell.
func helloSealWizard(t *testing.T, cfg *Config) WizardModel {
	t.Helper()
	t.Setenv(helloSealBaseURLEnv, "")
	t.Setenv(OpenAICompatKeyEnvName(helloSealVendor), "")
	return endpointWizardFixture(t, cfg, helloSealProvider)
}

// fetchHelloSealModels presses enter on the key row and runs the fetch the
// wizard scheduled, returning the wizard after it received the result.
func fetchHelloSealModels(t *testing.T, w WizardModel) WizardModel {
	t.Helper()
	w, cmd := w.Update(keyEnter)
	if cmd == nil {
		t.Fatalf("enter on the key row scheduled no fetch (err %q)", w.hsErr)
	}
	if !w.hsLoading || !strings.Contains(w.View(), "Fetching models from HelloSeal") {
		t.Fatalf("wizard is not in the loading state:\n%s", w.View())
	}
	w, _ = w.Update(cmd())
	return w
}

func TestWizard_HelloSealSkipsRoutingAndSelectsALiveModel(t *testing.T) {
	srv := newModelListServer(t, "sk-test", "zeta", "alpha")
	cfg := DefaultConfig()
	w := helloSealWizard(t, cfg)

	// Choosing HelloSeal goes straight to its step with endpoint routing
	// pinned: there is no Routing step and no Endpoint step.
	w, _ = w.advance()
	if w.step != StepHelloSealConfig || w.routing != RoutingEndpoint || !w.routingChosen || w.llmGatewayEnabled {
		t.Fatalf("after provider: step=%v routing=%q chosen=%v gateway=%v", w.step, w.routing, w.routingChosen, w.llmGatewayEnabled)
	}
	view := w.View()
	for _, want := range []string{"[HelloSeal]", "Connect to HelloSeal", "Base URL", "API key", "fetched live from <base URL>/v1/models"} {
		if !strings.Contains(view, want) {
			t.Errorf("HelloSeal view missing %q:\n%s", want, view)
		}
	}
	for _, absent := range []string{"Routing", "Endpoint"} {
		if strings.Contains(view, absent) {
			t.Errorf("HelloSeal view still shows the %s step label:\n%s", absent, view)
		}
	}

	// Letters type into the rows; a bad URL is caught before any request.
	w = typeText(w, "ftp://nope")
	w = press(w, keyTab)
	w = typeText(w, "sk-test")
	if got, cmd := w.Update(keyEnter); cmd != nil || !strings.Contains(got.hsErr, "http(s) URL") {
		t.Fatalf("invalid URL: cmd=%v err=%q, want no fetch and a URL error", cmd, got.hsErr)
	}
	w.hsInputs[hsRowBaseURL] = srv.URL + "/v1"
	w.cursor = hsRowAPIKey
	w = fetchHelloSealModels(t, w)
	if w.hsErr != "" || len(w.hsModels) != 2 || w.hsModels[0].ID != "alpha" {
		t.Fatalf("after fetch: err=%q models=%+v", w.hsErr, w.hsModels)
	}
	view = w.View()
	for _, want := range []string{"Select a HelloSeal model", "2 models listed by", "> alpha", "  zeta"} {
		if !strings.Contains(view, want) {
			t.Errorf("model list view missing %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "sk-test") {
		t.Errorf("key echoed in the view:\n%s", view)
	}

	// Pick the second model.
	w = press(w, tea.KeyPressMsg{Code: 'j', Text: "j"})
	w = press(w, keyEnter)
	if w.step != StepBranch {
		t.Fatalf("after selecting a model: step = %v, want Branch", w.step)
	}
	if w.oacValue(oacRowBaseURL) != srv.URL+"/v1" || w.oacValue(oacRowVendor) != helloSealVendor || w.oacValue(oacRowModel) != "zeta" {
		t.Errorf("endpoint state = %q %q %q", w.oacValue(oacRowBaseURL), w.oacValue(oacRowVendor), w.oacValue(oacRowModel))
	}
	if w.hsInputs[hsRowAPIKey] != "" {
		t.Error("typed key kept in wizard state after it was stored")
	}

	// Saved the same way the Endpoint step saves: the remembered endpoint
	// under the provider key and the key in HelloSeal's own slot, on disk.
	rec := cfg.OpenAICompat.Recent[helloSealProvider]
	if rec.BaseURL != srv.URL+"/v1" || rec.Vendor != helloSealVendor || rec.Model != "zeta" || cfg.OpenAICompat.LastProvider != helloSealProvider {
		t.Errorf("remembered endpoint = %+v (last provider %q)", rec, cfg.OpenAICompat.LastProvider)
	}
	saved, err := LoadConfig(ConfigPath())
	if err != nil {
		t.Fatal(err)
	}
	if saved.SavedEnvVars["OPENAI_COMPAT_API_KEY_HELLOSEAL"] != "sk-test" || saved.OpenAICompat.Recent[helloSealProvider].Model != "zeta" {
		t.Errorf("config on disk: key slot %q, model %q", saved.SavedEnvVars["OPENAI_COMPAT_API_KEY_HELLOSEAL"], saved.OpenAICompat.Recent[helloSealProvider].Model)
	}

	// Back from Branch returns to the list with the chosen model under the cursor.
	back := press(w, keyEsc)
	if back.step != StepHelloSealConfig || back.hsModels == nil || back.cursor != 1 {
		t.Errorf("back from branch: step=%v models=%v cursor=%d", back.step, back.hsModels != nil, back.cursor)
	}

	// Confirm shows HelloSeal as the routing and the result carries the endpoint.
	w.selectedBranch = 1
	w.step = StepConfirm
	w.cursor = 0
	if view := w.View(); !strings.Contains(view, "HelloSeal (compatible endpoint)") || !strings.Contains(view, "Model:         zeta") || strings.Contains(view, "sk-test") {
		t.Errorf("confirm view:\n%s", view)
	}
	w, _ = w.advance()
	r := w.Result()
	if r.ProviderKey != helloSealProvider || r.Routing != RoutingEndpoint || r.Vendor != helloSealVendor || r.BaseURL != srv.URL+"/v1" || r.Model != "zeta" {
		t.Errorf("result = provider %q routing %q vendor %q url %q model %q", r.ProviderKey, r.Routing, r.Vendor, r.BaseURL, r.Model)
	}
}

func TestWizard_HelloSealFetchFailureStaysOnStepWithoutFallback(t *testing.T) {
	srv := newModelListServer(t, "sk-right", "alpha")
	cfg := DefaultConfig()
	w := helloSealWizard(t, cfg)
	w, _ = w.advance()
	w = typeText(w, srv.URL+"/v1")
	w = press(w, keyTab)
	w = typeText(w, "sk-wrong")
	w = fetchHelloSealModels(t, w)
	if w.step != StepHelloSealConfig || w.hsModels != nil || w.hsLoading {
		t.Fatalf("after a rejected key: step=%v models=%v loading=%v, want to stay on the connection rows", w.step, w.hsModels, w.hsLoading)
	}
	if !strings.Contains(w.hsErr, "rejected the API key") || strings.Contains(w.hsErr, "sk-wrong") {
		t.Errorf("hsErr = %q", w.hsErr)
	}
	if !strings.Contains(w.View(), "✗ HelloSeal") {
		t.Errorf("error not shown:\n%s", w.View())
	}
	if _, ok := cfg.SavedEnvVars["OPENAI_COMPAT_API_KEY_HELLOSEAL"]; ok || cfg.OpenAICompat.LastProvider != "" {
		t.Error("a rejected key or an unverified endpoint was saved")
	}
	// The typed key stays so it can be corrected; a retry with the right key works.
	if w.hsInputs[hsRowAPIKey] != "sk-wrong" {
		t.Errorf("typed key = %q after the failure", w.hsInputs[hsRowAPIKey])
	}
	w.hsInputs[hsRowAPIKey] = "sk-right"
	w = fetchHelloSealModels(t, w)
	if w.hsErr != "" || len(w.hsModels) != 1 {
		t.Errorf("retry: err=%q models=%v", w.hsErr, w.hsModels)
	}

	// An unreachable endpoint is reported the same way and leaves nothing chosen.
	dead := httptest.NewServer(http.NotFoundHandler())
	deadURL := dead.URL
	dead.Close()
	w = press(w, keyEsc) // list → connection rows
	w.hsInputs[hsRowBaseURL] = deadURL + "/v1"
	w.cursor = hsRowAPIKey
	w = fetchHelloSealModels(t, w)
	if w.hsModels != nil || !strings.Contains(w.hsErr, "is unreachable") {
		t.Errorf("unreachable: models=%v err=%q", w.hsModels, w.hsErr)
	}
	// Esc from the connection rows goes back to the provider list, since
	// HelloSeal replaced the Routing step.
	if w = press(w, keyEsc); w.step != StepProvider {
		t.Errorf("esc: step = %v, want Provider", w.step)
	}
}

func TestWizard_HelloSealPrefillsBaseURLAndReusesTheSavedKey(t *testing.T) {
	srv := newModelListServer(t, "sk-saved", "zeta", "alpha", "beta")
	cfg := DefaultConfig()
	cfg.SavedEnvVars = map[string]string{"OPENAI_COMPAT_API_KEY_HELLOSEAL": "sk-saved"}
	cfg.RememberEndpoint(helloSealProvider, srv.URL+"/v1", helloSealVendor, "zeta")
	w := helloSealWizard(t, cfg)
	w, _ = w.advance()
	if w.hsInputs[hsRowBaseURL] != srv.URL+"/v1" || w.hsInputs[hsRowAPIKey] != "" {
		t.Fatalf("prefill: url=%q key=%q, want the remembered URL and an empty key row", w.hsInputs[hsRowBaseURL], w.hsInputs[hsRowAPIKey])
	}
	if !strings.Contains(w.View(), "a key is already saved for HelloSeal") {
		t.Errorf("saved-key hint missing:\n%s", w.View())
	}
	// Enter on the URL row moves down; enter on the blank key row fetches
	// with the saved key.
	w = press(w, keyEnter)
	if w.cursor != hsRowAPIKey {
		t.Fatalf("enter on the URL row: cursor = %d", w.cursor)
	}
	w = fetchHelloSealModels(t, w)
	if w.hsErr != "" || len(w.hsModels) != 3 {
		t.Fatalf("fetch with saved key: err=%q models=%v", w.hsErr, w.hsModels)
	}
	if got := srv.seen(); len(got) != 1 || got[0] != "/v1/models auth=Bearer sk-saved" {
		t.Errorf("requests = %v, want one with the saved key", got)
	}
	// The last-used model is pre-selected and marked.
	if w.cursor != 2 || w.hsModels[w.cursor].ID != "zeta" || !strings.Contains(w.View(), "zeta") || !strings.Contains(w.View(), "← last used") {
		t.Errorf("cursor=%d view:\n%s", w.cursor, w.View())
	}
	// The env override wins over the remembered URL on a fresh wizard.
	t.Setenv(helloSealBaseURLEnv, "https://env.example/v1")
	fresh := endpointWizardFixture(t, cfg, helloSealProvider)
	fresh, _ = fresh.advance()
	if fresh.hsInputs[hsRowBaseURL] != "https://env.example/v1" {
		t.Errorf("env prefill = %q", fresh.hsInputs[hsRowBaseURL])
	}
}

func TestWizard_HelloSealLateFetchResultIsIgnoredAfterBackingOut(t *testing.T) {
	srv := newModelListServer(t, "", "alpha")
	w := helloSealWizard(t, DefaultConfig())
	w, _ = w.advance()
	w = typeText(w, srv.URL+"/v1")
	w.cursor = hsRowAPIKey
	w, cmd := w.Update(keyEnter)
	if cmd == nil || !w.hsLoading {
		t.Fatal("no fetch scheduled")
	}
	// Esc while loading abandons the fetch and returns to the provider list.
	w = press(w, keyEsc)
	if w.step != StepProvider || w.hsLoading {
		t.Fatalf("esc while loading: step=%v loading=%v", w.step, w.hsLoading)
	}
	w, _ = w.Update(cmd())
	if w.step != StepProvider || w.hsModels != nil {
		t.Errorf("late result changed the wizard: step=%v models=%v", w.step, w.hsModels)
	}
}

func TestListWindow_KeepsCursorVisible(t *testing.T) {
	for _, tc := range []struct{ cursor, n, size, start, end int }{
		{0, 5, 12, 0, 5}, {0, 30, 12, 0, 12}, {29, 30, 12, 18, 30}, {15, 30, 12, 9, 21}, {11, 12, 12, 0, 12},
	} {
		start, end := listWindow(tc.cursor, tc.n, tc.size)
		if start != tc.start || end != tc.end || tc.cursor < start || tc.cursor >= end {
			t.Errorf("listWindow(%d, %d, %d) = [%d, %d), want [%d, %d)", tc.cursor, tc.n, tc.size, start, end, tc.start, tc.end)
		}
	}
}

func TestModelsCmd_HelloSealListsLive(t *testing.T) {
	t.Setenv("VIBEFLOW_ROOT", t.TempDir())
	srv := newModelListServer(t, "sk-test", "zeta", "alpha")
	t.Setenv(helloSealBaseURLEnv, "")
	t.Setenv(OpenAICompatKeyEnvName(helloSealVendor), "sk-test")

	if _, err := runModelsCmd(t, []string{helloSealProvider}); err == nil || !strings.Contains(err.Error(), helloSealBaseURLEnv) {
		t.Errorf("without a base URL: err = %v, want it to name %s", err, helloSealBaseURLEnv)
	}
	t.Setenv(helloSealBaseURLEnv, srv.URL+"/v1")
	out, err := runModelsCmd(t, []string{helloSealProvider})
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"helloseal (live from", "alpha", "zeta"} {
		if !strings.Contains(out, want) {
			t.Errorf("output missing %q:\n%s", want, out)
		}
	}
	if strings.Contains(out, "sk-test") {
		t.Errorf("key printed:\n%s", out)
	}
	// Listing every provider stays offline: no request, no helloseal section.
	before := len(srv.seen())
	all, err := runModelsCmd(t, nil)
	if err != nil || strings.Contains(all, "helloseal") || len(srv.seen()) != before {
		t.Errorf("`models`: err=%v requests=%d output:\n%s", err, len(srv.seen())-before, all)
	}
}

func TestLaunchCmd_HelloSealDefaultsToItsEndpoint(t *testing.T) {
	if _, err := exec.LookPath("tmux"); err != nil {
		t.Skip("tmux not installed")
	}
	repo := newTestRepo(t, "")
	t.Chdir(repo)
	state := t.TempDir()
	t.Setenv("VIBEFLOW_ROOT", state)
	t.Setenv(helloSealBaseURLEnv, "http://helloseal.local:4000/v1")
	t.Setenv(OpenAICompatKeyEnvName(helloSealVendor), "sk-helloseal-from-shell")
	// Inherited by the tmux server; must never reach the pane.
	t.Setenv("OPENAI_API_KEY", "sk-real-openai")
	t.Setenv("OPENAI_BASE_URL", "https://api.openai.com/v1")
	t.Setenv("MCP_TOKEN", "")

	socket := fmt.Sprintf("vftest-helloseal-launch-%d-%d", os.Getpid(), time.Now().UnixNano())
	tm := NewTmuxManager(socket)
	t.Cleanup(func() { _, _ = tm.run("kill-server") })

	record := filepath.Join(state, "agent-record")
	binary := filepath.Join(state, "fake-qwen")
	script := "#!/bin/sh\nprintf 'URL=%s\\nMODEL=%s\\nKEY=%s\\nARGS=%s\\n' \"$OPENAI_BASE_URL\" \"$OPENAI_MODEL\" \"$OPENAI_API_KEY\" \"$*\" > " + shellQuote(record) + "\nsleep 300\n"
	if err := os.WriteFile(binary, []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := DefaultConfig()
	cfg.TmuxSocket = socket
	prov := cfg.Providers[helloSealProvider]
	prov.Binary = binary
	cfg.Providers[helloSealProvider] = prov
	if err := SaveConfig(cfg, ConfigPath()); err != nil {
		t.Fatal(err)
	}

	run := func(args ...string) error {
		root := &cobra.Command{Use: "vibeflow"}
		root.PersistentFlags().String("config", "", "")
		root.PersistentFlags().String("mcp", "", "")
		root.AddCommand(launchCmd())
		root.SilenceErrors, root.SilenceUsage = true, true
		root.SetArgs(append([]string{"launch", "--provider", helloSealProvider}, args...))
		return root.Execute()
	}
	// Other modes are refused before anything is created.
	if err := run("--routing", "direct", "--model", "m"); err == nil || !strings.Contains(err.Error(), "cannot connect directly") {
		t.Errorf("--routing direct: %v", err)
	}
	if err := run("--llm-gateway", "--model", "m"); err == nil || !strings.Contains(err.Error(), "cannot route through") {
		t.Errorf("--llm-gateway: %v", err)
	}
	t.Setenv(helloSealBaseURLEnv, "")
	if err := run("--model", "m"); err == nil || !strings.Contains(err.Error(), helloSealBaseURLEnv) {
		t.Errorf("no base URL anywhere: %v", err)
	}
	t.Setenv(helloSealBaseURLEnv, "http://helloseal.local:4000/v1")
	if metas, _ := NewStore().List(); len(metas) != 0 {
		t.Fatalf("refused launches created sessions: %v", metas)
	}

	// No --routing, --base-url or --vendor: HelloSeal defaults supply them.
	if err := run("--model", "some-model"); err != nil {
		t.Fatal(err)
	}
	var got string
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if data, err := os.ReadFile(record); err == nil && strings.Contains(string(data), "ARGS=") {
			got = string(data)
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	for _, want := range []string{
		"URL=http://helloseal.local:4000/v1\n",
		"MODEL=some-model\n",
		"KEY=sk-helloseal-from-shell\n",
		"--auth-type openai",
		"--openai-base-url http://helloseal.local:4000/v1",
		"--model some-model",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("agent record missing %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "sk-real-openai") || strings.Contains(got, "api.openai.com") {
		t.Errorf("shell OpenAI values reached the session:\n%s", got)
	}

	// Metadata keeps the HelloSeal endpoint for restart; the key is never stored.
	metas, err := NewStore().List()
	if err != nil || len(metas) != 1 {
		t.Fatalf("stored sessions = %v, %v", metas, err)
	}
	m := metas[0]
	if m.Provider != helloSealProvider || m.Routing != RoutingEndpoint || m.Vendor != helloSealVendor || m.BaseURL != "http://helloseal.local:4000/v1" || m.Model != "some-model" {
		t.Errorf("meta = %q %q %q %q %q", m.Provider, m.Routing, m.Vendor, m.BaseURL, m.Model)
	}
	raw, err := os.ReadFile(DefaultStorePath())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "sk-helloseal-from-shell") {
		t.Error("API key written to sessions.json")
	}
}
