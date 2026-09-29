package vibeflowcli

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// A relative --root must not become relative to the child guard's new cwd.
// This runs the real CLI, checkout export and child guard, not just a provider.
func TestReviewCommandRelativeRootCompletes(t *testing.T) {
	testReviewSupervisorCompletes(t, false)
}

func TestReviewInstalledSupervisorAcceptance(t *testing.T) {
	if os.Getenv("VIBEFLOW_REVIEW_SUPERVISOR_ACCEPTANCE") != "claude" {
		t.Skip("set VIBEFLOW_REVIEW_SUPERVISOR_ACCEPTANCE=claude to spend one bounded model call through the real CLI supervisor")
	}
	testReviewSupervisorCompletes(t, true)
}

func testReviewSupervisorCompletes(t *testing.T, installed bool) {
	t.Helper()
	if testing.Short() {
		t.Skip("builds and runs the CLI")
	}
	binary := filepath.Join(t.TempDir(), "vibeflow")
	build := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow")
	if output, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v: %s", err, output)
	}
	for _, relative := range []bool{false, true} {
		if installed && !relative {
			continue
		}
		t.Run(fmt.Sprintf("relative=%v", relative), func(t *testing.T) {
			source, execution := reviewTestRepo(t)
			if installed {
				execution.Prompt = "Review this owned tiny acceptance fixture. Return a valid result with a brief summary and empty finding/reconciliation arrays if the value change has no confirmed blocker."
				execution.Attempt.Round.DeadlineAt = time.Now().Add(90 * time.Second).UnixMilli()
			}
			root := t.TempDir()
			content := json.RawMessage(`{"findings":[]}`)
			digest := sha256.Sum256(content)
			brief := reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: content}
			result := map[string]any{
				"schema_version": 1, "brief_digest": brief.Digest,
				"head_sha": execution.Review.HeadSHA, "base_sha": execution.Review.BaseSHA,
				"outcome": "clean", "summary": "Owned fixture review", "new_findings": []any{}, "reconciliations": []any{},
			}
			provider := filepath.Join(root, "owned-provider")
			// The harness must start inside a git worktree detached at the head.
			script := "#!/bin/sh\n[ \"$(git rev-parse HEAD)\" = " + execution.Review.HeadSHA + " ] || exit 5\n[ -z \"$(git branch --show-current)\" ] || exit 6\necho scratch > build-output.txt\n" + reviewWriteResult(reviewResultEnvelope(t, result))
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var results, failures atomic.Int64
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get("Authorization") != "Bearer owned-supervisor-token" {
					t.Error("supervisor authorization was not preserved")
					w.WriteHeader(http.StatusUnauthorized)
					return
				}
				switch {
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/pr-review-repositories"):
					fmt.Fprint(w, `{"repositories":[]}`)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
					var body map[string]any
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					body["user_id"] = 1
					execution.Attempt.RunnerID, _ = body["id"].(string)
					json.NewEncoder(w).Encode(body)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/work"):
					json.NewEncoder(w).Encode(map[string]any{"reviews": []reviewJob{execution.Review}, "next_after_id": ""})
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/claim"):
					json.NewEncoder(w).Encode(execution)
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/brief"):
					json.NewEncoder(w).Encode(brief)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/renew"):
					json.NewEncoder(w).Encode(execution)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/result"):
					var submitted map[string]any
					if err := json.NewDecoder(r.Body).Decode(&submitted); err != nil || submitted["head_sha"] != execution.Review.HeadSHA || submitted["brief_digest"] != brief.Digest {
						t.Error("supervisor submitted a different result")
					}
					results.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/fail"):
					failures.Add(1)
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "DELETE":
					w.WriteHeader(http.StatusNoContent)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken = server.URL, "owned-supervisor-token"
			if !installed {
				cfg.Providers["claude"] = Provider{Binary: provider}
			}
			if err := SaveConfig(cfg, filepath.Join(root, "config.yaml")); err != nil {
				t.Fatal(err)
			}
			rootArg := root
			if relative {
				rootArg = "."
			}
			duration := 30 * time.Second
			if installed {
				duration = 100 * time.Second
			}
			ctx, cancel := context.WithTimeout(context.Background(), duration)
			defer cancel()
			command := exec.CommandContext(ctx, binary, "--cra", "--root", rootArg, "review-watch", "--project", "1", "--repository-link", "7", "--git-provider", "github", "--provider", "claude", "--repo", source, "--once")
			command.Dir = root
			command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "USER=review-fixture", "LOGNAME=review-fixture"}
			if installed {
				command.Args = append(command.Args, "--model", "haiku")
				command.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + os.Getenv("HOME"), "USER=" + os.Getenv("USER"), "LOGNAME=" + os.Getenv("LOGNAME")}
			}
			output, err := command.CombinedOutput()
			if err != nil {
				t.Fatalf("CLI failed: %v: %s", err, output)
			}
			if results.Load() != 1 || failures.Load() != 0 {
				t.Fatalf("real supervisor did not complete: results=%d failures=%d output=%s", results.Load(), failures.Load(), output)
			}
			assertReviewCleanedUp(t, root, source, execution.Review.HeadSHA)
		})
	}
}

// assertReviewCleanedUp proves no review directory or worktree survives and
// that the developer's checkout gained no worktree, branch or modification.
func assertReviewCleanedUp(t *testing.T, root, source, head string) {
	t.Helper()
	left, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "work", "*"))
	if len(left) != 0 {
		t.Fatalf("review files survived: %v", left)
	}
	if list := reviewTestGit(t, source, "worktree", "list", "--porcelain"); strings.Count(list, "worktree ") != 1 {
		t.Fatalf("developer repository gained a worktree: %s", list)
	}
	if branches := reviewTestGit(t, source, "for-each-ref", "--format=%(refname)", "refs/heads"); strings.Count(branches, "\n") != 0 {
		t.Fatalf("developer repository gained a branch: %s", branches)
	}
	if status := reviewTestGit(t, source, "status", "--porcelain"); status != "" || reviewTestGit(t, source, "rev-parse", "HEAD") != head {
		t.Fatalf("developer checkout changed: %q", status)
	}
}
