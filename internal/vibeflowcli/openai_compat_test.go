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
