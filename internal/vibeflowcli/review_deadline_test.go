//go:build darwin || linux

package vibeflowcli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// The attempt limit is the server's round deadline; --timeout only shortens it
// (issue #6299).
func TestReviewWatchTimeoutBounds(t *testing.T) {
	if def := reviewWatchCmd().Flags().Lookup("timeout").DefValue; def != "0s" {
		t.Fatalf("default timeout %q; want 0 (server deadline)", def)
	}
	previousRoot, previousConfig := rootDir, flagConfigPath
	t.Cleanup(func() { rootDir = previousRoot; flagConfigPath = previousConfig })
	SetRootDir(t.TempDir())
	flagConfigPath = filepath.Join(RootDir(), "config.yaml")
	for _, tc := range []struct {
		timeout string
		ok      bool
	}{{"0", true}, {"30s", false}, {"1m", true}, {"1h", true}, {"2h", false}} {
		t.Run(tc.timeout, func(t *testing.T) {
			cmd := reviewWatchCmd()
			cmd.SetOut(io.Discard)
			cmd.SetErr(io.Discard)
			cmd.SetArgs([]string{"--project", "1", "--repository-link", "7", "--timeout", tc.timeout})
			err := cmd.ExecuteContext(context.Background())
			rejected := err != nil && strings.Contains(err.Error(), "timeout must be 0 or 1m to 1h")
			if rejected == tc.ok {
				t.Fatalf("timeout %s ok=%v: %v", tc.timeout, tc.ok, err)
			}
		})
	}
}

func TestReviewAttemptLimitFollowsServerDeadline(t *testing.T) {
	for _, tc := range []struct {
		name     string
		timeout  time.Duration
		deadline time.Duration // The round deadline at claim.
		renewed  time.Duration // The round deadline the server returns once checkout is reported.
		limit    string
		finishes bool
	}{
		{name: "server deadline", deadline: time.Second, renewed: time.Second, limit: "(server deadline)"},
		{name: "timeout shortens", timeout: time.Second, deadline: time.Minute, renewed: 2 * time.Minute, limit: "(--timeout)"},
		{name: "renew extends", deadline: 3 * time.Second, renewed: time.Minute, limit: "(server deadline)", finishes: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			source, execution := reviewTestRepo(t)
			execution.ProgressReportingVersion = 1
			execution.Attempt.Round.DeadlineAt = time.Now().Add(tc.deadline).UnixMilli()
			execution.Attempt.LeaseExpiresAt = time.Now().Add(5 * time.Minute).UnixMilli()
			content := json.RawMessage(`{"findings":[]}`)
			digest := sha256.Sum256(content)
			brief := reviewBrief{RoundID: execution.Attempt.Round.ID, Digest: hex.EncodeToString(digest[:]), Content: content}
			result := map[string]any{
				"schema_version": 1, "brief_digest": brief.Digest,
				"head_sha": execution.Review.HeadSHA, "base_sha": execution.Review.BaseSHA,
				"outcome": "clean", "summary": "fixture", "new_findings": []any{}, "reconciliations": []any{},
			}
			root := t.TempDir()
			provider := filepath.Join(root, "provider")
			// Slower than every deadline at claim, faster than the extended one.
			script := "#!/bin/sh\nsleep 5\n" + reviewWriteResult(reviewResultEnvelope(t, result))
			if err := os.WriteFile(provider, []byte(script), 0700); err != nil {
				t.Fatal(err)
			}
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.Method == "GET" && strings.HasSuffix(r.URL.Path, "/brief"):
					json.NewEncoder(w).Encode(brief)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/heartbeat"):
					w.WriteHeader(http.StatusNoContent)
				case r.Method == "POST" && strings.HasSuffix(r.URL.Path, "/renew"):
					var body struct {
						Progress reviewProgressInput `json:"progress"`
					}
					if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
						t.Error(err)
					}
					renewed := *execution
					if body.Progress.CheckoutPrepared {
						renewed.Attempt.Round.DeadlineAt = time.Now().Add(tc.renewed).UnixMilli()
					}
					json.NewEncoder(w).Encode(renewed)
				default:
					t.Errorf("unexpected request: %s %s", r.Method, r.URL.Path)
					w.WriteHeader(http.StatusNotFound)
				}
			}))
			defer server.Close()
			cfg := DefaultConfig()
			cfg.Providers["claude"] = Provider{Binary: provider}
			var out bytes.Buffer
			watch := &reviewWatch{
				client: NewClient(server.URL, "token"), cfg: cfg, root: root, output: &out,
				state:   reviewRunnerState{ID: execution.Attempt.RunnerID},
				options: reviewWatchOptions{ProjectID: 1, Repository: source, Provider: "claude", Timeout: tc.timeout},
			}
			started := time.Now()
			_, err := watch.execute(context.Background(), &reviewReceipt{JobID: execution.Review.ID, RequestID: reviewUUID(), Execution: execution})
			if tc.finishes {
				if err != nil {
					t.Fatalf("extended attempt failed: %v", err)
				}
			} else if err == nil || !strings.Contains(err.Error(), "deadline_exceeded") || time.Since(started) > 4*time.Second {
				t.Fatalf("attempt was not stopped at its limit: %v after %s", err, time.Since(started))
			}
			if !strings.Contains(out.String(), "Attempt limit: ") || !strings.Contains(out.String(), tc.limit) {
				t.Fatalf("attempt limit line missing %q:\n%s", tc.limit, out.String())
			}
		})
	}
}

// The child guard moves its own deadline when the supervisor sends a later one.
func TestReviewChildExtendsDeadlineFromSupervisor(t *testing.T) {
	root := t.TempDir()
	input := filepath.Join(root, "task.txt")
	if err := os.WriteFile(input, nil, 0600); err != nil {
		t.Fatal(err)
	}
	spec := reviewChildSpec{Binary: "/bin/sh", Args: []string{"-c", "sleep 1"}, Env: []string{"PATH=/usr/bin:/bin"}, Dir: root, InputFile: input, DeadlineAt: time.Now().Add(300 * time.Millisecond).UnixMilli()}
	path := filepath.Join(root, "child.json")
	if err := saveReviewJSON(path, spec); err != nil {
		t.Fatal(err)
	}
	reader, writer := io.Pipe()
	defer reader.Close()
	defer writer.Close()
	go func() {
		var frame [8]byte
		binary.BigEndian.PutUint64(frame[:], uint64(time.Now().Add(10*time.Second).UnixMilli()))
		_, _ = writer.Write(append([]byte{'R'}, frame[:]...))
	}()
	command := reviewChildCmd()
	command.SetContext(context.Background())
	command.SetIn(reader)
	command.SetOut(io.Discard)
	command.SetErr(io.Discard)
	if err := command.RunE(command, []string{path}); err != nil {
		t.Fatalf("extended child was stopped: %v", err)
	}
}
