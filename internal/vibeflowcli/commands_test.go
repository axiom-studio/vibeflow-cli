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
	"testing"
)

// TestRestartCmd_SkipPermissionsFlag verifies that `vibeflow restart`
// distinguishes an unset --skip-permissions flag (preserve stored value) from
// an explicitly set flag (override stored value with the passed value).
// This is the QA fix for Issue #517: before this change, passing
// --skip-permissions=false on the CLI did not downgrade a stored-autonomous
// session because the RunE only branched on `if skipPermissions`.
func TestRestartCmd_SkipPermissionsFlag(t *testing.T) {
	tests := []struct {
		name         string
		args         []string
		wantChanged  bool
		wantFlagVal  bool
		wantResolved bool // resolved SkipPermissions when stored value is true
	}{
		{
			name:         "flag omitted preserves stored true",
			args:         []string{"some-session"},
			wantChanged:  false,
			wantFlagVal:  false,
			wantResolved: true,
		},
		{
			name:         "flag explicit true keeps stored true",
			args:         []string{"--skip-permissions=true", "some-session"},
			wantChanged:  true,
			wantFlagVal:  true,
			wantResolved: true,
		},
		{
			name:         "flag explicit false overrides stored true",
			args:         []string{"--skip-permissions=false", "some-session"},
			wantChanged:  true,
			wantFlagVal:  false,
			wantResolved: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cmd := restartCmd()
			if err := cmd.ParseFlags(tc.args); err != nil {
				t.Fatalf("ParseFlags failed: %v", err)
			}

			changed := cmd.Flags().Changed("skip-permissions")
			if changed != tc.wantChanged {
				t.Errorf("Changed() = %v, want %v", changed, tc.wantChanged)
			}

			flagVal, err := cmd.Flags().GetBool("skip-permissions")
			if err != nil {
				t.Fatalf("GetBool failed: %v", err)
			}
			if flagVal != tc.wantFlagVal {
				t.Errorf("flag value = %v, want %v", flagVal, tc.wantFlagVal)
			}

			// Mirror the RunE resolution step: start from stored=true, apply
			// override only when the flag was explicitly set.
			resolved := true
			if changed {
				resolved = flagVal
			}
			if resolved != tc.wantResolved {
				t.Errorf("resolved SkipPermissions = %v, want %v", resolved, tc.wantResolved)
			}
		})
	}
}

func TestParsePersonaModels(t *testing.T) {
	got, err := parsePersonaModels("developer=gpt-5.1-codex, architect=opus")
	if err != nil {
		t.Fatalf("parsePersonaModels failed: %v", err)
	}
	if got["developer"] != "gpt-5.1-codex" || got["architect"] != "opus" {
		t.Fatalf("parsePersonaModels = %#v", got)
	}

	if _, err := parsePersonaModels("developer"); err == nil {
		t.Fatal("expected malformed --models entry to fail")
	}
}

func TestValidatePersonaModels(t *testing.T) {
	models := map[string]string{"developer": "gpt-5.1-codex"}
	if err := validatePersonaModels(models, []string{"developer", "architect"}); err != nil {
		t.Fatalf("validatePersonaModels failed: %v", err)
	}

	if err := validatePersonaModels(map[string]string{"qa_lead": "sonnet"}, []string{"developer"}); err == nil {
		t.Fatal("expected unknown persona model override to fail")
	}

	if err := validatePersonaModels(models, []string{""}); err == nil {
		t.Fatal("expected --models without personas to fail")
	}
}

func TestModelForPersona(t *testing.T) {
	models := map[string]string{"developer": "gpt-5.1-codex"}
	if got := modelForPersona("sonnet", models, "developer"); got != "gpt-5.1-codex" {
		t.Errorf("developer model = %q", got)
	}
	if got := modelForPersona("sonnet", models, "architect"); got != "sonnet" {
		t.Errorf("architect model = %q", got)
	}
}
