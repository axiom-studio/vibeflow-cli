package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestReviewRemoteEnterpriseSSHIdentity(t *testing.T) {
	for _, remote := range []string{"tenant@tenant.ghe.com:acme/repo.git", "ssh://tenant@tenant.ghe.com/acme/repo.git"} {
		host, name, err := reviewRemoteIdentity(remote)
		if err != nil || host != "tenant.ghe.com" || name != "acme/repo" {
			t.Errorf("valid Enterprise SSH checkout %q rejected: %s %s %v", remote, host, name, err)
		}
	}
	for _, remote := range []string{"ssh://tenant:secret@tenant.ghe.com/acme/repo.git", "ssh://wrong@tenant.ghe.com/acme/repo.git", "tenant@tenant.ghe.com.evil.invalid:acme/repo.git", "https://token@github.com/acme/repo.git", "ssh://%74enant@tenant.ghe.com/acme/repo.git", "ssh://-option@tenant.ghe.com/acme/repo.git"} {
		if _, _, err := reviewRemoteIdentity(remote); err == nil {
			t.Errorf("unsafe remote accepted: %q", remote)
		}
	}
}

func TestReviewCommandRetainsSanitizedProviderFailure(t *testing.T) {
	binary := filepath.Join(t.TempDir(), "vibeflow")
	if out, err := exec.Command("go", "build", "-o", binary, "../../cmd/vibeflow").CombinedOutput(); err != nil {
		t.Fatalf("build CLI: %v %s", err, out)
	}
	for _, tc := range []struct {
		name, response, exit, category string
		httpStatus                     string
	}{
		{"api_exit_0", `{"is_error":true,"api_error_status":429,"terminal_reason":"untrusted-secret","result":"untrusted-secret"}`, "exit 0", "rate_limited", "429"},
		{"api_exit_1", `{"is_error":true,"api_error_status":429,"terminal_reason":"untrusted-secret","result":"untrusted-secret"}`, "exit 1", "rate_limited", "429"},
		{"exit_17", "untrusted-secret", "exit 17", "provider_exit", ""},
		{"signal", "untrusted-secret", "kill -TERM $$", "provider_signal", ""},
		{"invalid_result", "untrusted-secret", "exit 0", "invalid_result", ""},
		{"reported_failure", `{"structured_output":{"result":null,"failure_reason":"untrusted-secret"}}`, "exit 0", "provider_reported_failure", ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			repo, execution := reviewTestRepo(t)
			root := t.TempDir()
			provider := filepath.Join(root, "provider")
			script := "#!/bin/sh\nfor arg in \"$@\"; do if [ \"$arg\" = --help ]; then echo '--safe-mode --restricted --strict-mcp-config --tools --permission-prompts --json-schema --no-session-persistence'; exit 0; fi; done\nprintf '%s\\n' " + shellQuote(tc.response) + "\necho stderr-untrusted-secret >&2\n" + tc.exit + "\n"
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			var reason string
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/pr-review-runners"):
					var registration map[string]any
					json.NewDecoder(r.Body).Decode(&registration)
					execution.Attempt.RunnerID = registration["id"].(string)
					registration["user_id"] = 1
					json.NewEncoder(w).Encode(registration)
				case strings.HasSuffix(r.URL.Path, "/work"):
					json.NewEncoder(w).Encode(map[string]any{"reviews": []reviewJob{execution.Review}})
				case strings.HasSuffix(r.URL.Path, "/claim"), strings.HasSuffix(r.URL.Path, "/renew"):
					json.NewEncoder(w).Encode(execution)
				case strings.HasSuffix(r.URL.Path, "/brief"):
					digest := sha256.Sum256([]byte("{}"))
					json.NewEncoder(w).Encode(reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: json.RawMessage(`{}`)})
				case strings.HasSuffix(r.URL.Path, "/fail"):
					var body map[string]string
					json.NewDecoder(r.Body).Decode(&body)
					reason = body["reason"]
					w.WriteHeader(204)
				case strings.HasSuffix(r.URL.Path, "/heartbeat"), r.Method == "DELETE":
					w.WriteHeader(204)
				default:
					t.Errorf("unexpected API call %s", r.URL.Path)
					w.WriteHeader(400)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.ServerURL, cfg.APIToken = server.URL, "supervisor-secret"
			cfg.Providers["claude"] = Provider{Binary: provider}
			config := filepath.Join(root, "config.yaml")
			if err := SaveConfig(cfg, config); err != nil {
				t.Fatal(err)
			}
			ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, binary, "--cra", "--root", root, "--config", config, "review-watch", "--project", "1", "--repo", repo, "--repository-link", "7", "--provider", "claude", "--name", "diagnostic", "--once")
			output, err := cmd.CombinedOutput()
			if err != nil || !strings.Contains(reason, tc.category) || !strings.Contains(reason, tc.httpStatus) {
				t.Errorf("provider cause lost: %v reason=%q output=%s", err, reason, output)
			}
			paths, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "last-provider-diagnostic.json"))
			if len(paths) != 1 {
				t.Fatalf("missing private diagnostic: %v", paths)
			}
			data, err := os.ReadFile(paths[0])
			if err != nil || !bytes.Contains(data, []byte(tc.category)) || !bytes.Contains(data, []byte(tc.httpStatus)) || bytes.Contains(data, []byte("secret")) || bytes.Contains(output, []byte("secret")) || strings.Contains(reason, "secret") {
				t.Fatalf("unsafe or incomplete diagnostic: %s %v", data, err)
			}
			var diagnostic struct {
				Stage    string `json:"stage"`
				ExitCode *int   `json:"exit_code"`
				Signal   int    `json:"signal"`
			}
			if err := json.Unmarshal(data, &diagnostic); err != nil {
				t.Fatal(err)
			}
			if tc.name == "exit_17" && (diagnostic.Stage != "provider" || diagnostic.ExitCode == nil || *diagnostic.ExitCode != 17) {
				t.Fatalf("actual provider exit was replaced by guard exit: %s", data)
			}
			if tc.name == "signal" && diagnostic.Signal != 15 {
				t.Fatalf("provider signal was discarded: %s", data)
			}
			if info, err := os.Stat(paths[0]); err != nil || info.Mode().Perm() != 0600 {
				t.Fatalf("diagnostic not private: %v", err)
			}
			work, _ := filepath.Glob(filepath.Join(root, "review-runners", "*", "work", "*"))
			if len(work) != 0 {
				t.Fatalf("provider failure left private input/auth: %v", work)
			}
		})
	}
}

func TestReviewCheckoutFetchesMissingCommitsUsingOriginSSH(t *testing.T) {
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	upstream, execution := reviewTestRepo(t)
	for _, fork := range []bool{false, true} {
		t.Run(map[bool]string{false: "same_repository", true: "fork"}[fork], func(t *testing.T) {
			source := t.TempDir()
			reviewTestGit(t, source, "init")
			reviewTestGit(t, source, "fetch", upstream, execution.Review.BaseSHA)
			reviewTestGit(t, source, "remote", "add", "origin", "git@github.com:acme/repo.git")
			if err := exec.Command("git", "-C", source, "cat-file", "-e", execution.Review.HeadSHA+"^{commit}").Run(); err == nil {
				t.Fatal("fixture already contains the supposedly missing commit")
			}
			if fork {
				execution.Attempt.Round.Details.HeadRepositoryName = "acme/fork"
				execution.Attempt.Round.Details.HeadCloneURL = "https://github.com/acme/fork.git"
			}
			ssh := filepath.Join(t.TempDir(), "ssh")
			script := "#!/bin/sh\ncase \"$*\" in\n *\"git@github.com git-upload-pack '/" + execution.Attempt.Round.Details.HeadRepositoryName + ".git'\") exec git-upload-pack " + shellQuote(upstream) + " ;;\n *) exit 91 ;;\nesac\n"
			if err := os.WriteFile(ssh, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			t.Setenv("GIT_SSH_COMMAND", shellQuote(ssh))
			t.Setenv("GIT_SSH_VARIANT", "ssh")
			t.Setenv("GIT_CONFIG_COUNT", "1")
			t.Setenv("GIT_CONFIG_KEY_0", "protocol.https.allow")
			t.Setenv("GIT_CONFIG_VALUE_0", "never")
			if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), execution); err != nil {
				t.Fatalf("SSH-only runner cannot fetch the new PR head: %v", err)
			}
			if err := exec.Command("git", "-C", source, "cat-file", "-e", execution.Review.HeadSHA+"^{commit}").Run(); err == nil {
				t.Fatal("fetch wrote into the developer checkout")
			}
			execution.Attempt.Round.Details.HeadCloneURL = "https://foreign.invalid/acme/fork.git"
			if err := prepareReviewCheckout(context.Background(), source, t.TempDir(), execution); err == nil {
				t.Fatal("foreign clone host was accepted")
			}
			execution.Attempt.Round.Details.HeadRepositoryName = "acme/repo"
			execution.Attempt.Round.Details.HeadCloneURL = "https://github.com/acme/repo.git"
		})
	}
}

func TestReviewRunnerNameMatchesBackendLimit(t *testing.T) {
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir = previousRoot; flagConfigPath = previousConfig })
	SetRootDir(t.TempDir())
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	for _, length := range []int{100, 101} {
		cmd := reviewWatchCmd()
		cmd.SetOut(io.Discard)
		cmd.SetErr(io.Discard)
		cmd.SetArgs([]string{"--project", "1", "--repository-link", "7", "--name", strings.Repeat("n", length)})
		err := cmd.ExecuteContext(context.Background())
		rejected := err != nil && strings.Contains(err.Error(), "runner name")
		if rejected != (length > 100) {
			t.Errorf("name length %d local validation: %v", length, err)
		}
	}
}
