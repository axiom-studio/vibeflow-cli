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

import "testing"

func TestValidateModelForProvider(t *testing.T) {
	if err := ValidateModelForProvider("claude", "sonnet"); err != nil {
		t.Fatalf("expected sonnet to validate for claude: %v", err)
	}
	if err := ValidateModelForProvider("claude", "definitely-not-a-model"); err == nil {
		t.Fatal("expected unknown built-in provider model to fail")
	}
	if err := ValidateModelForProvider("custom-provider", "whatever-model"); err != nil {
		t.Fatalf("custom providers should not be constrained: %v", err)
	}
}

func TestModelsForProviderReturnsCopy(t *testing.T) {
	got := ModelsForProvider("codex")
	if len(got) == 0 {
		t.Fatal("expected codex models")
	}
	got[0].ID = "mutated"
	if ModelsForProvider("codex")[0].ID == "mutated" {
		t.Fatal("ModelsForProvider returned shared backing storage")
	}
}
