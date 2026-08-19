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

// newTestRepo creates a real git repo with an initial commit on "main" plus an
// extra branch, left checked out on "main". Real git, no mocks: the defect in
// #4680 is entirely about what git actually does.
func newTestRepo(t *testing.T, extraBranch string) string {
	t.Helper()
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git not installed")
	}
	dir := t.TempDir()
	run := func(args ...string) {
		t.Helper()
		cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
		cmd.Env = append(os.Environ(),
			"GIT_AUTHOR_NAME=t", "GIT_AUTHOR_EMAIL=t@t",
			"GIT_COMMITTER_NAME=t", "GIT_COMMITTER_EMAIL=t@t")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %v: %s", args, err, out)
		}
	}
	run("init", "-q", "-b", "main")
	if err := os.WriteFile(filepath.Join(dir, "f.txt"), []byte("x\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	run("add", ".")
	run("commit", "-qm", "init")
	if extraBranch != "" {
		run("branch", extraBranch)
	}
	return dir
}

// TestInPlaceLaunchChecksOutSelectedBranch reproduces #4680: selecting a branch
// for an in-place launch left the working directory on whatever branch it was
// already on, while SessionMeta and the status bar recorded the SELECTED branch.
// The user saw "develop" in the status bar while the agent ran on main, so work
// items targeting develop never surfaced and commits would have landed on main.
func TestInPlaceLaunchChecksOutSelectedBranch(t *testing.T) {
	for _, choice := range []struct {
		name   string
		choice WorktreeChoice
	}{
		{"current directory", WorktreeCurrent},
		{"specified directory", WorktreeSpecifyDir},
	} {
		t.Run(choice.name, func(t *testing.T) {
			repo := newTestRepo(t, "develop")
			if got := GetGitBranch(repo); got != "main" {
				t.Fatalf("setup: repo should start on main, got %q", got)
			}

			m := Model{config: DefaultConfig()}
			res := WizardResult{
				Branch:           "develop",
				WorktreeChoice:   choice.choice,
				WorkDir:          repo,
				SpecifiedWorkDir: repo,
			}

			workDir, _, err := m.resolveSessionWorkDir(res)
			if err != nil {
				t.Fatalf("resolveSessionWorkDir: %v", err)
			}
			if workDir != repo {
				t.Fatalf("workDir = %q, want %q", workDir, repo)
			}

			// THE BUG: without the fix the repo is still on main here.
			if got := GetGitBranch(workDir); got != "develop" {
				t.Errorf("selected branch was not checked out: working dir is on %q, want %q", got, "develop")
			}
		})
	}
}

// TestInPlaceLaunchRefusesDirtyTree pins the guard the switch/edit path already
// had: never check out over uncommitted work.
func TestInPlaceLaunchRefusesDirtyTree(t *testing.T) {
	repo := newTestRepo(t, "develop")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Model{config: DefaultConfig()}
	_, _, err := m.resolveSessionWorkDir(WizardResult{
		Branch:         "develop",
		WorktreeChoice: WorktreeCurrent,
		WorkDir:        repo,
	})
	if err == nil {
		t.Fatal("dirty working tree must refuse the in-place checkout, got nil error")
	}
	if got := GetGitBranch(repo); got != "main" {
		t.Errorf("refused launch must not move the working tree: on %q, want main", got)
	}
}

// TestSameBranchIsNoOp guards against churn: if the directory is already on the
// selected branch there is nothing to do, dirty or not.
func TestSameBranchIsNoOp(t *testing.T) {
	repo := newTestRepo(t, "")
	if err := os.WriteFile(filepath.Join(repo, "dirty.txt"), []byte("uncommitted\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	m := Model{config: DefaultConfig()}
	if _, _, err := m.resolveSessionWorkDir(WizardResult{
		Branch:         "main",
		WorktreeChoice: WorktreeCurrent,
		WorkDir:        repo,
	}); err != nil {
		t.Fatalf("already on the selected branch must not error even when dirty: %v", err)
	}
}

// TestEffectiveBranchNeverLies covers part B of #4680. The agent registers with
// the server using its own `git branch --show-current`, so any UI surface that
// echoes the REQUESTED branch instead can disagree with it - which is exactly
// how the status bar showed "develop" while wait_for_work filtered on main.
func TestEffectiveBranchNeverLies(t *testing.T) {
	repo := newTestRepo(t, "develop")

	t.Run("reports reality, not the request", func(t *testing.T) {
		if got := effectiveBranch(repo, "develop"); got != "main" {
			t.Errorf("effectiveBranch = %q, want %q (the branch the repo is actually on)", got, "main")
		}
	})

	t.Run("agrees once the branch is checked out", func(t *testing.T) {
		if err := ensureBranchCheckedOut(repo, "develop", false, ""); err != nil {
			t.Fatalf("ensureBranchCheckedOut: %v", err)
		}
		if got := effectiveBranch(repo, "develop"); got != "develop" {
			t.Errorf("effectiveBranch = %q, want develop", got)
		}
	})

	t.Run("falls back to the request outside a git repo", func(t *testing.T) {
		// A non-repo working directory is legitimate; there is nothing to
		// contradict, so the requested value is the best label available.
		if got := effectiveBranch(t.TempDir(), "develop"); got != "develop" {
			t.Errorf("effectiveBranch = %q, want the requested %q", got, "develop")
		}
	})

	t.Run("empty workdir falls back", func(t *testing.T) {
		if got := effectiveBranch("", "develop"); got != "develop" {
			t.Errorf("effectiveBranch = %q, want develop", got)
		}
	})
}

// TestEnsureBranchCheckedOutIsSafeOutsideRepos pins the no-op cases so the guard
// cannot start failing launches in directories it has no business touching.
func TestEnsureBranchCheckedOutIsSafeOutsideRepos(t *testing.T) {
	for _, tc := range []struct{ name, dir, branch string }{
		{"empty dir", "", "develop"},
		{"empty branch", t.TempDir(), ""},
		{"not a git repo", t.TempDir(), "develop"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if err := ensureBranchCheckedOut(tc.dir, tc.branch, false, ""); err != nil {
				t.Errorf("must be a no-op, got %v", err)
			}
		})
	}
}
