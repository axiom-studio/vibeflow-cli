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
	"path/filepath"
	"strings"
	"time"
)

// copilotUserConfigPath returns Copilot CLI's user config file
// (~/.copilot/config.json), which persists first-run state: the
// trusted-folders list, the desktop-app install nudge, and the app tip.
// COPILOT_HOME overrides are intentionally not resolved here — vibeflow
// targets the default location, matching the fixed-path precedent of every
// other agent config writer (see bootstrap.go path resolvers).
func copilotUserConfigPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("resolve home directory: %w", err)
	}
	return filepath.Join(home, ".copilot", "config.json"), nil
}

// EnsureCopilotFirstRunConfig pre-seeds Copilot CLI's first-run state so an
// unattended vibeflow tmux launch never stalls on a blocking dialog.
// Verified against copilot v1.0.79: a fresh folder triggers a folder-trust
// prompt, and a fresh install additionally shows a one-time desktop-app
// install nudge; both block until answered and both persist to
// ~/.copilot/config.json (trustedFolders / appInstallNudgeResponded /
// appTipShown / firstLaunchAt). Pre-seeding those keys skips the dialogs.
//
// The merge is additive and sibling-preserving: unknown keys are kept
// verbatim, trustedFolders only ever gains the session workDir, and
// firstLaunchAt is only set when absent. Copilot writes `//` comment header
// lines into this file; they are stripped before parsing and not written
// back — copilot v1.0.79 accepts a comment-less file and re-adds its header
// on its next own write.
//
// Returns whether the file was modified.
func EnsureCopilotFirstRunConfig(workDir string) (bool, error) {
	path, err := copilotUserConfigPath()
	if err != nil {
		return false, err
	}
	// Launch paths may pass a relative workDir (e.g. "." when launching from
	// the project directory). Copilot matches trustedFolders against the
	// absolute folder path, so a relative entry would never match and the
	// trust dialog would still fire — caught live during feature #667 E2E.
	workDir, err = filepath.Abs(workDir)
	if err != nil {
		return false, fmt.Errorf("resolve absolute path of %s: %w", workDir, err)
	}
	root, err := readJSONObjectStripComments(path)
	if err != nil {
		return false, err
	}

	changed := false

	// Trust the session working directory.
	folders, _ := root["trustedFolders"].([]any)
	trusted := false
	for _, f := range folders {
		if s, ok := f.(string); ok && s == workDir {
			trusted = true
			break
		}
	}
	if !trusted {
		root["trustedFolders"] = append(folders, workDir)
		changed = true
	}

	// Suppress the one-time first-run dialogs/tips. Forcing true (rather
	// than only-when-missing) is deliberate: these are "already shown /
	// already answered" markers, and suppressing them is the entire point
	// of an unattended launch.
	for _, key := range []string{"appInstallNudgeResponded", "appTipShown"} {
		if v, ok := root[key].(bool); !ok || !v {
			root[key] = true
			changed = true
		}
	}

	// firstLaunchAt is informational; only seed it when absent.
	if _, ok := root["firstLaunchAt"]; !ok {
		root["firstLaunchAt"] = time.Now().UTC().Format(time.RFC3339)
		changed = true
	}

	if !changed {
		return false, nil
	}

	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return false, fmt.Errorf("create %s: %w", dir, err)
	}
	// Ensure directory mode is strict even if it already existed with wider perms.
	if err := os.Chmod(dir, 0o700); err != nil {
		return false, fmt.Errorf("chmod %s: %w", dir, err)
	}
	data, err := json.MarshalIndent(root, "", "  ")
	if err != nil {
		return false, fmt.Errorf("marshal %s: %w", path, err)
	}
	data = append(data, '\n')
	// Atomic write: write to a temp file in the same directory and rename.
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return false, fmt.Errorf("write tmp %s: %w", tmp, err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return false, fmt.Errorf("rename %s -> %s: %w", tmp, path, err)
	}
	return true, nil
}

// readJSONObjectStripComments reads a JSON object file that may carry
// whole-line `//` comments (copilot's config.json header). A missing or
// empty file yields an empty object, matching readJSONObject's semantics.
func readJSONObjectStripComments(path string) (map[string]any, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return map[string]any{}, nil
		}
		return nil, fmt.Errorf("read %s: %w", path, err)
	}
	var kept []string
	for line := range strings.SplitSeq(string(data), "\n") {
		if strings.HasPrefix(strings.TrimSpace(line), "//") {
			continue
		}
		kept = append(kept, line)
	}
	body := strings.TrimSpace(strings.Join(kept, "\n"))
	if body == "" {
		return map[string]any{}, nil
	}
	var root map[string]any
	if err := json.Unmarshal([]byte(body), &root); err != nil {
		return nil, fmt.Errorf("parse %s: %w", path, err)
	}
	if root == nil {
		root = map[string]any{}
	}
	return root, nil
}
