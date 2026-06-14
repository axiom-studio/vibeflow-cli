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

import "fmt"

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

func ValidateModelForProvider(provider, model string) error {
	if model == "" {
		return nil
	}
	for _, option := range ModelsForProvider(provider) {
		if model == option.ID {
			return nil
		}
	}
	if len(ModelsForProvider(provider)) == 0 {
		return nil
	}
	return fmt.Errorf("unknown model %q for provider %q: run `vibeflow models %s` to list supported ids", model, provider, provider)
}
