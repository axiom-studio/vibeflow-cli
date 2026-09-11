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
	"fmt"
	"strings"
)

// WrapOpenShellCommand wraps an already-rendered provider command so it runs
// inside an NVIDIA OpenShell sandbox. The provider command must be final before
// calling this helper: launch flags, API flags, and init prompt are all part of
// the command executed inside the sandbox.
func WrapOpenShellCommand(command string, cfg OpenShellConfig) (string, error) {
	if !cfg.Enabled {
		return command, nil
	}
	if strings.TrimSpace(command) == "" {
		return "", fmt.Errorf("wrap openshell command: command is empty")
	}

	binary := cfg.Binary
	if binary == "" {
		binary = "openshell"
	}
	mode := cfg.Mode
	if mode == "" {
		mode = "create"
	}

	args := []string{binary, "sandbox"}
	switch mode {
	case "create":
		args = append(args, "create")
		if cfg.Sandbox != "" {
			args = append(args, "--name", cfg.Sandbox)
		}
		if cfg.Keep {
			args = append(args, "--keep")
		}
		if cfg.From != "" {
			args = append(args, "--from", cfg.From)
		}
		if cfg.Policy != "" {
			args = append(args, "--policy", cfg.Policy)
		}
		if cfg.NoAutoProviders {
			args = append(args, "--no-auto-providers")
		}
		for _, provider := range cfg.Providers {
			if provider != "" {
				args = append(args, "--provider", provider)
			}
		}
		args = append(args, cfg.Args...)
	case "use":
		if cfg.Sandbox == "" {
			return "", fmt.Errorf("wrap openshell command: mode %q requires sandbox", mode)
		}
		args = append(args, "use", cfg.Sandbox)
		args = append(args, cfg.Args...)
	default:
		return "", fmt.Errorf("wrap openshell command: unsupported mode %q", mode)
	}

	args = append(args, "--", "sh", "-lc", command)
	return shellJoin(args), nil
}

func shellJoin(args []string) string {
	quoted := make([]string, len(args))
	for i, arg := range args {
		quoted[i] = shellQuote(arg)
	}
	return strings.Join(quoted, " ")
}

func shellQuote(s string) string {
	if s == "" {
		return "''"
	}
	if strings.IndexFunc(s, func(r rune) bool {
		return !((r >= 'a' && r <= 'z') ||
			(r >= 'A' && r <= 'Z') ||
			(r >= '0' && r <= '9') ||
			strings.ContainsRune("-_./:=@+", r))
	}) == -1 {
		return s
	}
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}
