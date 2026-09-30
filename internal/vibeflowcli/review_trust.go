package vibeflowcli

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Claude and Codex ask "trust this folder?" for every new directory, and a
// review worktree is always new; Claude defaults to "No, exit". Vera
// pre-answers the question for the worktree in the harness's own config, the
// same entry the harness saves when the user accepts (verified 2026-09-30
// against Claude Code 2.1.285 and codex-cli 0.159.2), and forgets it at
// cleanup. Codex's `-c` override is not used: with any `-c`, codex 0.159.2
// fails its interactive bootstrap with HTTP 401.

// trustReviewWorktree pre-accepts folder trust for dir, as the harness sees
// it (symlinks resolved).
func trustReviewWorktree(provider string, env map[string]string, dir string) error {
	if real, err := filepath.EvalSymlinks(dir); err == nil {
		dir = real
	}
	switch provider {
	case "claude":
		return updateClaudeProjects(env, func(projects map[string]json.RawMessage) bool {
			entry := map[string]json.RawMessage{}
			_ = json.Unmarshal(projects[dir], &entry)
			entry["hasTrustDialogAccepted"] = json.RawMessage("true")
			projects[dir], _ = json.Marshal(entry)
			return true
		})
	case "codex":
		path, err := codexConfigFile(env)
		if err != nil {
			return err
		}
		key, _ := json.Marshal(dir) // A TOML basic string.
		header := "[projects." + string(key) + "]"
		return rewriteHarnessConfig(path, func(data []byte) ([]byte, bool, error) {
			for _, line := range strings.Split(string(data), "\n") {
				if strings.TrimSpace(line) == header {
					return nil, false, nil
				}
			}
			if len(data) > 0 && !bytes.HasSuffix(data, []byte("\n")) {
				data = append(data, '\n')
			}
			return append(data, "\n"+header+"\ntrust_level = \"trusted\"\n"...), true, nil
		})
	}
	return nil
}

// forgetReviewTrust removes every trust entry inside a review's directory,
// including any the harness added itself, such as Claude's entry for the
// worktree's repository.
func forgetReviewTrust(provider string, env map[string]string, dir string) error {
	dirs := []string{filepath.Clean(dir)}
	if real, err := filepath.EvalSymlinks(dir); err == nil && real != dirs[0] {
		dirs = append(dirs, real)
	}
	inside := func(key string) bool {
		for _, d := range dirs {
			if key == d || strings.HasPrefix(key, d+string(filepath.Separator)) {
				return true
			}
		}
		return false
	}
	switch provider {
	case "claude":
		return updateClaudeProjects(env, func(projects map[string]json.RawMessage) bool {
			changed := false
			for key := range projects {
				if inside(key) {
					delete(projects, key)
					changed = true
				}
			}
			return changed
		})
	case "codex":
		path, err := codexConfigFile(env)
		if err != nil {
			return err
		}
		return rewriteHarnessConfig(path, func(data []byte) ([]byte, bool, error) {
			var kept []string
			changed, skipping := false, false
			for _, line := range strings.Split(string(data), "\n") {
				if trimmed := strings.TrimSpace(line); strings.HasPrefix(trimmed, "[") {
					var key string
					quoted, ok := strings.CutPrefix(trimmed, "[projects.")
					skipping = ok && strings.HasSuffix(quoted, "]") && json.Unmarshal([]byte(strings.TrimSuffix(quoted, "]")), &key) == nil && inside(key)
					changed = changed || skipping
				}
				if !skipping {
					kept = append(kept, line)
				}
			}
			return []byte(strings.Join(kept, "\n")), changed, nil
		})
	}
	return nil
}

// claudeConfigFile is Claude's global config: $CLAUDE_CONFIG_DIR/.claude.json
// when set for the harness, otherwise ~/.claude.json.
func claudeConfigFile(env map[string]string) (string, error) {
	if dir := env["CLAUDE_CONFIG_DIR"]; dir != "" {
		return filepath.Join(dir, ".claude.json"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".claude.json"), nil
}

// codexConfigFile is $CODEX_HOME/config.toml, by default ~/.codex/config.toml.
func codexConfigFile(env map[string]string) (string, error) {
	if dir := env["CODEX_HOME"]; dir != "" {
		return filepath.Join(dir, "config.toml"), nil
	}
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".codex", "config.toml"), nil
}

// updateClaudeProjects applies change to the projects map in Claude's config.
func updateClaudeProjects(env map[string]string, change func(map[string]json.RawMessage) bool) error {
	path, err := claudeConfigFile(env)
	if err != nil {
		return err
	}
	return rewriteHarnessConfig(path, func(data []byte) ([]byte, bool, error) {
		root := map[string]json.RawMessage{}
		if len(data) > 0 && json.Unmarshal(data, &root) != nil {
			return nil, false, fmt.Errorf("Claude's config is not valid JSON; not changing it")
		}
		projects := map[string]json.RawMessage{}
		if raw := root["projects"]; raw != nil && json.Unmarshal(raw, &projects) != nil {
			return nil, false, fmt.Errorf("Claude's project settings are not valid JSON; not changing them")
		}
		if !change(projects) {
			return nil, false, nil
		}
		root["projects"], _ = json.Marshal(projects)
		out, err := json.MarshalIndent(root, "", "  ")
		return out, true, err
	})
}

// rewriteHarnessConfig applies change to a harness's config file, keeping
// everything else and its mode. The harness rewrites this file itself, so
// the new content is written beside it and renamed into place only if the
// file did not change meanwhile; otherwise the edit is retried on the new
// content. Other Vera listeners wait on a lock.
// ponytail: a harness write landing between the final check and the rename
// is lost; the harnesses have no lock to share, and the window is microseconds.
func rewriteHarnessConfig(path string, change func([]byte) ([]byte, bool, error)) error {
	if real, err := filepath.EvalSymlinks(path); err == nil {
		path = real // Keep a symlinked config (a dotfiles checkout) a symlink.
	}
	var lock *os.File
	var err error
	for try := 0; ; try++ { // Another listener holds it only for one edit.
		lock, err = lockReviewFile(filepath.Join(RootDir(), "harness-config.lock"))
		if !errors.Is(err, errReviewLockBusy) || try == 100 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if err != nil {
		return err
	}
	defer lock.Close()
	for range 5 {
		before, err := os.Stat(path)
		mode := os.FileMode(0600)
		var data []byte
		if err == nil {
			mode = before.Mode().Perm()
			if data, err = os.ReadFile(path); err != nil {
				return err
			}
		} else if !os.IsNotExist(err) {
			return err
		}
		out, changed, err := change(data)
		if err != nil || !changed {
			return err
		}
		if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return err
		}
		tmp, err := os.CreateTemp(filepath.Dir(path), "."+filepath.Base(path)+".vibeflow-*")
		if err != nil {
			return err
		}
		_, err = tmp.Write(out)
		if err == nil {
			err = tmp.Chmod(mode)
		}
		if closeErr := tmp.Close(); err == nil {
			err = closeErr
		}
		if err != nil {
			os.Remove(tmp.Name())
			return err
		}
		now, statErr := os.Stat(path)
		if (before == nil && os.IsNotExist(statErr)) || (before != nil && statErr == nil && now.Size() == before.Size() && now.ModTime().Equal(before.ModTime())) {
			if err = os.Rename(tmp.Name(), path); err != nil {
				os.Remove(tmp.Name())
			}
			return err
		}
		os.Remove(tmp.Name())
	}
	return fmt.Errorf("%s kept changing; its folder trust was not updated", filepath.Base(path))
}
