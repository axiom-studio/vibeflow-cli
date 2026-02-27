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
	"os"
	"os/exec"
	"path/filepath"
	"testing"
)

func TestCombineErrors(t *testing.T) {
	t.Run("both non-empty", func(t *testing.T) {
		got := combineErrors([]byte("error one"), []byte("error two"))
		if got != "error one; error two" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("first empty", func(t *testing.T) {
		got := combineErrors([]byte(""), []byte("error two"))
		if got != "error two" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("second empty", func(t *testing.T) {
		got := combineErrors([]byte("error one"), []byte(""))
		if got != "error one" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("both empty", func(t *testing.T) {
		got := combineErrors([]byte(""), []byte(""))
		if got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("whitespace only", func(t *testing.T) {
		got := combineErrors([]byte("  \n"), []byte("\t  "))
		if got != "" {
			t.Errorf("got %q", got)
		}
	})

	t.Run("trims whitespace", func(t *testing.T) {
		got := combineErrors([]byte("  error one\n"), []byte("\terror two  "))
		if got != "error one; error two" {
			t.Errorf("got %q", got)
		}
	})
}

// initTestRepo creates a temporary git repository with an initial commit.
// Returns the repo path. Caller uses t.TempDir() cleanup.
func initTestRepo(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()

	cmds := [][]string{
		{"git", "init", dir},
		{"git", "-C", dir, "config", "user.email", "test@test.com"},
		{"git", "-C", dir, "config", "user.name", "Test"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %s: %v", args, out, err)
		}
	}

	// Create an initial commit so we have a HEAD.
	readmePath := filepath.Join(dir, "README.md")
	if err := os.WriteFile(readmePath, []byte("# Test\n"), 0644); err != nil {
		t.Fatal(err)
	}
	cmds = [][]string{
		{"git", "-C", dir, "add", "."},
		{"git", "-C", dir, "commit", "-m", "initial commit"},
	}
	for _, args := range cmds {
		cmd := exec.Command(args[0], args[1:]...)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git command %v failed: %s: %v", args, out, err)
		}
	}

	return dir
}

func TestNewWorktreeManager(t *testing.T) {
	t.Run("valid git repo", func(t *testing.T) {
		repo := initTestRepo(t)
		wm, err := NewWorktreeManager(repo, ".worktrees")
		if err != nil {
			t.Fatalf("unexpected error: %v", err)
		}
		// Resolve symlinks (macOS /var → /private/var) before comparing.
		gotReal, _ := filepath.EvalSymlinks(wm.RepoRoot())
		wantReal, _ := filepath.EvalSymlinks(repo)
		if gotReal != wantReal {
			t.Errorf("RepoRoot() = %q, want %q", gotReal, wantReal)
		}
	})

	t.Run("not a git repo", func(t *testing.T) {
		dir := t.TempDir()
		_, err := NewWorktreeManager(dir, ".worktrees")
		if err == nil {
			t.Fatal("expected error for non-git directory")
		}
	})
}

func TestWorktreeManager_List(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	// Initially should have at least the main worktree.
	worktrees, err := wm.List()
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(worktrees) < 1 {
		t.Fatal("expected at least 1 worktree (main)")
	}

	// Main worktree should have a branch.
	repoReal, _ := filepath.EvalSymlinks(repo)
	found := false
	for _, wt := range worktrees {
		wtReal, _ := filepath.EvalSymlinks(wt.Path)
		if wtReal == repoReal {
			found = true
			if wt.Branch == "" {
				t.Error("main worktree should have a branch")
			}
		}
	}
	if !found {
		t.Errorf("main worktree (%s) not found in list", repo)
	}
}

func TestWorktreeManager_CreateAndExists(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	// Create a worktree.
	wtPath, err := wm.Create("test-wt", "test-branch")
	if err != nil {
		t.Fatalf("Create failed: %v", err)
	}

	// Verify path exists on disk.
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Errorf("worktree path %q should exist on disk", wtPath)
	}

	// Verify Exists returns true.
	if !wm.Exists(wtPath) {
		t.Error("Exists should return true for created worktree")
	}

	// Verify FindByBranch works.
	path, found := wm.FindByBranch("test-branch")
	if !found {
		t.Error("FindByBranch should find test-branch")
	}
	absWT, _ := filepath.Abs(wtPath)
	absFound, _ := filepath.Abs(path)
	if absWT != absFound {
		t.Errorf("FindByBranch returned %q, want %q", absFound, absWT)
	}
}

func TestWorktreeManager_BranchWorktreeMap(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	// Create a second worktree.
	_, err = wm.Create("map-test", "map-branch")
	if err != nil {
		t.Fatal(err)
	}

	m := wm.BranchWorktreeMap()
	if len(m) < 2 {
		t.Errorf("expected at least 2 entries in map, got %d", len(m))
	}
	if _, ok := m["map-branch"]; !ok {
		t.Error("map-branch should be in BranchWorktreeMap")
	}
}

func TestWorktreeManager_Remove(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath, err := wm.Create("removable", "remove-branch")
	if err != nil {
		t.Fatal(err)
	}

	if err := wm.Remove(wtPath, false); err != nil {
		t.Fatalf("Remove failed: %v", err)
	}

	if wm.Exists(wtPath) {
		t.Error("Exists should return false after removal")
	}
}

func TestWorktreeManager_FindByBranch_NotFound(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	_, found := wm.FindByBranch("nonexistent-branch")
	if found {
		t.Error("expected not found for nonexistent branch")
	}
}

func TestWorktreeManager_CreateBranch_NewBranch(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	wtPath, err := wm.CreateBranch("new-wt", "explicitly-new", true)
	if err != nil {
		t.Fatalf("CreateBranch failed: %v", err)
	}
	if _, statErr := os.Stat(wtPath); statErr != nil {
		t.Error("worktree should exist on disk")
	}

	path, found := wm.FindByBranch("explicitly-new")
	if !found {
		t.Error("explicitly-new branch should exist")
	}
	if path == "" {
		t.Error("path should be non-empty")
	}
}

func TestWorktreeManager_Exists_NotRegistered(t *testing.T) {
	repo := initTestRepo(t)
	wm, err := NewWorktreeManager(repo, ".worktrees")
	if err != nil {
		t.Fatal(err)
	}

	if wm.Exists("/tmp/nonexistent-worktree-xyz-123") {
		t.Error("should return false for unregistered path")
	}
}
