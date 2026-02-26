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
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// Worktree represents a git worktree entry parsed from porcelain output.
type Worktree struct {
	Path     string
	Branch   string
	HEAD     string
	Bare     bool
	Detached bool
}

// WorktreeManager provides git worktree CRUD operations via the git CLI.
type WorktreeManager struct {
	repoRoot string
	baseDir  string // relative to repoRoot, e.g. ".claude/worktrees"
}

// NewWorktreeManager creates a manager rooted at the given repository.
// baseDir is the directory (relative to repoRoot) where new worktrees are
// created. Returns an error if repoRoot is not inside a git repository.
func NewWorktreeManager(repoRoot, baseDir string) (*WorktreeManager, error) {
	// Verify this is actually a git repo.
	cmd := exec.Command("git", "-C", repoRoot, "rev-parse", "--show-toplevel")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("not a git repository: %w", err)
	}
	root := strings.TrimSpace(string(out))
	return &WorktreeManager{repoRoot: root, baseDir: baseDir}, nil
}

// RepoRoot returns the repository root path.
func (wm *WorktreeManager) RepoRoot() string {
	return wm.repoRoot
}

// Create adds a new git worktree. The worktree is placed under
// {repoRoot}/{baseDir}/{name}. If branch already exists it is checked out;
// otherwise a new branch is created.
func (wm *WorktreeManager) Create(name, branch string) (string, error) {
	return wm.CreateBranch(name, branch, false)
}

// CreateBranch adds a new git worktree. When newBranch is true, the branch
// is explicitly created with -b (fails if it already exists). When false,
// it tries to check out an existing branch first, then falls back to
// creating a new one.
func (wm *WorktreeManager) CreateBranch(name, branch string, newBranch bool) (string, error) {
	dir := filepath.Join(wm.repoRoot, wm.baseDir)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return "", fmt.Errorf("create base dir: %w", err)
	}

	wtPath := filepath.Join(dir, name)

	// If the worktree path already exists on disk, use a unique suffix.
	if _, statErr := os.Stat(wtPath); statErr == nil {
		wtPath = fmt.Sprintf("%s-%d", wtPath, time.Now().Unix())
	}

	if newBranch {
		// Explicitly create a new branch.
		cmd := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("create worktree with new branch %q: %s: %w", branch, strings.TrimSpace(string(out)), err)
		}
		return wtPath, nil
	}

	// Try checking out existing branch first.
	cmd := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, branch)
	if _, err := cmd.CombinedOutput(); err == nil {
		return wtPath, nil
	}

	// Branch might not exist — try creating it.
	cmd2 := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", branch)
	if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
		// Last resort: use a unique branch name to avoid conflicts.
		uniqueBranch := fmt.Sprintf("%s-wt-%d", branch, time.Now().Unix())
		cmd3 := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", uniqueBranch)
		if out3, err3 := cmd3.CombinedOutput(); err3 != nil {
			return "", fmt.Errorf("create worktree: %s: %w", combineErrors(out2, out3), err3)
		}
	}
	return wtPath, nil
}

// CreateBranchInDir creates a git worktree for the given branch inside a custom
// base directory (instead of the default baseDir). Used for the "Custom location"
// wizard option.
func (wm *WorktreeManager) CreateBranchInDir(customDir, name, branch string, newBranch bool) (string, error) {
	if err := os.MkdirAll(customDir, 0755); err != nil {
		return "", fmt.Errorf("create custom dir: %w", err)
	}

	wtPath := filepath.Join(customDir, name)

	if _, statErr := os.Stat(wtPath); statErr == nil {
		wtPath = fmt.Sprintf("%s-%d", wtPath, time.Now().Unix())
	}

	if newBranch {
		cmd := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", branch)
		if out, err := cmd.CombinedOutput(); err != nil {
			return "", fmt.Errorf("create worktree with new branch %q: %s: %w", branch, strings.TrimSpace(string(out)), err)
		}
		return wtPath, nil
	}

	cmd := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, branch)
	if _, err := cmd.CombinedOutput(); err == nil {
		return wtPath, nil
	}

	cmd2 := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", branch)
	if out2, err2 := cmd2.CombinedOutput(); err2 != nil {
		uniqueBranch := fmt.Sprintf("%s-wt-%d", branch, time.Now().Unix())
		cmd3 := exec.Command("git", "-C", wm.repoRoot, "worktree", "add", wtPath, "-b", uniqueBranch)
		if out3, err3 := cmd3.CombinedOutput(); err3 != nil {
			return "", fmt.Errorf("create worktree: %s: %w", combineErrors(out2, out3), err3)
		}
	}
	return wtPath, nil
}

// List returns all worktrees for the repository by parsing git's porcelain
// output format.
func (wm *WorktreeManager) List() ([]Worktree, error) {
	cmd := exec.Command("git", "-C", wm.repoRoot, "worktree", "list", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("list worktrees: %w", err)
	}

	var worktrees []Worktree
	var current Worktree

	for _, line := range strings.Split(string(out), "\n") {
		line = strings.TrimSpace(line)
		switch {
		case strings.HasPrefix(line, "worktree "):
			if current.Path != "" {
				worktrees = append(worktrees, current)
			}
			current = Worktree{Path: strings.TrimPrefix(line, "worktree ")}
		case strings.HasPrefix(line, "HEAD "):
			current.HEAD = strings.TrimPrefix(line, "HEAD ")
		case strings.HasPrefix(line, "branch "):
			ref := strings.TrimPrefix(line, "branch ")
			// Convert refs/heads/main → main
			current.Branch = strings.TrimPrefix(ref, "refs/heads/")
		case line == "bare":
			current.Bare = true
		case line == "detached":
			current.Detached = true
		case line == "":
			if current.Path != "" {
				worktrees = append(worktrees, current)
				current = Worktree{}
			}
		}
	}
	if current.Path != "" {
		worktrees = append(worktrees, current)
	}
	return worktrees, nil
}

// FindByBranch returns the path of the first worktree checked out on the
// given branch. Returns ("", false) if no worktree exists for that branch.
func (wm *WorktreeManager) FindByBranch(branch string) (string, bool) {
	worktrees, err := wm.List()
	if err != nil {
		return "", false
	}
	for _, wt := range worktrees {
		if wt.Branch == branch && !wt.Bare && !wt.Detached {
			return wt.Path, true
		}
	}
	return "", false
}

// BranchWorktreeMap returns a map of branch name → worktree path for all
// non-bare, non-detached worktrees. Useful for annotating branch lists.
func (wm *WorktreeManager) BranchWorktreeMap() map[string]string {
	worktrees, err := wm.List()
	if err != nil {
		return nil
	}
	m := make(map[string]string, len(worktrees))
	for _, wt := range worktrees {
		if !wt.Bare && !wt.Detached && wt.Branch != "" {
			m[wt.Branch] = wt.Path
		}
	}
	return m
}

// Remove deletes a worktree. If force is true, uncommitted changes are
// discarded; otherwise the operation fails if changes exist.
func (wm *WorktreeManager) Remove(path string, force bool) error {
	args := []string{"-C", wm.repoRoot, "worktree", "remove", path}
	if force {
		args = append(args, "--force")
	}
	cmd := exec.Command("git", args...)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("remove worktree %q: %s: %w", path, strings.TrimSpace(string(out)), err)
	}
	return nil
}

// Exists reports whether a worktree at the given path is registered with git.
func (wm *WorktreeManager) Exists(path string) bool {
	worktrees, err := wm.List()
	if err != nil {
		return false
	}
	abs, _ := filepath.Abs(path)
	for _, wt := range worktrees {
		wtAbs, _ := filepath.Abs(wt.Path)
		if wtAbs == abs {
			return true
		}
	}
	return false
}

// combineErrors joins two command outputs for a combined error message.
func combineErrors(a, b []byte) string {
	parts := make([]string, 0, 2)
	if s := strings.TrimSpace(string(a)); s != "" {
		parts = append(parts, s)
	}
	if s := strings.TrimSpace(string(b)); s != "" {
		parts = append(parts, s)
	}
	return strings.Join(parts, "; ")
}
