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
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"
)

// ModelOption describes a model id accepted by a built-in provider.
type ModelOption struct {
	ID          string
	Description string
}

var builtInProviderModels = map[string][]ModelOption{
	"claude": {
		{ID: "opus", Description: "Claude Opus alias"},
		{ID: "sonnet", Description: "Claude Sonnet alias"},
	},
	"codex": {
		{ID: "gpt-5.1-codex", Description: "Codex coding model"},
		{ID: "gpt-5-codex", Description: "Codex coding model"},
	},
	"cursor": {
		{ID: "gpt-5.1-codex", Description: "OpenAI coding model"},
		{ID: "sonnet", Description: "Claude Sonnet alias"},
		{ID: "opus", Description: "Claude Opus alias"},
	},
	// copilot: "auto" is the only slug available on every Copilot plan
	// (verified on v1.0.79); paid-plan slugs are plan-gated server-side and
	// fail loudly at startup, so they are added only once enumerated on an
	// entitled account (feature #667 E2E).
	"copilot": {
		{ID: "auto", Description: "Copilot picks the best available model"},
	},
	"gemini": {
		{ID: "gemini-2.5-pro", Description: "Gemini Pro model"},
		{ID: "gemini-2.5-flash", Description: "Gemini Flash model"},
	},
	"qwen": {
		{ID: "qwen3-coder-plus", Description: "Qwen coding model"},
		{ID: "GLM-4.6", Description: "z.ai coding model"},
		{ID: "glm-4.6", Description: "z.ai coding model"},
		{ID: "gpt-4o-mini", Description: "OpenAI-compatible model"},
	},
}

// ModelsForProvider returns a copy of the curated model list for a built-in
// provider. Custom providers intentionally return nil so their model space stays
// unconstrained by vibeflow-cli.
func ModelsForProvider(provider string) []ModelOption {
	options := builtInProviderModels[provider]
	if len(options) == 0 {
		return nil
	}
	out := make([]ModelOption, len(options))
	copy(out, options)
	return out
}

func IsKnownModelForProvider(provider, model string) bool {
	for _, option := range ModelsForProvider(provider) {
		if model == option.ID {
			return true
		}
	}
	return false
}

// modelListTimeout bounds a live model-list request so the wizard never hangs
// on an endpoint that accepts the connection and then stalls.
const modelListTimeout = 15 * time.Second

// modelListMaxBytes caps a model-list response body; a real list is a few KB.
const modelListMaxBytes = 1 << 20

// openAICompatModelList is the shape of an OpenAI-compatible GET /v1/models
// response. Only the fields the picker shows are decoded.
type openAICompatModelList struct {
	Data []struct {
		ID          string `json:"id"`
		OwnedBy     string `json:"owned_by"`
		DisplayName string `json:"display_name"`
	} `json:"data"`
}

// FetchOpenAICompatModels lists the models an OpenAI-compatible endpoint
// serves: GET <root>/v1/models with apiKey as a bearer token (no header when
// the key is empty). The result is sorted by id in the catalog's ModelOption
// shape, so a live list renders exactly like a curated one.
//
// It never falls back. An unreachable host, a rejected key (401/403), any
// other non-2xx status, an unreadable body or an empty list is an error that
// names the endpoint (name plus the credential-free URL) and the failure,
// never the key. baseURL is shaped like the endpoint step's, with or without
// a trailing /v1 (endpointRootURL removes one so the path is never /v1/v1).
func FetchOpenAICompatModels(ctx context.Context, name, baseURL, apiKey string) ([]ModelOption, error) {
	listURL := endpointRootURL(strings.TrimSpace(baseURL)) + "/v1/models"
	where := name + " (" + displayEndpointURL(listURL) + ")"
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, listURL, nil)
	if err != nil {
		return nil, fmt.Errorf("%s: invalid base URL: %w", where, err)
	}
	req.Header.Set("Accept", "application/json")
	if apiKey != "" {
		req.Header.Set("Authorization", "Bearer "+apiKey)
	}
	client := &http.Client{Timeout: modelListTimeout}
	resp, err := client.Do(req)
	if err != nil {
		// url.Error repeats the URL that `where` already carries; keep the
		// cause only.
		var ue *url.Error
		if errors.As(err, &ue) {
			err = ue.Err
		}
		return nil, fmt.Errorf("%s is unreachable: %w", where, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusUnauthorized || resp.StatusCode == http.StatusForbidden:
		return nil, fmt.Errorf("%s rejected the API key (HTTP %d)", where, resp.StatusCode)
	case resp.StatusCode < 200 || resp.StatusCode > 299:
		return nil, fmt.Errorf("%s returned HTTP %d", where, resp.StatusCode)
	}
	var list openAICompatModelList
	if err := json.NewDecoder(io.LimitReader(resp.Body, modelListMaxBytes)).Decode(&list); err != nil {
		return nil, fmt.Errorf("%s returned an unreadable model list: %w", where, err)
	}
	options := make([]ModelOption, 0, len(list.Data))
	for _, m := range list.Data {
		if m.ID == "" {
			continue
		}
		description := m.DisplayName
		if description == "" {
			description = m.OwnedBy
		}
		options = append(options, ModelOption{ID: m.ID, Description: description})
	}
	if len(options) == 0 {
		return nil, fmt.Errorf("%s returned no models", where)
	}
	// O(n log n) over the handful of models an endpoint serves.
	sort.Slice(options, func(i, j int) bool { return options[i].ID < options[j].ID })
	return options, nil
}
