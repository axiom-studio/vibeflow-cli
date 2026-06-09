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

func TestWrapOpenShellCommand_Disabled(t *testing.T) {
	got, err := WrapOpenShellCommand("codex --yolo", OpenShellConfig{})
	if err != nil {
		t.Fatal(err)
	}
	if got != "codex --yolo" {
		t.Errorf("disabled wrapper changed command: %q", got)
	}
}

func TestWrapOpenShellCommand_CreateMode(t *testing.T) {
	got, err := WrapOpenShellCommand("codex --yolo 'Initialize a vibeflow session'", OpenShellConfig{
		Enabled:         true,
		Sandbox:         "vf-main",
		From:            "ghcr.io/nvidia/openshell-community/sandboxes/base",
		Policy:          "/sandbox/policy.yaml",
		Providers:       []string{"openai", "github"},
		NoAutoProviders: true,
		Keep:            true,
		Args:            []string{"--gpu"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "openshell sandbox create --name vf-main --keep --from ghcr.io/nvidia/openshell-community/sandboxes/base --policy /sandbox/policy.yaml --no-auto-providers --provider openai --provider github --gpu -- sh -lc 'codex --yolo '\\''Initialize a vibeflow session'\\'''"
	if got != want {
		t.Errorf("WrapOpenShellCommand create:\n got:  %q\n want: %q", got, want)
	}
}

func TestWrapOpenShellCommand_UseModeRequiresSandbox(t *testing.T) {
	_, err := WrapOpenShellCommand("claude", OpenShellConfig{Enabled: true, Mode: "use"})
	if err == nil {
		t.Fatal("expected sandbox requirement error")
	}
}

func TestWrapOpenShellCommand_UseMode(t *testing.T) {
	got, err := WrapOpenShellCommand("claude 'hello'", OpenShellConfig{
		Enabled: true,
		Mode:    "use",
		Sandbox: "existing-sandbox",
		Args:    []string{"--workspace", "/sandbox"},
	})
	if err != nil {
		t.Fatal(err)
	}
	want := "openshell sandbox use existing-sandbox --workspace /sandbox -- sh -lc 'claude '\\''hello'\\'''"
	if got != want {
		t.Errorf("WrapOpenShellCommand use:\n got:  %q\n want: %q", got, want)
	}
}

func TestWrapOpenShellCommand_RejectsUnsupportedMode(t *testing.T) {
	_, err := WrapOpenShellCommand("claude", OpenShellConfig{Enabled: true, Mode: "connect"})
	if err == nil {
		t.Fatal("expected unsupported mode error")
	}
}
